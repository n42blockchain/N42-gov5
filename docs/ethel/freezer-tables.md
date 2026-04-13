# EthEL Freezer Table Inventory

This document is the authoritative catalog of every persistent table the
`internal/ethel` replay pipeline reads or writes, what each table contains,
who writes it, who consumes it, and which capability depends on it. It
exists so that any reviewer can answer "is offline replay possible from
this datadir alone?" without grepping the executor.

## Layout on disk

A complete EthEL output datadir looks like:

```
<datadir>/
  mdbx.dat                       # MDBX state DB (PlainState, HashedAccount, etc.)
  mdbx.lck
  chain/
    senders.cidx                 # SegmentStore senders (when present)
    freezer/
      receipts.{cidx,NNNN.cdat}  # block receipts (Compact codec)
      acctcs.{cidx,NNNN.cdat}    # account changesets (Erigon V2 codec)
      storcs.{cidx,NNNN.cdat}    # storage changesets (Erigon V2 codec)
      leaves.{cidx,NNNN.cdat}    # per-block leaf changes (plain keys)
      witness.{cidx,NNNN.cdat}   # state access set per block
      senders.{cidx,NNNN.cdat}   # pre-computed senders (alt format)
    headers.{cidx,NNNN.cdat}     # columnar headers (header_compact.go)
    headers.bin / headers.idx    # alt segmenting layout (per dev_log)
    bodies.NNNN.dat / bodies.idx # columnar bodies (body_compact.go)
```

The Geth ancient (input) is **external** to this layout — it lives at
`--ancient <path>` and is read-only.

## Output tables (per-block, written by `executor.writeOutputs`)

Each row lists: table name, purpose, writer, format, consumer.

| Table | Purpose | Writer | Codec | Consumer |
|-------|---------|--------|-------|----------|
| `receipts` | Per-block receipt list. Skipped when `--leaves-only`. | `executor.writeOutputs` (`internal/ethel/executor.go:644-649`) | Reth-style Compact (1B flags + tight fields, see `receipt_codec.go`) | RPC `eth_getTransactionReceipt`, log indexer, ExEx |
| `acctcs` | Per-block account changeset (old → new). | `writeOutputs` (`:698-700`) via `BufferedPlainStateWriter.ChangeSetWriter` | Erigon V2 changeset encoding (`changeset_codec.go`) | `journal_verify.applyChangeset` (revert), historical state lookup |
| `storcs` | Per-block storage changeset. | `writeOutputs` (`:701-703`) | Erigon V2 changeset encoding | Same as `acctcs` |
| `leaves` | Per-block leaf-level state changes (account + storage). For block 0 contains the **entire** genesis state so the journal alone can rebuild PlainState. | `writeOutputs` (`:704-706`) — `EncodeLeavesJournal` for blocks ≥1, `EncodeGenesisJournal` for block 0 | Plain-key leaves journal (addr+slot, no incarnation, no Keccak hashing — see `leaves_journal.go`) | `journal_verify.applyJournalEntry` (forward replay), `JournalVerifier.Run` |
| `witness` | Set of state slots touched by EVM during block execution. Empty bytes if witness recording disabled. | `writeOutputs` (`:708-715`) via `WitnessStateReader.Encode` | `witness.go` custom encoding | `witness_verify.go` for trace replay |

All five output tables are committed in lockstep: `executor.go:221`
`batchAligned := (blockNum+1)%BatchSize == 0` ensures the commit boundary
falls on a multiple of `freezer.BatchSize = 64` so that `alignOnResume`
(`output_batcher.go:427`) can recover partial batches deterministically.
At the commit boundary, `outputBatcher.Sync()` (`output_batcher.go:152-`)
calls `b.freezer.Sync()` to fsync every table's bufio + os file BEFORE
the MDBX `tx.Commit()` runs (see `docs/consensus/hotstuff2-spec.md`'s
durability ordering — wait, wrong doc; see
`memory/project_freezer_mdbx_atomicity.md`).

## Output tables (separate stage)

| Table | Purpose | Writer | Codec | Consumer |
|-------|---------|--------|-------|----------|
| `senders` | Pre-recovered sender addresses (concatenated 20B). One stage upstream of execution to avoid `ecrecover` in the hot loop. | `cmd/ethexec sender-recovery` subcommand (`internal/ethel/sender_stage.go`); also a SegmentStore variant at `chain/senders.cidx` | Concatenated raw 20B addresses, batch-zstd | `executor.run` loads via `SetSenderFreezer` / `SetSenderStore`; ProcessBlock calls `SetFrom` to skip `ecrecover` |

`senders` is **optional** — `executor.go:485-487` falls back to live
`ecrecover` per tx. The dev log claims ~45ms/block savings on dense blocks.

## Input mirror (columnar, separate from freezer)

These are NOT freezer tables. They are columnar compressed stores
written by the `header-compact` and `body-compact` subcommands as
optional offline replay inputs that **eliminate the dependency on a
Geth ancient**.

| Store | Purpose | Writer | Codec | Status |
|-------|---------|--------|-------|--------|
| `headers.{cidx,NNNN.cdat}` (or `headers.bin/.idx`) | Per-block header. Stripped of derivable fields (parentHash, bloom, number, difficulty/nonce/uncleHash post-PoS). | `internal/ethel/header_compact.go` | Adaptive dictionary + delta-varint columns + zstd integer compression (per dev log) | ✅ Roundtrip tested |
| `bodies.NNNN.dat` + `bodies.idx` | Per-block body (txs, uncles, withdrawals). Columnar TX encoding, top-100 To-address dictionary, varint nonce/gas, trimmed uint256 values. | `internal/ethel/body_compact.go` | Per-column zstd, custom V/R/S layout | ⚠️ **Disabled in main `run` path** — see *Known gaps* below |

