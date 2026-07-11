# Data-pipeline lessons (distilled, 2026-06)

Durable, reusable engineering lessons from the 2026-06-14..16 weekly data-sync
(bringing geth/reth + all n42-eth-el derived data to a common height) and the
verify-root optimization. The blow-by-blow lives in `sync-runbook-2026-06-14.md`
§8; this file is the distilled "what to remember" for future maintainers.

---

## 1. Long MDBX copy / build tools — three iron rules

When writing or running a tool that copies/builds a large MDBX dataset:

1. **Sorted source + fresh dst → `Append`/`AppendDup`, never `Put`.**
   `Put` does a B-tree search + possible page split per row (random-insert);
   `Append` assumes ascending keys, fills pages sequentially, no search. For
   non-dup and **AutoDupSort** tables use plain `RwCursor.Append` (the cursor
   splits AutoDupSort keys internally — same path as `etl.Collector.Load`);
   only genuine DupSort (e.g. `TrieOfStorage`, `Flags: DupSort` without
   `AutoDupSortKeysConversion`) needs `AppendDup`. Across periodic commits,
   reopen the cursor; Append stays valid because the source is sorted (next key
   > committed table's max). Honest caveat: Append is ~1.4-2× here, not the
   textbook 3-10×, because a semantic copy's **read + value transform** (e.g.
   reth Compact → N42 MarshalV2) is a big part of per-row cost, not just the
   write. Reference: `../erigon` table-copy code; `n42-migrate-reth-hashed`.

2. **Support resume.** Read the dst table's last key, seek the (sorted) source
   past it, continue Append. Multi-phase tools: a fully-written phase auto-skips
   (source exhausted past last key), the in-progress phase resumes mid-stream.
   `newAppendWriter` (n42-migrate-reth-hashed/phases.go) is the pattern.

3. **Stop with graceful `Ctrl+C` (SIGINT), never `kill -9`.** A SIGINT handler
   commits the current tx and exits, preserving progress to the last batch;
   `kill -9` drops the in-flight uncommitted batch (back to the last commit
   checkpoint). Wire `signal.NotifyContext` → per-phase commit-on-cancel →
   `exit 0` so a re-run resumes. For background processes you started, send
   SIGINT and give it flush time. (MEMORY `feedback_bulk_copy_append_resume_graceful`.)
   **Windows caveat:** a `nohup &`-detached process has no console, so MSYS
   `kill -INT` does NOT deliver SIGINT (it keeps running); and a tool WITHOUT a
   `signal.NotifyContext` handler (e.g. `n42-stateless-blockproof-produce`) can't
   flush-on-signal anyway. For those, the "friendly" guarantee is purely **atomic
   per-checkpoint commit + MDBX ACID + `-resume`**: `Stop-Process -Force` rolls back
   only the uncommitted in-flight window, and a re-run resumes from the last
   committed checkpoint. So: ADD a SIGINT handler when you write the tool; when
   stopping someone else's handler-less tool on Windows, force-stop is safe IFF it
   commits atomically per checkpoint and supports resume.

---

## 2. ETL temp ALWAYS goes to D: (`N42_ETL_TMPDIR`)

Before any ETL/external-sort op (`verify-root`, `verify-cs-root`, `history-build`,
`cs-compact`, `txlookup-build`, `rebuild-state`, anything logging
`RebuildHashedStateETL` / `etl.NewCollector`):

```
export N42_ETL_TMPDIR='D:/etl-tmp'
```

ETL spill defaults to `os.TempDir()` = `C:\…\Temp`; **C: has only ~50-65 GB
free**, but full-chain external sort needs **~130 GB** → mid-run "not enough
space" crash after 30-50 min wasted. **verify-root does NOT OOM** (ETL is
memory-bounded) — the real trap is temp disk. Always check **C: free + temp
location**, not just RAM. (MEMORY `feedback_etl_tmpdir_to_d`.)

---

## 3. Columnar freezer (headerc/bodyc) resume — segment-count trap

The columnar header/body freezers (8192-block segments, header_compact 8-byte
cidx) resume by **`SegmentCount × 8192`**. If the last segment is **partial**
(e.g. 4408/8192), the segment count **over-estimates** actual coverage, and a
naive extension appends the tail at the wrong block → a **non-contiguous hole**.

- `freezer-info`'s "N segments × 8192 = MaxBlock" is a **CAPACITY** figure, NOT
  actual coverage. To know the real last block, **READ across the suspected
  region**, don't trust the segment count.
- **Symptom:** a continuous-read consumer (bpp / blockproof-produce) crashes
  `read header N: index X out of range (segment has Y)`. bpp is the de-facto
  continuity validator.
- **Fix (cidx-only, safe):** back up the cidx; verify the contiguous prefix
  (probe blocks, hashes must match geth); `truncate -s <N_full_segs × 8>
  headerc.cidx bodyc.cidx` to drop the partial segment + everything after; the
  freezer's `SegmentCount` falls to N → header-/body-compact **resume from the
  correct block** and rebuild contiguously to tip. Old `.cdat` tail becomes
  unreferenced garbage (harmless — the cidx is the source of truth). Do NOT touch
  `.cdat`. Verify the gap blocks now read + match geth.
- Batch-mode freezers (receipts/senders/acctcs/storcs/witness, NCIX + 6-byte
  entries) track items continuously and were unaffected — only the columnar
  pair had the hole.

---

## 4. verify-root (full state-root recompute) optimization

Sequential cost at 25M-block scale: **3h36m** = collectAcct 9m + collectSto
1h22m + loadAcct 10m + **loadSto 1h5m** + CalcTrieRoot ~40m.

