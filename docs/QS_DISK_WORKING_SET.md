# S70: per-node disk working set vs page-cache thrash

Context: S68/S69 and the standing-score line (`docs/QS_QUEUE.md`). At GOMEMLIMIT 11 GiB x 7
the fleet holds; at 12 GiB x 7 page cache falls 66->28 GB and MDBX refaults cost ~10%
throughput. Question: which per-node writes are bench-irrelevant and switchable by config,
so the MDBX working set and dirty-page volume shrink and page cache stops thrashing.

## 1. Observed cache/dirty behavior, r35zzzau (node1, kept logs)

From `r35zzzau-mem.log` / `r35zzzau-vm.log`, leg boundaries from `leg=` markers:

| Leg | Span | Cached start->end | Dirty end | pgmajfault (cum, in-leg) | refaultFile (cum, in-leg) |
|-----|------|--------------------|-----------|--------------------------|---------------------------|
| B1  | 13:38:54-13:52:13 (13.3 min) | 53,175M -> 46,617M (-6.6 GB) | 5M  | ~0 (flat after warmup settle) | low, settling |
| B2  | 13:52:21-14:05:58 (13.6 min) | 49,804M -> 26,430M (-23.4 GB, ~1.72 GB/min) | 328M | climbing to 247,190 | climbing to 664,767 |

B2 is the tighter-memory leg: cache evicts 3.5x faster than B1 and dirty pages end the leg
65x higher, matching the described refault spike. This is node-level vmstat, not a
per-table breakdown — MDBX does not expose per-table dirty-page counts in the write-probe
log, only aggregate `dirtyBytes`/`dirtyLimit` and per-table **key counts touched per
commit** (`tables="qmdbEntries=... Storage=... StorageChangeSet=... ..."` in the
`write probe` lines).

## 2. Per-write-path breakdown (from kept node1 log, `blockwrite phases` + `write probe` lines)

Per-block write-path timings (not bytes) from `msg":"blockwrite phases"`, typical block:
`qflush` (QMDB leaf flush, the state trie) 350-800k ns, `ce` (changeset encode) 370-480k ns,
`chgset`+`chgHist`+`chgTrunc` (account/storage changesets + the AccountHistory/
StorageHistory inverted index + truncation) 20-30k ns combined in this sample, `commit`
highly variable (100k-7M ns, fsync-bound), `post` ~100k ns, `state` 20-40k ns. `receipts`
is 0 in every sampled line (receipts writer not active on this leg/config).

The three largest *disk-working-set* writers, ranked by what the codebase itself documents
as costly (see `modules/state/history_index_mode.go` header comment, measured at 22,857
tx/block on this fleet):

1. **AccountHistory/StorageHistory inverted index** (`WriteHistory`) — **117.6 ms/block**,
   92.7% of the chgset phase, 20.3% of the whole write path. Roaring-bitmap read-modify-write
   over ~24k changed keys/block. This is the single largest write-amplifying, cache-churning
   path on the write side.
2. **QMDB twig/leaf state store** (`qmdbTwigLeaves`/`qmdbTwigs`/`qmdbEntries`) — the state
   trie itself; grows monotonically within a write-probe burst (e.g. 31,828 -> 36,436 leaf
   bytes across 10 probes in the sample). Load-bearing, cannot be turned off.
3. **AccountChangeSet/StorageChangeSet** (`WriteChangeSets`) — **8.7 ms/block**, load-bearing
   for PlainState rewind, eth-el's path, and DATC; much cheaper than #1 but not switchable
   without breaking rewind/DATC.

QMDBUndoWindow (reorg revert buffer) and BlockBody/Header/ExecutedResult/ConsensusEvidence
are small per-commit (hundreds of bytes to a few KB in the sampled `tables=` counts) —
not a top-3 writer at this tx rate.

txindex on node1 is only 322M on disk (N42_TXINDEX_TAIL=1 already keeps the hot tail small;
S60 already tried and falsified shrinking `N42_TXINDEX_KEEP_BLOCKS` further — no live-heap
or GC-pacing win). ancient-era (frozen historical segments, 7.1G) is write-once per
backfill, not a per-leg churn source.

## 3. Does the bench read it?

- Bench polls `eth_blockNumber` / `eth_getBlockByNumber(n,false)` — headers/bodies only.
- txflood looks up its newest funding receipt — needs receipts + the **tx-lookup tail**
  (`N42_TXINDEX_TAIL`), not the full AccountHistory/StorageHistory index.