When loaded, these are wired in via `executor.SetCompactReaders(hr, br)`
(`executor.go:115-117`). When not loaded, the executor falls back to the
Geth ancient via `executor.readHeader` / `executor.readBody`
(`executor.go:455-475`).

## Input data (external)

| Source | Path | Format | Notes |
|--------|------|--------|-------|
| Geth ancient | `--ancient <path>` | Standard Geth freezer (`headers`, `bodies`, `receipts`, `hashes`, `diffs`) | Read-only. Required for `cmd/ethexec run` unless compact readers are enabled. |

## Known gaps

These are real but explicitly **out of scope for stability work**. They
are documented here so future contributors don't re-discover them.

### G1 — Compact body reader disabled in `run`

`cmd/ethexec/main.go:515-538` wraps the `executor.SetCompactReaders`
wiring in `if false { ... }` with the comment:

> TODO: compact body reader has signature decoding issues (V/R/S columns).
> Disabled until fixed. Using Geth freezer for now.

The unit test (`internal/ethel/body_compact_test.go`) currently passes,
which suggests the issue is either (a) already fixed but the comment is
stale, or (b) only triggered by mainnet edge cases not in the unit test
corpus (e.g. very old pre-EIP-155 txs with V=27/28 mixed with EIP-155 V).

**Action when re-enabling:**
1. Pick the earliest 100K mainnet blocks from a Geth ancient.
2. Encode them via `cmd/ethexec body-compact`.
3. Run `cmd/ethexec run --datadir <test>` with compact readers enabled and verify state root matches an independent run that used the Geth ancient.
4. If pre-EIP-155 V handling is the bug, the suspect lines are
   `body_compact.go:295-311` (encode parity) and `:1218-1243` (decode parity).

**Owner:** unassigned. Marked `// TODO(p1-3)` in the disabled block.

### G2 — Trie node delta history

There is no per-block table of trie node insertions/deletions. Today's
state proofs are produced by re-executing from the nearest checkpoint
plus changesets. A trie-history table would let `eth_getProof` answer
arbitrary historical queries in O(log n) without re-execution.

**Why it's not P1:** journal_verify already reconstructs full state from
`leaves` + `acctcs/storcs`, so all stability-relevant correctness goals
are met. Trie history is a perf/feature item.

**Estimated cost:** non-trivial. Requires defining a stable trie-node
delta encoding, deciding whether to store dirty paths or full witness
sets, and writing a reader that can stitch deltas across batches.

**Owner:** unassigned. Tracked here only — no code marker yet.

### G3 — Blob sidecar (EIP-4844)

Per-tx blob hashes (KZG commitments) are captured in the body
(`body_compact.go` BlobHashes column). The actual blob data and KZG
proofs are NOT mirrored; they live in `internal/peerdas/` and are only
needed for trustless DA verification, not for execution replay (BLOBHASH
opcode and the point-evaluation precompile both work from the hash
alone).

**Why it's not in scope here:** orthogonal subsystem. PeerDAS owns blob storage.

## Capability matrix

What can you do with just the EthEL output datadir (no Geth ancient)?

| Capability | Required tables | Status |
|------------|----------------|--------|
| Rebuild PlainState from genesis | `leaves` (+ block 0 has full genesis) | ✅ Works (verified by `cmd/ethexec verify-journal`) |
| Verify state root at any block | `leaves` + MDBX HashedAccount | ✅ Works (`JournalVerifier.Run`) |
| Revert state by N blocks | `acctcs` + `storcs` + `leaves` + `PlainContractCode` | ✅ Works (`JournalVerifier.revertTest`) |
| `eth_getTransactionReceipt` historical | `receipts` | ✅ Works |
| `eth_getProof` historical | `leaves` + state re-execution | ⚠️ Requires re-execution (no trie history) |
| Re-execute block N offline | `headers` + `bodies` + `senders` + `acctcs/storcs` (for state at N-1) | ⚠️ Requires compact readers re-enabled (G1) OR Geth ancient |
| Trace replay (per-tx) | Same as re-execute + `witness` for sanity check | ⚠️ Same as above |
| Trustless DA verification | Blob sidecars | ❌ Out of scope (G3, see PeerDAS) |

## Where to add a new freezer table

If you need to add another EthEL output table:

1. Add the constant to `modules/rawdb/freezer/freezer.go` (`Table*` block at `:39-51`).
2. Add it to `extendedTableSpecs` if it should be auto-opened by `freezer.New()` (`:110-123`). For tables only used by the EthEL output side, prefer leaving it out and letting `EnsureTableCompressed` open it on demand.
3. Add the per-block write in `internal/ethel/executor.go:writeOutputs` BEFORE the `flushFullBatches` call.
4. Add the corresponding read path in your consumer.
5. Add the table name to `outputBatcher.alignOnResume`'s `tables` slice (`output_batcher.go:430-444`) so `--end` resume aligns it with the others. If you forget this, the table will be skipped by alignOnResume and will eventually drift from the others, causing the `freezer lags MDBX` hard error in `output_batcher.go:alignOnResume`.
6. Update this catalog.

## Cross-references

- `docs/consensus/hotstuff2-spec.md` — consensus side rules (unrelated)
- `memory/project_ethel_dev_log.md` — full historical dev log of all freezer + codec work
- `memory/project_freezer_mdbx_atomicity.md` — durability ordering rules
- `memory/feedback_freezer_batch_pitfalls.md` — batch-mode gotchas
