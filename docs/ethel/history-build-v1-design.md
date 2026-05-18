# History Build v1 — Design

**Goal:** transpose existing `acctcs.cdat` / `storcs.cdat` (block-major
changesets) into Erigon-E3-style history files (key-major: per-key
list of "blocks where it changed" + values at those blocks). After
build, the original CS files can be truncated to a small warm tier.

## Why not reuse `lib/state` HistoryV3 as-is

`lib/state/history_build.go` builds RecSplit indexes (`.efi`, `.vi`)
on top of pre-existing `.ef` / `.v` files. The upstream pipeline
expects `addPrevValue(key1, key2, prev)` to be fed via `historyWAL`
which writes into MDBX tables, then `domain.go` collation reads MDBX
and produces sorted `.ef`/`.v`.

For our case, the data is already sequentially sorted by block in
`storcs.cdat`. Going via MDBX would mean materialising ~400 GB of
changesets into MDBX first, only to dump it back out. Not worth it.

We write a direct streaming transpose tool, but **reuse the
file format and the RecSplit/EliasFano builders** so the output
is byte-compatible with `lib/state.HistoryRoTx` readers.

## File layout per domain

```
<out>/storage.kv      sorted [(key, packed-history)] pages (zstd)
<out>/storage.idx     sparse offset index → page byte offset
```

Where `packed-history` for a single key encodes its full timeline:

```
[varint numChanges]
[varint deltaBlock_0][scheme-C value_0]
[varint deltaBlock_1][scheme-C value_1]   // delta = block - prev_block
...
```

Values are the OLD value at the start of that block (i.e. the value
returned by `eth_getStorageAt(addr, slot, "block - 1")`).

**Trade-off vs Erigon E3:** we collapse `.ef` + `.v` into a single
per-key blob, which is simpler to read (one binary search + one page
decompress) but harder to range-scan across keys for a fixed block.
For "historical state at block N" lookups (the dominant query) this
is the right shape.

## Size estimate (storage, 25M blocks)

Assume 300M unique slots × 4 writes avg = 1.2B (key, block, value)
entries.

| Component | Per entry | Per 1.2B |
|-----------|-----------|----------|
| numChanges varint (per key, amortised) | 1 B / 4 | 0.3 B |
| deltaBlock varint (avg block spacing ~6M, ~3-4 B) | ~3.5 B | 4.2 GB |
| value scheme-C | ~10 B | 12 GB |
| **Subtotal raw** | ~13 B | ~16 GB |
| **After page-zstd-64** | | ~8 GB |
| Per-key sparse idx (52 B firstKey + 8 B offset / 64 entries) | | 280 MB |

**Total storage history ≈ 8 GB.**

Account history (300M addresses × 30 writes avg = 9B entries, but
account value much more compressible due to repeating codeHash via
dict): **≈ 4 GB**.

**Grand total history ≈ 12 GB**, below the 17 GB plan.

## Build algorithm

```
for each domain in [account, storage]:
    1. Open CS freezer table read-only.
    2. ETL.NewCollector(tmpdir, large buffer).
    3. for blk in [0, head):
           data := tbl.Retrieve(blk)
           for c in Decode(data):
               key := c.CompositeKey  // 20 for addr / 52 for addr+slot
               // OldValue = value BEFORE this block applied
               // Emit (key, blockBE, oldValue) → ETL
               collector.Collect(key, packEntry(blk, c.OldValue))
    4. Coldstore writer (sorted): coldstore.NewWriter(out, domain, keyLen, 64).
    5. collector.Load(func(key, packedEntry, nextLoad)):
           // ETL gives us entries sorted by key, then by (because we
           // packed blockBE inside collector value) implicitly by block.
           // Group by key, build per-key history packed bytes.
           if key != prevKey:
               if prevKey != nil { coldstore.Append(prevKey, packPrevHistory()) }
               prevKey = key; reset accumulator
           accumulator.Append(blockNum, value)
        coldstore.Append(prevKey, packPrevHistory())
    6. Close everything.
```

ETL handles the spilling-to-disk + merge sort, exactly the pattern
already validated by `reth-snapshot-export` Phase 3.

Memory ceiling: one ETL buffer (e.g., 1 GB) + one in-progress per-key
accumulator (bounded by max writes for a single hottest slot — wETH
total_supply slot might have millions; safety net = chunk per-key if
> 1M entries).

## Lookup API

```go
type Reader interface {
    // Get the storage value (or account) at exactly block N.
    // Returns the value as-of the END of block N-1.
    GetAsOf(key []byte, block uint64) (value []byte, found bool, err error)
}
```

Implementation:
1. Binary search coldstore for `key` → packed history blob.
2. Decode varint deltas, find largest `block_i ≤ block`.
3. Return `value_i` (= old value entering `block_i`, which is = value
   after `block_i - 1`).
4. If `block_i == block` exactly, return value just before `block`.
   If no `block_i ≤ block`, value didn't change before this block →
   fall back to **snapshot at block 0** (or per-step snapshot if we
   add stepping later).

## Out of scope for v1

- Per-step file partitioning (E3 has `<step>.history.{v,ef}` —
  we keep one big file). Simpler; defer until file size > 16 GB.
- RecSplit MPHF (we use sparse binary search like coldstore v1).
- `getAsOf` for ranges (full historical state replay) — only point lookups.
- Background merging of step files (we build once, immutable).

## Validation

```
1. Build for first 100K blocks.
2. For 1000 random (key, block) pairs in that range:
     a. Replay storcs from block 0 to block-1 → ground truth.
     b. GetAsOf(key, block).
     c. Assert equal.
```

## Tool entry point

```
cmd/n42-history-build/main.go
  --freezer D:/N42-eth1177/chain/freezer
  --out     D:/n42-history
  --domain  account|storage|both
  --start   0
  --end     0  (0 = head)
  --tmpdir  C:/tmp/etl
```

## Implementation size

- Tool: ~250 lines (mostly flag parsing + ETL plumbing)
- New shared code in `internal/coldstore/`: 0 (reuses writer + reader)
- New shared code in `internal/history/`: ~120 lines for per-key
  pack/unpack codec
- Verification tool `cmd/n42-history-verify/main.go`: ~100 lines

**Total ≈ 470 lines new code.** Doable in one focused session
once snapshot export validates.
