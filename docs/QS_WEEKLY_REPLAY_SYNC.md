# QS fleet weekly replay-sync runbook

Weekly cycle that folds the live `mainnet_qmdb_staggered` fleet's new blocks
into the canonical replay-v2 base (`E:\qs-replay-v4`), then restarts the fleet
on the freshly extended base. Result: the fleet always boots from a clean,
compact-codec, gap-filled replay target, and the replay base never falls more
than a week behind the live chain.

First executed end-to-end 2026-08-10 (this file records that run's measured
numbers). Companion doc for the eth-el side: `docs/ethel/weekly-update-runbook.md`.

## Why

- The replay-v2 target is the **portable canonical base**: compact header/tx/
  receipt/log/body codecs, gap-filled timeline, QMDB commitment, checkpoint
  manifest. Fleet node dirs accumulate consensus journals, logs, undo layers —
  the replay base stays clean.
- Re-seeding weekly keeps every node's chaindata identical and bounds per-node
  disk growth (node dirs grow ~2x the base between reseeds).
- The cycle doubles as a full-chain re-execution audit of the week's blocks:
  replay-v2 re-runs every tx and recomputes the QMDB root per batch.

## Prerequisites

- Fleet running (or stopped) on `E:\qs-node0..6`, chain `mainnet_qmdb_staggered`.
- Current binary: `C:\N42\N42-gov5\build\bin\n42-v5.7.<latest>.exe` — the SAME
  binary drives replay-v2 and the fleet (it is the `n42` CLI). Never mix: an old
  wk-style exe may silently ignore newer flags (see the 2026-08-09 codes-export
  trap in the ethel runbook).
- `E:\qs-replay-v4` = current replay target (chaindata + checkpoint.json).
- `E:\deploy-7node.ps1` + `E:\qs-validators.md` (BLS keys, seed 0x42…42).
- `sendbreak.exe` (AttachConsole + CTRL_BREAK broadcaster, source mirrors
  `cmd/n42/stop_windows.go`) — or `n42 stop`.

## Step 0 — THE BASE'S SOURCE IS THE OLD MAINNET, NOT THE FLEET (policy, 2026-09-04)

**Read this before Step 1. It corrects what Steps 1-2 below say.**

The canonical base is built ONLY from the old mainnet (`mainnet_compat`,
chain ID 94, genesis `138734b7…`), which lives at `D:\mainnet\mainnet` on the
Windows box and is synced from the network there. **The fleet's own blocks are
TEST data and are never folded into the base.**

Weekly order:

1. **Sync the old mainnet on Windows** to its current network head. That height
   is the week's BASELINE — the replay can go exactly that far and no further.
   Stop it with CTRL_BREAK (`sendbreak.exe <pid>`); the caught-up signal is
   `currentNr == highestExpectedBlockNr` in the resync log, after which it
   switches to live-follow.
2. **Copy that data to Linux.** One 40 GB MDBX file; chunked transfer with a
   per-chunk byte-count check and a full md5 on both sides afterwards.
   2026-09-04: `/data/blockchain/mainnet-source-win`, md5
   `dc797adf00c9f0823a07b92a199b7af2`.
3. **Replay from that copy on BOTH machines, independently.** Each machine's
   result is its own base. Two independent executions of the same input from
   the same starting state are the cross-check: compare `checkpoint.json`
   (`sourceHead`, `number`, `hash`), `replay_stats.json`, and the per-batch
   `qmdbRoot` lines.
4. Seal / emit-hot / txindex / re-seed as in Steps 2b-3.

### Why: folding fleet blocks forks the base, silently

Step 2 below passes `--source E:/qs-node0` — a FLEET NODE. The fleet produces
its own blocks on top of the replayed head (stress-test load, `--dev.txgen`),
and those are not mainnet history. Folding them in permanently forks the base
from the chain it is supposed to mirror, and nothing reports an error.

That already happened. Two lineages exist:

| base | checkpoint number | what it contains |
|---|---|---|
| `E:\qs-replay-v4` (49 GB) and `qs-replay-v5` (65 GB) — same checkpoint | **14,288,280** | old-mainnet history **plus folded fleet blocks** |
| `qs-replay-linux` (29 GB) | **13,652,362** | old-mainnet history only — **the clean lineage** |

