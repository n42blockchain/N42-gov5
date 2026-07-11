# Sync Runbook — geth + reth → common height, regenerate eth-el data (2026-06-14)

> Goal: **D:\geth** and **D:\reth2k** were already aligned **2026-06-13**;
> use the **current data height** = geth ancient frozen **25,311,095**
> (probed via `cmd/block-stats` → `freezer.NewReadOnly`, geth cidx sentinel
> stripped; db head a few ×10k higher). Regenerate / extend all
> **n42-eth-el** derived data to that height for the **4 distribution modes**
> (minimal / full / archive / stateless-IDC).

---

> **Distilled, reusable lessons from this run** → `docs/ethel/data-pipeline-lessons.md`
> (Append/resume/graceful-stop rules, ETL-temp, columnar segment-count trap,
> verify-root optimization, architecture facts, weekly maintenance, op pitfalls).

## 0. Decisions locked (2026-06-14)

| Question | Answer |
|---|---|
| 4th mode | **stateless / IDC client** (no local PlainState; pulls code/witness/proof from IDC, keccak-verifies, caches) |
| Common target height | **current aligned height = 25,311,095** (geth ancient frozen, 2026-06-13; NOT the stale 05-28 log value 25,198,422) |
| Phase A | geth+reth **already aligned 06-13** — verify, don't re-sync |

---

## 1. Upstream source inventory (the two clients to align)

| Source | Path | Role for eth-el | Current height | Status |
|---|---|---|---|---|
| **geth** | `D:\geth` (`geth\chaindata\ancient\chain`, 1.2 TB ancient) | canonical headers / bodies / receipts source | **ancient frozen 25,311,095** (last write 06-13 18:04; db head ≈ frozen + ≤90k); stale stderr log shows 05-28/25,198,422 but data moved on | **STOPPED but current** — ran 06-13, aligned. Restart only if pushing head further |
| **reth2k** | `D:\reth2k` (`db\mdbx.dat` 2.3 TB, `static_files` to 9.99M) | reth state / static_files source (snapshot + witness pre-state) | target `--debug.tip 0xfd31698e…` = block **25,311,094** (= geth tip) | **STOPPED** since 06-14 02:26 (mdbx.dat last write; `Get-Process reth*` = 0). No longer running — earlier "actively syncing" read was a 14h-stale mtime mistaken for live |

