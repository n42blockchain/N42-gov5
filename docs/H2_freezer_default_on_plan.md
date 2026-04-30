# H2: Receipts/Bytecodes Default-On Freezer — Audit + Plan

Status: planning. Implementation deferred until 12498920 prefetcher race
is settled.

## Audit summary

### Receipts

**ethexec path** (cmd/ethexec/main.go forward replay):
- `internal/ethel/process.go::ProcessBlock` computes per-block receipts
  in memory.
- Receipts are NOT written to MDBX from ethexec — verified by
  `db-stats --datadir d:/N42-ethverify`: no `Receipts` row in MDBX
  table list (only Storage / HashedStorage / Account / HashedAccount /
  Code / SyncStage).
- ethexec only USES receipts for diagnostics: `executor.go:1052` reads
  Geth's `freezer.TableReceipts` for gas-mismatch comparison.
- → For ethexec, **Receipts is already freezer-only**. No migration
  needed.

**cmd/n42 main node path**:
- `modules/rawdb/accessors_chain_receipts.go::WriteReceipts(tx, n, receipts)`
  → `tx.Put(modules.Receipts, EncodeBlockNumber(n), data)`.
- Three write entry points: `WriteReceipts` (174), `WriteReceiptsPooled`
  (210), `AppendReceipts` (236).
- `modules/rawdb/freezer_integration.go::CollectFreezeData` reads
  Receipts from MDBX; `CleanupFrozenData` deletes from MDBX after
  freeze. The freeze/cleanup pair exists but is NOT auto-triggered
  anywhere in the current main tree.
- → For cmd/n42, the work is to: (a) **replace `tx.Put(Receipts)` with
  freezer.Append** in the three write paths, and (b) **drop MDBX
  Receipts table entirely** OR leave a thin `< freezeThreshold`
  buffer.

### Bytecodes (Code table)

**ethexec path**:
- `internal/ethel/buffered_plain_state.go` accumulates code in
  `buf.code[codeHash]`.
- `BufferSnapshot.ApplyTo` step 4 writes via cursor.Put to
  `modules.Code` (codeHash → bytecode).
- For d:/N42-ethverify: Code table = 413K entries / 2.6 GB / avg 6.3 KB.
- → MDBX is the current store. Migration target: SegmentStore-style
  (codeHash → file offset, RecSplit MPHF).

**Read path**:
- `BufferedPlainStateReader.ReadAccountCode` checks
  `buf.code[codeHash]` → `inFlight.code` → `readCode` LRU →
  `tx.GetOne(modules.Code, codeHash[:])`.
- Many call sites; per-block reads are infrequent (only when EXTCODE*
  / CALL hits a contract whose code is not in LRU). LRU 2 GB budget
  catches most.

## The Receipts batch-size concern (user-flagged)

Default `freezer.BatchSize = 64` (modules/rawdb/freezer/freezer.go:65)
applies to every batch-mode table. For Receipts in the DeFi era:

| Block era | Avg receipt size | Batch size 64 |
|-----------|------------------|---------------|
| Pre-DeFi (block < 5M) | ~5 KB | ~320 KB / batch |
| DeFi 10M-15M | ~50 KB | ~3.2 MB / batch |
| Post-Merge with blobs | ~150 KB | ~9.6 MB / batch |

Retrieving ONE receipt forces zstd decompression of the whole batch.
At 9.6 MB/batch the amplification on a single-receipt RPC query is
~60×. That kills `eth_getTransactionReceipt` latency.

**Fix**: per-table BatchSize override. Existing `setBatchSize(int)`
on FreezerTable supports it (table.go:468); but `BatchSize` is referenced
as a const in many places (`freezer.go:95, 487, 491, 495 …`).

### Proposed BatchSize per table

| Table | Avg item size | Recommended batch | Compression unit | Decomp on Retrieve |
|-------|---------------|--------------------|------------------|---------------------|
| receipts | 50-150 KB | **8** | 400 KB - 1.2 MB | ~600 KB |
| bodies | 1-3 KB | 64 (current) | 64-200 KB | ~130 KB |
| headers | 0.5 KB | 64 | 32 KB | 32 KB |
| acctcs | 1-5 KB | 64 | 64-300 KB | ~180 KB |
| storcs | 1-10 KB | 32 | 32-320 KB | ~170 KB |
| witness | 0.5-2 KB | 64 | 32-130 KB | ~80 KB |
| senders | tx_count × 20 B | 64 | 200 KB - 6 MB | varies |
| **bytecodes** | 200 B - 24 KB | **N/A** | content-addressed → SegmentStore + RecSplit, not freezer | |