The 636k-block gap between them is fleet output, not chain history. The clean
base's `sourceHead` (13,497,579 before this week's fold) matched the Windows
old-mainnet's height exactly — that fingerprint is how the correct source was
identified.

**Do not use the 14,288,280 lineage as a base.** It is kept for reference only.

### A source that is NOT the right one, on the same box

`/data/blockchain/mainnet-source` on Linux looks like the old mainnet and is
not: genesis `883bb3e2…` (not `138734b7…`) and it stops at 13,205,073.
Always check the genesis in the startup banner and the height against the
base's recorded `sourceHead` before pointing a replay at a source.

### Replay's resume point lives in the target DB

Measured 2026-09-04: `checkpoint.json` is an OUTPUT. Rewinding it does nothing
(0 blocks processed), and `--from`/`--to` do not override the resume point
either (also 0 blocks). To re-run a range that has already been folded you need
an EMPTY target or one that is genuinely behind. This matters when reproducing
a fold for measurement or for a cross-check.

## Step 1 — gracefully stop the fleet, record the head

```powershell
# pids live in E:\qs-nodeN\n42.pid (first field; the second is a start-time stamp)
foreach ($i in 0..6) { $p = (Get-Content E:\qs-node$i\n42.pid).Split()[0]; sendbreak.exe $p }
```

Wait until all 7 exit (`wmic process where "name like 'n42%'" get ProcessId`).
CTRL_BREAK only — a hard kill truncates MDBX spill and poisons the undo layer.

Record the converged stop state (all 7 must agree):

```bash
for i in 0 1 2 3 4 5 6; do go run ./cmd/hotstuff-inspect -datadir E:/qs-node$i/chaindata; done
# expect identical committedQC=<view>/<hash> on every node
tail /e/qs-node0/log/n42.log   # last "commit-to-canonical phases" number = final head H
```

2026-08-10: committedQC=533538/59eb15d0 on all 7; H = 13,912,188.

## Step 2 — incremental replay-v2 into the base

```powershell
Start-Process C:\N42\N42-gov5\build\bin\n42-v5.7.<latest>.exe -ArgumentList `
  'replay-v2','--source','<OLD-MAINNET DATADIR>','--target','<BASE>', `   # NOT a fleet node -- see Step 0
  '--chain','mainnet_qmdb_staggered','--tree','qmdb', `
  '--output','E:/qs-replay-v4/replay_stats.json' `
  -RedirectStandardOutput E:\qs-replay-v4\replay-inc-<date>.log `
  -RedirectStandardError E:\qs-replay-v4\replay-inc-<date>.err -WindowStyle Hidden
