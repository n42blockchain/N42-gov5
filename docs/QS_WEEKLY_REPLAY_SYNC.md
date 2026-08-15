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
  'replay-v2','--source','E:/qs-node0','--target','E:/qs-replay-v4', `
  '--chain','mainnet_qmdb_staggered','--tree','qmdb', `
  '--output','E:/qs-replay-v4/replay_stats.json' `
  -RedirectStandardOutput E:\qs-replay-v4\replay-inc-<date>.log `
  -RedirectStandardError E:\qs-replay-v4\replay-inc-<date>.err -WindowStyle Hidden
```

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

GATE: exit clean + `checkpoint.json` number == H (+ per-batch `qmdbRoot` lines,
no errors).

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