- HotStuff import/voting needs headers, bodies, and the state root (QMDB twigs) to validate
  and commit; it does not consult AccountHistory/StorageHistory.
- **Nothing in the bench or in consensus reads AccountHistory/StorageHistory.** Per the code
  comment, "a throughput workload never reads it back" — api.State's historical-query path is
  the only consumer, and it is not exercised by run_leg.

Switch: `N42_NO_HISTORY_INDEX=1` (env, `modules/state/history_index_mode.go`). **Off by
default**, i.e. the index is ON unless this is set. `qs-env.sh` does not set it — the fleet
is currently paying the full 117.6 ms/block, cache-churning cost on every node, every block.
Side effect: historical-state queries (not used by bench/consensus) are refused while set,
per the sealed-horizon gate; changesets (load-bearing) are untouched, so DATC/rewind are
unaffected and the index can be rebuilt out-of-band later from changesets via
`cmd/n42-hist-from-freezer`.

Other switches checked, all already off by default and unset in `qs-env.sh` (no action
needed): `N42_WITNESS_DIR` (witness capture), `N42_CONTENTION_DIAG`, `N42_CODETRACE`/
`N42_EXECPROF`/trace family, exex AI indexer (not wired into the qs chain config).

## 4. MDBX map / page-cache side

Current node1 `chaindata/mdbx.dat` = 23G (post several legs' accumulation since the 16G
reseed point; not a single-leg delta). `N42_MDBX_MAPSIZE_GB` default 128 in `qs-env.sh` —
map size is not the constraint, resident/touched working set is. The AccountHistory/
StorageHistory tables are read-modify-write of roaring bitmaps keyed by account/storage
slot — effectively random-access over the *entire* historical key space each block, which is
exactly the access pattern that forces pages in and out of cache under memory pressure.
Disabling it removes a full-keyspace random-RMW table from the touched set every block,
which should cut both the per-leg Dirty MB and the amount of Cached GB this node competes
for, without shrinking the chain-essential (header/body/receipt/QMDB) working set.

## 5. Ranked rounds (config-only)

**#1 (best): `N42_NO_HISTORY_INDEX=1` on all 7 nodes.**
- Expected: chgset-phase write time drops ~92.7% (117.6 ms -> single-digit ms/block out of
  the ~20.3%-of-total chain), cutting Dirty MB/min materially and removing a full-keyspace
  RMW table from the Cached working set — directly targets the page-cache-thrash mechanism.
- Risk: none to the bench or consensus (neither reads this index); historical-state RPC
  queries refused while set (not used here). Safety: changesets, rewind, and DATC are
  untouched.
- Prediction to register: at GOMEMLIMIT 12 GiB x 7 with `N42_NO_HISTORY_INDEX=1`, B-leg
  pgmajfault/refaultFile deltas fall toward the B1 (11 GiB) band or lower, and B-leg
  throughput recovers to within the 3.6% noise floor of the 11 GiB baseline (currently ~10%
  down). `run_leg` setting: add `N42_NO_HISTORY_INDEX=1` to the existing `qs-env.sh` export
  block (same idiom as `N42_TXINDEX_TAIL`), no other changes; keep GOMEMLIMIT at 12 GiB to
  isolate the effect.

**#2: Reduce `N42_MDBX_MAPSIZE_GB` toward the actual working set (e.g. 48-64 instead of
128).** Expected: smaller mmap region can reduce address-space-driven readahead/touch
footprint; effect on resident Cached GB likely small since MDBX only touches pages it reads,
not the whole map. Risk: none to bench; risk of map-full abort if misjudged under growth —
needs headroom above current 23G and future leg growth. Lower expected benefit than #1 and
some operational risk, so ranked second.

**#3: Shrink the QMDB undo/revert window (if a size knob exists) or `N42_TXINDEX_KEEP_BLOCKS`
further.** Already falsified for `N42_TXINDEX_KEEP_BLOCKS` at S60 (no live-heap/GC win); undo
window is small per the sampled `tables=` byte counts, so expected cut is marginal. Risk:
undo window directly protects reorg safety — any cut here needs a correctness read-through on
HotStuff revert first. Ranked last; only worth pursuing if #1 undershoots its prediction.

## Scratch

Working notes under `/data/blockchain/gov5-work/scratch/s70/` deleted at task end.