```

- **`--source` MUST be the old-mainnet datadir, never a fleet node.** This
  line used to read `--source E:/qs-node0`; that folds the fleet's own
  stress-test blocks into the base and forks it from mainnet history
  permanently, with no error. See Step 0 for the two lineages that already
  exist because of it.
- **`--source` / `--target` take the datadir ROOT** — the tool appends
  `/chaindata` itself. Passing `E:/qs-node0/chaindata` fails with
  `open source DB … Accede mode`.
- Resume is automatic: the target stores its last source block; the run logs
  `resuming from checkpoint lastSourceBlock=<N> startBlock=<N+1>` and `--to 0`
  (default) auto-targets the source head. Verify the logged range matches
  Step 1's H before walking away.
- All other flags stay default (compact codecs on, gap-fill on, undo-window 64,
  qmdb-history off) — the same set the v3/v4 base was built with.
- Detach with `Start-Process`, NOT a background shell — a reclaimed shell kills
  its children mid-write (learned 2026-08-09, ethel snapshot export).
- Rate depends entirely on tx density: empty/light blocks replay at ~6,000
  blk/s, stress-test-era full blocks at ~150 blk/s. 2026-08-10: 708,139 blocks
  in ~1.2 h (~165 blk/s, ~200-tx blocks).

GATE: exit clean + `replay_stats.json.currentBlock == H` +
`checkpoint.json.sourceHead == H`; when gap-fill inserted blocks,
`checkpoint.json.number` is the **target** head (`H + synthetic blocks`), and
its hash must equal the target canonical hash at that height. Also require
per-batch `qmdbRoot` lines and no errors. The separate source and target fields
prevent a source number from being paired with a target hash.

## Step 2b — seal eras + emit the hot layout (since 2026-08-15)

The fleet now boots from the **era layout** (docs/QS_ANCIENT_ERA_DESIGN.md):
hot MDBX (state + QMDB + navigation + recent window) + read-only
`ancient-era` files. `E:\qs-replay-v4` stays the full canonical base; the
seed artifact is derived from it each week:

```powershell
# Seal (resume-safe: previously sealed eras are skipped — most weeks this
# seals 0 or 1 new era). Can run concurrently with Step 2's replay: sealed
# ranges are immutable.
C:\N42\n42-ancient-seal.exe --source E:/qs-replay-v4 --out E:/qs-era-out --seal
# Hot MDBX (AFTER Step 2 completes — cuts at the folded head):
C:\N42\n42-ancient-seal.exe --source E:/qs-replay-v4 --out E:/qs-era-out --emit-hot
# Verify: full payload scrub + per-era sampled byte-compare vs the base
C:\N42\n42-ancient.exe verify --dir E:/qs-era-out/ancient-era --deep --source E:/qs-replay-v4/chaindata --sample 64
Copy-Item E:\qs-replay-v4\checkpoint.json E:\qs-era-out\checkpoint.json
```

GATE: verify prints `deep verify OK`; `SEAL_DONE.json` head == Step 2's
target head. 2026-08-15 first run: 13 eras / 39 files / 17 GB sealed in
~13 min, hot MDBX 8.0 GB (was 49 GB), deep verify 832 blocks clean.

- `--emit-hot` restarts from scratch if interrupted (safe); `--seal`
  resumes per era file.
- The node auto-attaches `<datadir>/ancient-era` at boot — look for the
  `era store attached` log line with `degraded=0`.
- `n42-ancient prune --class aux --before-era <N>` is the (future) knob
  for dropping old witnesses/changesets; the chain class is not prunable.

## Step 2c — build the tx-lookup index into the seed (since 2026-08-16)

The fleet runs with `N42_TXINDEX_TAIL=1` (set in `deploy-7node.ps1`): without
it every transaction writes a random-key `TxLookup` row into MDBX and
`mdbx_txn_commit` dominates the block cycle — 1.9 s/block at 22.8k txs
against 0.5 ms with the tier on (`docs/QS_TPS_BENCHMARK.md`).

The tier keeps its index in `<datadir>/txindex`, so the seed has to carry
one or every reseed would start lookups over from the new head.

```powershell
& E:\build-seed-txindex.ps1 -Seed E:\qs-era-out
```

What it does, and why it is shaped this way:

- **It builds on a throwaway copy, not on the seed.** A running node also
  writes HotStuff state into its chaindata, and the seed is copied verbatim
  to all 7 nodes — seeding them with one node's consensus state is not
  acceptable. Only `txindex/` (pure derived data) moves into the seed.
- **It seeds the start watermark** (`txindex-watermark --set`). The indexer
  defaults the watermark to `head+1` on first run — correct for a live node
  (no back-scan), wrong for a seed. Nodes booting from the seed then take
  `max(watermark, SealedEnd(txindex))`, so they only rebuild the short tail.
- **Never copy a previous generation's `txindex` forward by hand.** replay-v2
  is append-only, so heights already in the base keep their numbers and
  their index entries stay valid — but the week being folded in is
  renumbered by gap fill (e.g. source head 14,227,107 landed at 14,266,472).
  An index built against the old numbering points at the wrong blocks, which
  is worse than having none. Rebuilding from the seed is what makes the
  carry-over correct.

GATE: `E:\qs-era-out\txindex` contains `txindex.NNNN.cdat` + `.cidx` +
`.ranges`, and `txindex-watermark --db E:\qs-era-out\chaindata` reports the
range start. 2026-08-16 first run: 22 MB covering 13,631,488 → 14,323,711.

## Step 3 — re-seed the fleet on the extended base

```powershell
# keep ONE previous fleet generation as rollback; delete the generation before it
foreach ($i in 0..6) { Move-Item E:\qs-node$i E:\qs-node$i-pre<date> }
# -Data is the ERA LAYOUT dir (Step 2b output), not the raw replay base
pwsh -File E:\deploy-7node.ps1 -Data E:\qs-era-out -Bin C:\N42\N42-gov5\build\bin\n42-v5.7.<latest>.exe
```

- deploy-7node.ps1 seeds a node dir **only if it does not exist** — moving the
  old dirs aside is what makes it re-seed. It copies `$Data` wholesale (7 × base
  size; 26 GB base → ~180 GB total in the 2026-08-10 run), then places BLS keys
  + fixed network keys and launches all 7.
- `-Bin` MUST be passed: the script default is a stale binary.
- The replay base carries no HotStuff journal, so the new fleet starts
  consensus fresh from the replayed head — equivalent to a clean
  hotstuff-reset; no extra reset step needed.

## Step 4 — acceptance

- All 7 pids up; `eth_blockNumber` on 20012..20018 advancing past H within
  ~1 min and identical across nodes at the same instant (single hash per height).
- `hotstuff: commit phases` lines resuming in `E:\qs-node0\log\n42.log`.
- No `ValidateState` / root-mismatch warnings in the first minutes (QMDB
  three-state self-heal would surface a bad base immediately).
- Rollback: stop fleet, move `qs-nodeN-pre<date>` back, relaunch with the same
  deploy command minus re-seed (dirs exist → no copy).

## Weekly disk hygiene

- After a verified restart, the PREVIOUS `qs-nodeN-pre*` generation (kept as
  rollback) can be deleted the NEXT week once the new one passes acceptance.
- `E:\qs-replay-v3` is the 07-27 full-replay ancestor of v4 — retained as the
  from-genesis rebuild reference; do not extend it.

## Known deviations (accepted for this chain)

The replay base is NOT byte-exact with the live chain, by design: gap-fill
inserts synthetic empty blocks, so target block numbers, timestamps, baseFee
sequence and BLOCKHASH window all shift. On a chain whose traffic is synthetic
txgen/txflood load, a small fraction of replayed txs consequently drifts:
2026-08-10 measured `txFailed` 105,080 of 128.8 M (0.082 %, all `evm_error` —
nonce-too-low cascades once one tx in a sender chain diverges) and
`receiptMismatch` 36,853 (v3 full-replay baseline was already 2,952 non-zero).
The target chain is internally consistent (per-batch QMDB roots, zero errors)
and that is the acceptance bar for this synthetic-load network. A real-asset
chain would need `--fill-gaps=false` and a byte-exact receipt gate instead.

## Run log

### 2026-08-10 (first full cycle)

- Fleet stopped clean: 7/7 identical committedQC=533538/59eb15d0,
  H=13,912,188 (chain had been live through the 08-04/05 throughput-tuning
  cycles and the 08-05 hotstuff-reset).
- Incremental replay: 13,204,050 → 13,912,188 = 708,139 source blocks /
  128.7 M txs in 52m46s (~224 blk/s; tx-dense stress-era blocks — empty-era
  replays run ~6,000 blk/s). Target chain head 13,951,553 (+39,365 gap-fill
  empties). Deviations: see section above. Traps hit: `--source` must be the
  datadir ROOT (tool appends /chaindata; passing the chaindata path fails with
  an Accede-mode error); `--output` OVERWRITES the previous stats json — point
  it at a dated filename next time.
- Re-seed: base had grown 26 → 47 GB with the increment + undo layer; 7 copies
  ≈ 330 GB, a few minutes each on NVMe.
- Restart: 7/7 up on n42-v5.7.947, `eth_blockNumber` identical across all
  nodes and advancing ~1.5 blk/s with txgen load; commit phases ~1.0-2.1 ms
  total; zero ValidateState / root-mismatch warnings. Old generation kept at
  `E:\qs-nodeN-pre20260810` for rollback until next week's cycle passes.