senders is interesting: for blocks with 200 tx × 20 B = 4 KB per item,
batch 64 = 256 KB. For blocks with 0 tx (empty), batch 64 = nearly empty.
Current 64 is OK.

## Implementation plan

### Phase H2.A — Per-table BatchSize override (~150 LOC)

```go
// modules/rawdb/freezer/freezer.go

// BatchSize is the DEFAULT batch size. Tables can override via the
// TableSpec.BatchSize field for tables whose item size makes a smaller
// or larger batch optimal (e.g. Receipts shrinks batches to limit
// per-item retrieve amplification).
const BatchSize = 64

type tableSpec struct {
    name      string
    ext       string
    batchSize int  // 0 → use BatchSize default
}

var extendedTableSpecs = []tableSpec{
    {TableHeaders, "c", 0},
    {TableBodies, "c", 0},
    {TableReceipts, "c", 8},   // NEW: smaller batch for big DeFi receipts
    {TableHashes, "r", 0},
    {TableDifficulty, "r", 0},
    {TableSenders, "c", 0},
    {TableAccountChanges, "c", 0},
    {TableStorageChanges, "c", 32},  // NEW: storcs avg 1-10 KB → 32 sweet spot
    {TableLeavesJournal, "c", 0},
    {TableBlockWitness, "c", 0},
}
```

Wire-format compatibility: cidxHeader.batchSize is uint8 (table.go:63),
already encodes the per-table batch. New files written with batch=8/32
are fully self-describing — no upgrade required, readers auto-detect.

### Phase H2.B — Migrate Bytecodes to SegmentStore + RecSplit (~400 LOC)

Layout:
```
chain/bytecodes.cidx          # 12B per segment (SegmentStore format)
chain/bytecodes.NNNN.cdat     # zstd-compressed code blobs + RecSplit
```

Per-segment data: codeHash → offset → bytecode (length-prefixed).
RecSplit MPHF: 32-byte codeHash → segment-local offset.

Segment cut: every N codeHashes (or when segment hits 500 MB).

Reuse:
- `internal/cscompact/segment_store.go` writer/reader (already done).
- `lib/recsplit/` MPHF builder/reader.

Read path:
1. `BufferedPlainStateReader.ReadAccountCode(addr, codeHash)` checks
   buf/inFlight/LRU as before.
2. Falls through to a new `BytecodeStore.Get(codeHash)`:
   - Iterate segments, MPHF lookup → if found, return the bytes
   - Order by recency (newer segments first) since hot code is recent

Write path:
- ApplyTo step 4 currently writes to MDBX modules.Code.
- New: writes go to a buffered "pending segment" → when 500 MB or
  10K codeHashes accumulate, finalize segment and start new one.
- Old MDBX rows (for already-finalized codeHashes) get deleted lazily
  via a `cmd/mdbx-evict-bytecodes` one-shot tool.

### Phase H2.C — One-shot migration tools

```
build/bin/mdbx-evict-receipts --datadir <path>
  // Reads MDBX modules.Receipts, freezes if not already in freezer,
  // then Deletes the MDBX row. Skips rows that are too recent (within
  // freeze threshold). Single-pass.

build/bin/mdbx-evict-bytecodes --datadir <path>
  // Walks MDBX modules.Code, writes each (codeHash, bytecode) into
  // bytecodes SegmentStore, then Deletes the MDBX row.
```

## Estimated savings

For d:/N42-ethverify (current state):
- Code: 2.6 GB → ~2 GB after zstd dict compression
- Receipts: 0 (not in MDBX for ethexec — see audit)
- HashedAccount/HashedStorage: NOT migrated (state root path needs
  cursor scan)

For cmd/n42 mainnet sync:
- Code: same ~2 GB savings
- Receipts: ~30-60 GB out of MDBX → mostly net-zero (already counted in
  freezer)

## Decision: Do H2 BEFORE or AFTER the 12498920 hunt

**AFTER**. Reason: H2 changes the write surface for ApplyTo (Code path)
and the freezer's batch-size handling. Mixing those changes with the
prefetch-race hunt would muddy the bisection. Keep separate.

Concrete order:
1. ✓ Settle 12498920 (prefetch race or otherwise)
2. ⏸ H2.A (per-table BatchSize) — ~1 day, low risk
3. ⏸ H2.B (Bytecodes SegmentStore) — ~3-5 days, more involved
4. ⏸ H2.C (eviction tools) — ~1 day each
5. ⏸ Verify on a 0-1M block fresh-genesis run before committing main path