**Correction (2026-06-14 ~16:30):** reth2k is **NOT** running — it stopped overnight
at 02:26. Only the in-flight `ethexec` replay is hot. So: no reth contention (the
replay's ~25 blk/s is async witness-buffer flush + disk, not CPU competition), and
`code-import` (reads reth MDBX read-only) is **unblocked**. reth exact head still to
be confirmed via `reth db stats` (safe now it's idle).

---

## 2. n42-eth-el derived-data inventory (what gets regenerated)

**Real ends probed 2026-06-14** (`freezer-info`, `headerc-bodyc-probe`,
cidx sizes) — supersede the stale 05-19 manifest (25,101,867):

| Dataset | Path | Size | Freezer tables / content | **Real current end** | Feeds mode |
|---|---|---|---|---|---|
| **n42-eth1** (distribution freezer) | `D:\n42-eth1\chain\freezer` | 843 GB | `headerc, bodyc, receipts, accthist, storhist, txindex, senders, codes` | headerc/bodyc **25,255,936** (seg cap); receipts **25,252,185**; **senders 25,101,867 ← LAGS**; accthist/storhist/txindex tiny (MPHF, being retired) | minimal / full |
| **N42-eth1177** (archive-extra freezer) | `D:\N42-eth1177\chain\freezer` (+ MDBX) | 899 GB | `acctcs, storcs, witness, senders, codes` | acctcs / storcs / witness / senders **all 25,252,185** | archive |
| **N42-hashed** (canonical hashed state) | `D:\N42-hashed\chaindata` (MDBX) | 219 GB | hashed accounts/storage + trie (live eth-el state) | **25,191,536** — root `0x85bfede5…` ✅ matches geth @25191536 | live eth-el / snapshot src |
| **n42-idc-anchors-25m** (stateless anchors) | `D:\n42-idc-anchors-25m` | 4.7 GB | `anchorc` columnar MPT block-proofs | ≈ **24,998,000** (bpp-bound) | stateless-IDC |
| **n42-codes-25252** (standalone code freezer) | `D:\n42-codes-25252` | 5.7 GB | `codes` content-addressed | ≈ **25,252,xxx** | stateless-IDC / full / archive code-fetch |
| **blockhash-archive** | `D:\blockhash-archive` | 102 MB | `blockhash → number` self-verifying | TBD | RPC `eth_getBlockByHash` |
| **n42-bpp-trie-25m** | `D:\n42-bpp-trie-25m` | 253 GB | bpp block-proof trie (anchor producer) | 24,998,000 | anchor producer (intermediate) |

**Gap to close (→ geth frozen 25,311,095):**

| Dataset | From | To | Δ blocks |
|---|---|---|---|
| headerc / bodyc | ~25,255,936 | 25,311,095 | ~55k |
| receipts / acctcs / storcs / witness / codes | 25,252,185 | 25,311,095 | ~59k |
| **senders (n42-eth1)** | **25,101,867** | 25,311,095 | **~209k (worst lag)** |
| N42-hashed state | 25,191,536 | 25,311,095 | ~120k |
| anchors / bpp-trie | ~24,998,000 | 25,311,095 | ~313k |

> Note: the MEMORY "headerc stale 24,998,xxx" is **outdated** — headerc/bodyc
> were refreshed to ~25.255M since. The real laggards are **senders (n42-eth1)**,
> **anchors/bpp**, and **N42-hashed**.

---

## 3. Per-mode data composition (4 modes)

> **Sizes corrected to `docs/ethel/storage-matrix-2026-06.md` (authoritative).**
> The old architecture-doc figures (minimal 39 / full 682 / archive 849 GB)
> are superseded — they mixed download vs on-disk and pre-date the
> hot-segment / F2 / EIP-4444 accounting.

| Mode | Download | On-disk | Basis (storage-matrix §1) |
|---|---|---|---|
| **minimal** | **~30 GB** | ~30 GB | snapshot zst 24 + headerc 4.5 (+ code cache 1-3) |
| **full** | **~126 GB** | **~285 GB** | minimal + hot-segment bodyc/receipts/txindex/accthist + code; cold via torrent/IDC |
| **archive** | (full) + cold | **~1.0–1.2 TB** (current ~2.5 TB pre-squeeze) | full + acctcs/storcs 405 + witness 171.6 + DATC lean 80-150 |
| **stateless / IDC** | **~5-8 GB** | ~5-8 GB | anchors 4.6 + code cache 1-3; state/witness/proof pulled from IDC on demand, keccak/root verified |

| | **minimal** | **full** | **archive** (default) | **stateless / IDC** |
|---|---|---|---|---|
| **bootstrap** | snapshot-direct (no rebuild) | snapshot-direct (no rebuild) | leaves-journal → RebuildState | none (online, no local state) |
| **state** | snapshot accounts+storage (RecSplit/EF/zstd ~28 GB) | + history index | + full PlainState (genesis..tip) + witness | **none locally** |
| **headers** | `headerc` | `headerc` | `headerc` | (fetched from IDC) |
| **bodies/receipts** | — | `bodyc` (~567) + `receipts` (~63) | + same | — |
| **history index** | — | `accthist` + `storhist` (~41) + `txindex` (~13) | + same | — |
| **changesets** | — | — | `acctcs` + `storcs` (witness-derivable) | — |
| **witness** | — | — | `witness` (~167) | on-demand from IDC |
| **codes** | `codes` | `codes` | `codes` | on-demand from IDC + local cache |
| **proofs/anchors** | — | — | derivable (full PlainState) | **`anchorc` (stateless anchors)** |
| **capability** | tip state + header verify; no tx/history/proof | + historical state + tx + receipts | full semantics: any-height proof + trace + unwind + fraud-proof | verify-only via IDC proofs |
| **source data** | N42-hashed snapshot + n42-eth1(`headerc,codes`) | + n42-eth1(`bodyc,receipts,accthist,storhist,txindex`) | + N42-eth1177(`acctcs,storcs,witness`) | n42-idc-anchors + n42-codes + IDC URL |

---

## 4. Workflow (dependency-ordered)

```
Phase A  align upstream sources to 25,198,422
  A1  reth2k: confirm head, let it finish to >= 25,198,422 (already running)
  A2  geth:   start geth + lighthouse (or devp2p) → advance head to 25,198,422
              (already at 25,198,422 head from 05-28; verify still canonical)
  GATE: both clients report block 25,198,422 with matching stateRoot

Phase B  extend canonical hashed state (live eth-el)
  B1  N42-hashed 25,191,536 → 25,198,422  (eldevp2p catchup / Engine API, ~7k blocks)
      per-block / per-sub-batch stateRoot verify

Phase C  rebuild / extend distribution freezers from aligned geth+reth
  C1  n42-eth1: refresh headerc 24,998,xxx → 25,198,422 (re-import from geth ancient)
  C2  n42-eth1: extend bodyc / receipts / accthist / storhist / txindex / senders → tip
  C3  N42-eth1177: extend acctcs / storcs / witness → tip
  C4  n42-codes: extend codes → tip

Phase D  stateless-IDC anchors
  D1  n42-bpp-trie: produce block-proofs 24,998,000 → 25,198,422
  D2  n42-idc-anchors: extend anchorc → tip
  D3  blockhash-archive: extend → tip

Phase E  re-manifest + verify per mode
  E1  cmd/n42-eth-manifest → manifest-minimal / -full / -archive (blake3, range 0..25,198,422)
  E2  per-mode bootstrap smoke (minimal/full snapshot-direct; archive rebuild; stateless online)
  E3  IDC serve sample + minimal-client round-trip verify
```

**Critical ordering:** C/D depend on A (aligned geth+reth) and B (hashed state).
Do not regenerate derived data before the GATE in Phase A passes — otherwise
freezers get pinned to a stale/forked height again.

---

## 5. Tooling map (which command extends which dataset)

| Phase | Dataset | Tool / entry | Notes |
|---|---|---|---|
| A2 | geth advance | `geth --syncmode snap` + lighthouse (CL via `--authrpc`) | `D:\geth\g.bat`; `D:\lighthouse` |
| B1 | N42-hashed | eth-el `eldevp2p` catchup / Engine API | `-tags n42el`, `--hashed-canonical`, per-block root verify |
| C1-C4 | n42-eth1 / N42-eth1177 | witness-input pipeline + freezer importers | `docs/ethel/witness-input-pipeline.md`; geth ancient bodies + reth state |
| C4 | codes | code freezer extend | content-addressed, incremental |
| D1 | bpp-trie | `blockproof-produce` | `docs/ethel` bpp runbook; anchor producer |
| D2 | anchors | anchor columnar writer | `D:\n42-idc-anchors-25m` |
| E1 | manifests | `cmd/n42-eth-manifest` | blake3-256 per file + segment range |
| E2/E3 | verify | `cmd/n42-stateless-e2e`, IDC serve | three-layer E2E (header / witness / anchor) |

---

## 5b. Generation tools — verbatim commands (RECORD; reuse for every extend)

> Each derived dataset has a dedicated generator. Auto-resume reads the current
> freezer `Items()` and continues — **omit `--start` to avoid off-by-one**
> (`--start` must exactly equal current `Items()` or it errors to prevent gaps).

### senders (ecrecover cache) — `ethexec sender-recovery`
- Entry: `cmd/ethexec/main.go` (subcommand `sender-recovery`); stage `internal/ethel/sender_stage.go`.
- Inputs (mutually exclusive): `--ancient <geth-ancient>` (ecrecover from bodies) | `--bodyc <n42-columnar>` | `--erigon-db <reth-mdbx>` (reads precomputed TransactionSenders, no ecrecover).
- Resume: `startBlock = senders.Items()` (auto); `--start N` only as a contiguity assertion; `--end N` exclusive, default = input `MaxBlock()` (geth frozen−1 = 25,311,094).
- fsync every 100k blocks; parallel ecrecover workers (`--workers 0` = NumCPU).

```bash
# n42-eth1 (primary distribution) — auto-resume to geth MaxBlock
ethexec sender-recovery --ancient d:/geth/geth/chaindata/ancient/chain \
  --datadir d:/n42-eth1 --workers 0
# N42-eth1177 (archive duplicate) — same, second dir
ethexec sender-recovery --ancient d:/geth/geth/chaindata/ancient/chain \
  --datadir d:/N42-eth1177 --workers 0
```

### headerc / bodyc / receipts — `ethexec header-compact` / `body-compact` / `receipt-copy`
- All read geth ancient **read-only**, auto-resume from current end → ancient MaxBlock (frozen−1).
- columnar 8192-block segments (delta+dict+zstd for headers; freezer-style multi-file for bodies).

```bash
ethexec header-compact  --ancient d:/geth/geth/chaindata/ancient/chain --datadir d:/n42-eth1
ethexec body-compact    --ancient d:/geth/geth/chaindata/ancient/chain --datadir d:/n42-eth1
ethexec receipt-copy    --ancient d:/geth/geth/chaindata/ancient/chain --datadir d:/n42-eth1 --workers 0
```

### acctcs / storcs / witness — main `ethexec` executor (EVM replay, HEAVY)
- Re-executes blocks from geth ancient; emits acctcs + storcs + witness (+ recovers PlainState).
- `--no-outputs` skips them; `--no-witness` keeps acctcs/storcs but drops witness. Receipts are
  NOT written by the executor (use receipt-copy). Senders are an INPUT cache (run sender-recovery first).

### codes — `ethexec code-import` (reads **Reth MDBX** Bytecodes)
- `ethexec code-import --erigon-db <reth-mdbx> --datadir <out>` — DEFERRED while reth2k is live/locked.

### anchors (anchorc) — `blockproof-produce` (bpp) → anchor columnar writer (6-10h)
### N42-hashed state — eth-el `eldevp2p` catchup (`-tags n42el`, per-block root verify)
### txlookup / history index — `ethexec txlookup-build` / `history-build` / `storhist-build`
  (accthist/storhist/txindex are MPHF/RecSplit; being retired per storage-matrix — rebuild only if needed)

## 5c. Blocker hit + fix — MDBX maxdbs (2026-06-14)

When extending **acctcs/storcs/witness** via the main `ethexec` executor on
N42-eth1177 (RW), open failed:
`open mdbx: create table: qmdbMeta, MDBX_DBS_FULL: Too many DBI-handles (maxdbs reached)`.

- **Root cause (code bug, not data):** `N42TableCfg` grew to **213 tables**
  (beacon/bor/clique/qmdb/changeset families) but `lib/kv/mdbx/kv_mdbx_opts.go`
  hardcoded `OptMaxDB = 200`. Read-only Accede opens only existing tables (≤200) →
  works; RW open that must CREATE a missing table past the 200th (here `qmdbMeta`)
  fails. The failed run aborted at OPEN, before any write — N42-eth1177 untouched
  (marker still 25,252,184, tables unchanged; verified).
- **Fix:** `OptMaxDB 200 → 256` (1 line + comment). Raising maxdbs on reopen only
  adds runtime dbi slots, never rewrites data. Rebuilt ethexec → replay resumes
  correctly (`Resuming execution from=25252185`, genesis idempotent-skip,
  alignOnResume self-heals the 2-block freezer/marker overlap).
- **Pitfall noted:** `ethexec | grep | head` masked the real error (pipe exit code
  = head's 0). Always capture raw output to a file for RW data ops.

## 5d. verify-root state-root GATE + optimization (2026-06-14)

**Sequential GATE (gold standard) PASSED:** N42-eth1177 PlainState @25,311,094 →
full re-hash + FlatDBTrieLoader root = `0xee50e5b237313c3c74235b1cef85ab843d686afcfd54b56ec60c1ba8cd3bf52c`
= geth header.Root @25,311,094. **acctcs/storcs/witness extension VERIFIED correct.**
Cost **3h36m41s** breakdown: collectAcct 9m36 + **collectSto 1h22m** + loadAcct 9m48
+ **loadSto 1h5m** + **CalcTrieRoot ~40m**.

**Optimizations implemented (byte-exact equivalence-tested vs FlatDBTrieLoader):**
- **#1 memoize keccak(addr)** in storage hashing (consecutive same-addr rows) — ~2× on hash.
- **#3 parallel sharded hash** — per-worker RoTx over addr-byte shards + `etl.LoadMerged`
  (heap-merge N collectors' sorted runs). `RebuildHashedStateETLParallel`.
- **Streaming fusion** — `StreamingFullStateRoot` + `etl.StreamMerged` +
  `trie.CalcTrieRootStreaming`: stream sorted hashed leaves DIRECTLY into the trie
  builder, **eliminating the ~75min HashedAccounts/HashedStorage MDBX load** (transient
  for verify). `verify-root --workers N` (default min(NumCPU,16)) uses it; read-only.
- **Work-stealing shards (B)** — `processByteShards`: 256 single-byte shards over a
  shared queue instead of 16 fixed ranges → bounds the mega-contract straggler.
- Tests: `TestStreamingFullStateRootMatchesFlatDB` (1/4/8w), `TestRebuildHashedStateETLParallelMatchesSequential` — streaming/parallel root byte-identical to FlatDBTrieLoader.
- Pitfall: set `N42_ETL_TMPDIR=D:/etl-tmp` (ETL spill; C: too small — see MEMORY).

**Measured (real 25.3M state, workers=16):**
- sequential: 3h36m (collect 92m + load 75m + CalcTrieRoot 40m).
- streaming fixed-range: **hash 21m16s** (~4.3× vs 92m collect; skew + per-entry
  overhead cap it below 16×), **load eliminated**, trie build serial (pending).
- streaming work-stealing (B): pending comparison.
- streaming work-stealing (B): hash **13m5s** (vs 21m fixed-range; partly OS page-cache
  warmth from the prior run). Total ~1h1m (trie build still serial).
- **C2 — parallel subtrie build: correct on fixtures, BROKEN at scale, REVERTED.**
  `StreamingFullStateRootC2` + `trie.CalcTrieRootStreamingCutoff(…,cutoff=1)` +
  `trie.CombineNibbleSubtries` (16 depth-1 subtrie hashes as AHashStreamItems → top
  branch). The MPT mechanism is proven: byte-identical root to serial across 1/4/8/16
  workers on unit fixtures (`TestStreamingFullStateRootC2MatchesSerial`,
  `TestC2NibbleShardCombineMatchesSerial` — cutoff=1 gives the depth-1 subtrie hash;
  storage composite first nibble == account hashed-key first nibble so nibble-demux
  keeps each account with its storage). **But on real 2B-leaf state it deadlocked /
  ballooned to ~104 GB RAM** (killed before OOM): the two global-sorted demux
  goroutines feeding 16 lockstep consumers serialize and pile per-leaf copies
  (verify-root sets no GOMEMLIMIT). **verify-root reverted to StreamingFullStateRoot**
  (serial trie, 40min, validated). C2 needs a redesign — per-nibble PARTITIONED
  collectors during the hash phase so the 16 subtrie builds are truly independent
  (the `ConcurrentMPTRootComputer` model), not a post-hoc demux. Code kept, unwired.
  Lesson: small-fixture unit tests prove MPT correctness but NOT the at-scale
  streaming pipeline — real-data run is what exposed the deadlock/memory.
- DATC parallel-trie evaluation: **modest (~1.5-2×), not worth it** — DATC computes a
  small INCREMENTAL root per ~1000-block window (RetainList skips unchanged), where
  16-way nibble parallelism gives only ~3-5× (small/skewed dirty set + per-window 16-worker
  setup), and Amdahl caps it (~58% CalcTrieRoot + ~42% changeset/flush → ≤2.4×); window
  mode already amortizes. Nibble-parallelism's sweet spot is the FULL dense from-scratch
  rebuild (verify-root / C2), NOT DATC's incremental roots. The repo's
  `ConcurrentMPTRootComputer` (16-way nibble, PlainState/MPTBranch model) is the parallel
  precedent but not drop-in for the HashedState/FlatDBTrieLoader path (per MEMORY).

## 5e. Weekly update cadence — heights are point-in-time (maintenance)

**The published data is refreshed WEEKLY.** Every height in this doc (25,311,094 etc.)
is a **snapshot of one week's build** — next week it advances to the new geth tip.
So treat all block numbers here as point-in-time; the SOURCE OF TRUTH for "where is
each dataset" is its on-disk **height marker**, which every weekly run updates:

| Dataset | Authoritative height marker | How to read |
|---|---|---|
| n42-eth1 / N42-eth1177 freezers | per-table `Items()` (= last block + 1) | `freezer-info --dir <fz>` |
| N42-eth1177 PlainState | `ethel-last-block` (SyncStage) | `ethexec db-stats --datadir` |
| **codes** | `codes.coverage` (8-byte BE uint64 block) | `xxd codes.coverage` |
| manifests | `block_range.end` | `manifest.json` |
| N42-hashed | head block (hashed-canonical) | — |
| anchors | `anchorc` end | probe |
| source tip | geth ancient `Frozen()` | `block-stats` |

**Weekly maintenance = re-run the same generators** (§5b): they all AUTO-RESUME from
their marker to the new geth tip — `sender-recovery`, `header-compact`, `body-compact`,
`receipt-copy`, main `ethexec` (acctcs/storcs/witness), `code-import2fz` (re-export full
codes + bump `--coverage-block` to tip), bpp→anchors, eldevp2p catchup (N42-hashed).
First refresh geth ancient (the source tip) from a live geth+CL, then fan out.

**codes is content-addressed FULL history** — never window it by recency: the hottest
codes (USDT/USDC/Uniswap, deployed years ago) are the OLDEST; a "recent codes" cut would
break execution of most txs. The weekly delta is just cold new deployments; the
`code-import2fz` re-export keeps all history and only bumps `codes.coverage`.

## 6. Risks / guardrails

- **reth2k is live** — never kill; gate Phase A on its natural completion.
- **headerc stale** is the long-standing blocker — must refresh from geth ancient
  *before* anchors/bpp can extend past 24,998,000.
- **Never overwrite source freezers in place** — extend (append segments) or write
  to a new dir, verify, then swap (house rule: back up before destructive ops).
- **Verify every block's stateRoot** during B1 catchup — no batch skipping.
- Disk: derived regen is large (full ~682 GB, archive +167 GB witness). Confirm
  free space on D: before Phase C.

---

## 7. Source-correctness GATE — verification results (2026-06-14, read-only)

Tools: `cmd/block-stats`, `cmd/geth-hdr-probe` (new), `cmd/headerc-bodyc-probe`,
`cmd/freezer-info`. All open data read-only (`freezer.NewReadOnly` / `Readonly()`).

| Check | Result |
|---|---|
| geth ancient frozen | **25,311,095** (authoritative; valid indices 0..25,311,094) |
| geth tip chain continuity 25,311,084..094 | ✅ **OK** — parentHash links all consistent, not corrupt |
| geth.Root @24,998,000 vs n42-eth1 headerc.Root | ✅ **byte-identical** `0x2f81833e…1f1a6cc4` (block hash `0xd4bff4de…` also identical) |
| geth.Root @25,191,536 | `0x85bfede5…741101` = N42-hashed #150-closure recomputed root → N42-hashed canonical at its head ✅ |
| derived freezers (receipts/acctcs/storcs/witness) | all at **25,252,185**, consistent |

**GATE: PASS.** Source (geth) is canonical and uncorrupted to its frozen tip;
derived header/state data is faithfully derived from canonical mainnet. Safe to
APPEND derived data toward 25,311,095.

**reth2k:** still syncing (locked); its state/witness contribution is transitively
validated (witness-replay reproduces geth roots, prior sessions). Confirm reth
head via `reth db stats` when idle — non-blocking for append since derived data
already cross-checks to canonical geth.

## 8. Status log

- 2026-06-14 — runbook authored; inventory + heights probed; **source GATE PASS**;
  real ends established; gap table computed.
- 2026-06-14 — **senders DONE (both dirs)**: `ethexec sender-recovery` auto-resumed
  25,252,185 → 25,311,094 (58,910 blocks, ~32s each @ ~1800 blk/s, 32 workers).
  Verified `senders.Items()=25,311,095` in **both** n42-eth1 and N42-eth1177.
  senders is now fully caught up to geth frozen tip.
- 2026-06-14 — **headerc DONE**: `ethexec header-compact` 25,255,936 → 25,311,094
  (55,159 blocks, 7 segments, ~0s, 36.8% ratio).
- 2026-06-14 — **bodyc DONE**: `ethexec body-compact` 25,255,936 → 25,311,094
  (55,159 blocks, 7 segments, 3m12s, 2.61 GB, 24.7% vs raw RLP).
- 2026-06-14 — **receipts DONE**: `ethexec receipt-copy` 25,252,185 → 25,311,094
  (58,910 blocks, 2m29s, 32 workers). `receipts.Items()=25,311,095`.
- 2026-06-14 — **n42-eth1 light tables ALL caught up** to geth tip; tip cross-check
  PASS: headerc @25,311,094 hash `0xfd31698e…cccb13` + stateRoot `0xee50e5b2…3bf52c`
  **byte-identical to geth**.
- 2026-06-14 — **reth2k alignment CONFIRMED**: reth `--debug.tip
  0xfd31698e82082009d7251608655ddb0bacb6ef269575c10bd516154f90cccb13` == geth block
  **25,311,094**. reth's sync target == geth frozen tip → both clients align to the
  same block; reth state/witness usable once it finishes.
- Generators recorded in §5b: `header-compact` / `body-compact` / `receipt-copy`
  (all `--ancient <geth> --datadir <out>`, auto-resume, read-only on geth ancient).
- 2026-06-14 — **acctcs/storcs/witness DONE** (N42-eth1177): main `ethexec` EVM
  replay 25,252,185 → 25,311,094 (58,910 blocks, 37m @ ~26-30 blk/s; bottleneck =
  async witness-buffer flush + disk, NOT reth — reth was already stopped). Marker
  `ethel-last-block=25,311,094`; acctcs/storcs/witness Items 25,311,097 (freezer
  +2-3 async lead, consistent pattern). Required the maxdbs fix (§5c).
- 2026-06-14 — **verify-root** (state-root GATE) on N42-eth1177 @25,311,094:
  full RebuildHashedStateETL → compare to geth header.Root `0xee50e5b2…3bf52c`.
  - **Will it OOM? NO.** RebuildHashedStateETL uses etl.Collector (disk-backed
    external sort, buffers acct 4 GB + sto 12 GB) — memory is bounded; this is the
    OOM-safe alternative to periodic `--verify` (which holds state in RAM and OOMs
    past 10M). Live RAM stayed flat through 600M/1.58B storage slots.
  - **Real risk = TEMP DISK, not RAM.** 1st run FAILED at 38% storage hashing:
    `not enough space on disk` — ETL spill (~130 GB for 1.58B slots) defaulted to
    `C:\…\AppData\Local\Temp` (C: had only 49 GB free). **Fix:** export
    `N42_ETL_TMPDIR=D:/etl-tmp` (hashstate.go:117 honors it) → spill on D: (777 GB).
    Rerun confirmed writing to D:\etl-tmp. Cleaned 4 stale C: ETL dirs first.
- 2026-06-14 — **witness spot-check PASS** (`cmd/witness-block-trace`, ~0.2-1.4s/blk
  with MDBX base): blocks 25,260,000 / 25,290,000 / 25,311,094 all reproduce
  canonical gasUsed exactly (diff +0). Confirms the new acctcs/storcs/witness EVM
  replay is correct at sampled points while full verify-root runs.
  - Caveat (known, NOT a regression): without `--datadir` (MDBX base) the witness
    replay errors `nonce too high … state 0` — witnesses aren't fully self-contained;
    bulk replay leans on MDBX base. Pre-existing property (see MEMORY
    `project_mpt_stateless_p8` "部分 witness 不完整").
- 2026-06-15 — **codes DONE**: N42-eth1177 MDBX Code table already at tip (2,451,437
  codes, written by the EVM replay). Re-exported full-history content-addressed codes
  freezer via `code-import2fz --db d:/N42-eth1177 --outdir d:/n42-codes-25311
  --coverage-block 25311094` → 5.7 GB, `codes.coverage = 25,311,094` (verified). Old
  n42-codes-25252 untouched (swap at publish). codes is FULL history (hot codes are old
  — never window by recency).
- 2026-06-15 — **N42-hashed approach corrected.** N42-hashed (hashed-canonical state)
  is NOT needed by ANY published format — minimal/full snapshots export from PlainState
  (`reth-snapshot-export` reads PlainAccountState/Storage), archive uses PlainState+
  changesets, stateless uses anchors. N42-hashed is ONLY the eth-el live follower node's
  state. The block data is all LOCAL (don't fetch via network). N42-eth1177 PlainState is
  @ tip + verified; its hashed tables are EMPTY (HashedAccount/Storage/Trie* = 0 — the EVM
  replay wrote PlainState + changesets, NOT hashed). To advance N42-hashed: **verbatim
  copy from reth2k** — confirmed reth2k head = **25,311,094** (BlockBodyIndices lastKey =
  0x01823776) AND reth-2.2 trie byte-compatible (#150/P6 quirks handled). Running
  `n42-migrate-reth-hashed -reth d:/reth2k/db -dst D:/N42-hashed-25311/chaindata
  -head-block 25311094 -expect-root 0xee50e5b2… -phases acc,sto,tacc,tsto,code,vtrie,head`
  (fresh dir, vtrie verifies root = GATE, ~2-4h). Incremental-via-changesets
  (`MerkleStageIncremental` + `BuildRetainListFromChangesets`) is the alternative if reth2k
  were unavailable.
- 2026-06-16 — **headerc/bodyc GAP found + fixed** (earlier "DONE" was WRONG). bpp
  resume crashed: `read header 24999000: index 5208 out of range (segment has 4408)`.
  Diagnosis (NOT byte-corruption — present blocks' hashes match geth): the columnar
  headerc/bodyc had a partial segment 3051 (4408/8192 → actual coverage ~24,998,199),
  but `freezer-info` reports "3090 segments × 8192 = 25,313,280" which is CAPACITY, not
  actual. My earlier header-/body-compact extension resumed from that capacity estimate
  (25,255,936) → appended the tail but left a non-contiguous hole 24,998,200→25,255,935.
  Batch freezers (receipts/senders/acctcs/storcs/witness) were CONTIGUOUS/fine. **Fix
  (cidx-only, safe):** `cp cidx cidx.bak-gap`; verify 0..24,998,199 contiguous (probed,
  hashes match geth); `truncate -s 24408 headerc.cidx bodyc.cidx` (3051 full segments ×
  8B) → SegmentCount drops to 3051 → header-/body-compact resume from 24,993,792 and
  rebuild contiguously to 25,311,094 (old .cdat tail becomes unreferenced garbage, harmless
  — cidx is the source of truth). headerc verified: gap blocks now present, hashes match
  geth (24,999,000=0xf7c14690, 25,000,000=0xf3989761, 25,255,000=0x1efda118). bodyc
  rebuilding. **Lesson:** columnar resume uses SegmentCount×8192 — a PARTIAL last segment
  makes it over-estimate and skip a hole; bpp (continuous-read consumer) is the continuity
  validator that caught it. Verify columnar coverage by READING the gap, not the seg count.
- 2026-06-15 — **N42-hashed DONE** via reth2k verbatim copy. `n42-migrate-reth-hashed`
  → fresh `D:/N42-hashed-25311/chaindata` (163 GB): HashedAccount 394,025,618 / HashedStorage
  1,589,541,836 / TrieAccount 29,482,401 / TrieStorage 140,145,771 / Code 2,451,437;
  `ethel-last-block=25,311,094`; **vtrie root `0xee50e5b2…` == target** ✓. Old N42-hashed
  untouched (swap at deploy). **Tool upgraded (user-mandated rules):** Put→`Append` for the
  big phases (sorted source + fresh dst; AutoDupSort HashedStorage uses plain Append; ~1.36×
  — read+transform is the real floor, not write), **resume** (`newAppendWriter` reads dst
  last key → seeks source past it; acc auto-skipped, sto resumed from 375M), **graceful
  SIGINT** (`signal.NotifyContext` → per-phase commit-on-cancel, exit 0, re-run resumes —
  never `kill -9`). See MEMORY `feedback_bulk_copy_append_resume_graceful`.
- Remaining → 25,311,094: anchors/bpp (~24,998,000; `blockproof-produce`, 6-10h) — last item.