Shipped (byte-exact root, real-data validated → **40m, ~5.4×**):
- **Memoize keccak(addr)** in storage hashing (consecutive same-addr rows).
- **Parallel sharded hash** — per-worker RoTx over addr-byte shards + `etl.LoadMerged`
  (heap-merge N collectors' sorted runs).
- **Streaming fusion** (`StreamingFullStateRoot` + `etl.StreamMerged` +
  `trie.CalcTrieRootStreaming`) — stream sorted hashed leaves DIRECTLY into the
  trie builder, **eliminating the ~75 min HashedAccounts/HashedStorage MDBX
  materialization** (it's transient for verify).
- **Work-stealing shards** (`processByteShards`: 256 single-byte shards over a
  queue) — bounds the mega-contract straggler vs fixed equal ranges.

Did NOT ship — **C2 parallel-subtrie** (`StreamingFullStateRootC2`,
cutoff-1 subtries + `CombineNibbleSubtries`): MPT mechanism is correct
(byte-exact on unit fixtures) but at real 2B-leaf scale the demux pipeline
serialized + ballooned RAM to ~104 GB (two global-sorted demux goroutines
feeding 16 lockstep consumers, no GOMEMLIMIT) → killed + reverted; needs
per-nibble PARTITIONED collectors. **Lesson: small unit tests prove MPT
correctness but NOT at-scale streaming-pipeline deadlock/memory — a real-data
run is what exposes it.** (MEMORY `project_verifyroot_parallel_opt`.)

DATC parallel-trie evaluated as **not worth it** (~1.5-2×): DATC computes a small
INCREMENTAL root per ~1000-block window where 16-way nibble parallelism gives only
~3-5×, Amdahl-capped (~58% root + ~42% changeset/flush ≤ 2.4×), and window mode
already amortizes. Nibble-parallelism's sweet spot is the FULL dense from-scratch
rebuild (verify-root), not incremental roots.

---

## 5. Architecture facts worth keeping

- **No published distribution format depends on N42-hashed** (the hashed-canonical
  state). minimal/full **snapshots export from PlainState** (`reth-snapshot-export`
  reads PlainAccountState/Storage), archive = PlainState + changesets + witness,
  stateless = anchors. N42-hashed is ONLY the **eth-el live follower node's** state.
  So weekly published data derives entirely from N42-eth1177 PlainState (+ codes/
  changesets/witness/anchors), which is independent of N42-hashed.
- **N42-hashed is derived, not network-fetched.** It's `reth-2.2 hashed-canonical`
  (HashedAccounts/HashedStorage/TrieOf*, no PlainState). Build it by **verbatim
  copy from reth** (`n42-migrate-reth-hashed`, byte-compatible trie; vtrie verifies
  the root = GATE) — NOT by eldevp2p network catchup of blocks you already have
  locally. Confirm reth head first (`n42-reth-head`: BlockBodyIndices lastKey).
  Incremental-via-changesets (`MerkleStageIncremental` + `BuildRetainListFromChangesets`)
  is the alternative when reth is unavailable.
- **codes are FULL-history content-addressed — never window by recency.** The
  hottest codes (USDT/USDC/Uniswap) are the OLDEST; a "recent codes" cut breaks
  execution of most txs. The weekly delta is just cold new deployments. Dedup by
  codehash keeps full history at ~16 GB MDBX / ~5.7 GB freezer. The EVM replay
  already writes new codes to the Code table; re-export the published freezer with
  `code-import2fz --coverage-block <tip>`. Height marker = `codes.coverage`
  (8-byte BE uint64).

---

## 6. Weekly maintenance — heights are point-in-time

Published data refreshes **weekly**; every block number in the runbook is a
one-week snapshot. The SOURCE OF TRUTH for "where is each dataset" is its on-disk
height marker (`freezer-info` Items / `ethel-last-block` / `codes.coverage` /
manifest `block_range.end` / anchorc end / geth `Frozen()`). Weekly maintenance =
re-run the same generators (they auto-resume to the new geth tip); refresh geth
ancient from a live geth+CL first, then fan out. See runbook §5b/§5e.

---

## 7. Operational pitfalls (small but bit us)

- **`tool | grep | head` masks the tool's exit code** (pipeline status = head's
  0). A failing tool looked like success. For RW/data ops, capture raw output to
  a file; check the tool's own exit, not the pipeline's.
- **Cross-check derived data against geth at the same block** before trusting it:
  `geth-hdr-probe` (geth ancient header.Root/hash) vs the derived freezer at the
  same block must be **byte-identical**. This GATE caught the headerc/bodyc gap
  and confirmed every other freezer.
- **Don't trust a stale process-activity read as "live."** A 14h-old mtime read
  at a glance as "recent" led to a wrong "reth is syncing, don't touch" call.
  Check `Get-Process` / actual head, not just file timestamps.
- **Long-running builders' MDBX `MapSize` must fit the RESUMED db + its growth.**
  `n42-stateless-blockproof-produce` defaults `-mapsize-gb 64`, but its bpp-trie
  is ~270 GB on resume → `MDBX_MAP_FULL: storage size limit reached` mid-run (the
  trie keeps growing as it processes blocks). MapSize is a Windows MEM_RESERVE
  (no commit charge until used), so set it generously (`-mapsize-gb 1024`); actual
  growth is bounded by free disk. It commits progress atomically per anchor, so
  `-resume` continues from the last committed block after the bump — lost work is
  only the in-flight window. Same family as the `OptMaxDB 200→256` fix
  (`lib/kv/mdbx/kv_mdbx_opts.go`): cfg/limits that fit an old small db silently
  break on a grown one.
