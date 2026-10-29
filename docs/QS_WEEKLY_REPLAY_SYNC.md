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

### Step 0 commands (items 1 and 2)

Sync (item 1). Run from `D:\mainnet`: the datadir is the relative `./mainnet`, and
started from any other directory the node creates a new empty datadir there
(`D:\mainnet\n42q.bat` is the same command).

```powershell
$env:N42_BLOCK_ONLY_SYNC = '1'          # block-only sync, no state execution
Start-Process D:\mainnet\n42q.exe -WorkingDirectory D:\mainnet -WindowStyle Hidden -ArgumentList `
  '--data.dir','./mainnet','--chain','mainnet_compat', `
  '--p2p.tcp-port','10186','--p2p.udp-port','10185','--p2p.min-sync-peers','1', `
  '--p2p.bootstrap-node','enr:-Je4QGJb6IZbaceKodV55AtTd9oxwiqeVeZ1LwHA9MId-k8XAsKAAyxTi_Pf_nkRTuH-vQnICSLg2Uu_PmG3Vwx8eaIChmFzdEVucoQThzS3gmlkgnY0gmlwhAWhUVqJc2VjcDI1NmsxoQICbV6ssdll4ktSNrWY2FaYTFjio7WqVJLSPNwJMv2SBYN0Y3CCJ2aDdWRwgidl', `
  '--log.level','info' `
  -RedirectStandardOutput D:\mainnet\resync-<date>.log -RedirectStandardError D:\mainnet\resync-<date>.err
```

- Caught up when the log shows `currentNr=<N> highestExpectedBlockNr=<N>` with the
  two equal; `<N>` is the week's baseline. Then stop it: `C:\N42\sendbreak.exe <pid>`.
  Do not use `taskkill /F`; `sendbreak.exe` exits with code 58, which is its own convention.
- Do not run `n42q.exe version` from this directory to check it: it does not exit.
- 2026-10-14: 13,612,974 -> 14,066,100; `mainnet/chaindata/mdbx.dat` 45,097,156,608 B.

Copy to Linux (item 2). Only after the sync is stopped. From git bash, never a
PowerShell pipeline (it re-encodes the stream):

```bash
S=/d/mainnet/mainnet/chaindata/mdbx.dat
D=/data/blockchain/mainnet-source-<date>
ssh n42@192.168.0.166 "mkdir -p $D/chaindata"
scp "$S" n42@192.168.0.166:$D/chaindata/mdbx.dat.part       # ~118 MB/s measured
md5sum "$S"                                                   # Windows side
ssh n42@192.168.0.166 "stat -c %s $D/chaindata/mdbx.dat.part; md5sum $D/chaindata/mdbx.dat.part"
ssh n42@192.168.0.166 "mv -T $D/chaindata/mdbx.dat.part $D/chaindata/mdbx.dat"   # only if size and md5 match
```

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

## Step 0b — Cross-check result and what `receiptMismatch` means (2026-09-04)

### Both machines replayed the same source independently and agreed

Windows replayed all 13,612,974 blocks from genesis; Linux folded only this
week's 115,395-block increment. Different OS, different CPU, different binary
(Windows built fresh from `fff8a81e`, Linux running its 2026-08-28 `bin/n42`),
different starting points — and the final state is identical:

| | Windows | Linux |
|---|---|---|
| `sourceHead` | 13,612,974 | 13,612,974 |
| `number` | 13,652,362 | 13,652,362 |
| `hash` | `0x7b3f3a8f…7924` | **the same, character for character** |

The `replay_stats` fields differ only in SCOPE (full chain vs weekly
increment); every field that is comparable — `currentBlock`, `fromBlock`,
`toBlock`, `blocksMissing`, `txFailed`, `txSkipped` — matches.

Measured on the full replay, useful for planning:

| | |
|---|---|
| wall time | **1 h 33 m 47 s** (2,419 blk/s average) |
| processed | 13,612,974 blocks (1,975,448 empty), 26,176,157 txs, **0 missing, 0 failed** |
| **peak private memory** | **28.76 GB** (25,269 samples at 200 ms) |
| base size | 28.0 GB |

That peak was measured WHILE the DATC v2 build was running at 44.8 GB private
on the same box; the two coexisted with no incident. An earlier 6.68 GB figure
was from a sparse-range probe and is a floor, not the number to plan with.

### `receiptMismatch` is expected, and is NOT a failure

The full replay reported **2,979 receipt mismatches against 13,609,995
matches (0.022%)**. This is not damage.

The source is the OLD chain and the target is the NEW one, and the two sit on
**different EIP sets**, so a block's receipts are legitimately derived
differently on each side. The replayed receipts are correct for the target
chain; they simply do not reproduce a receipt root that the old chain's rules
produced. `internal/replay/engine_v2.go` now says so at the comparison itself.

What makes this checkable rather than a claim: the two machines replayed the
same source independently and produced the SAME canonical hash. Deterministic
rule differences do that; corruption does not.

So: **a non-zero `receiptMismatch` is not a gate failure.** It is an
observation counter — the run does not stop on it and the exit code is
unaffected. Do not make it fatal.

What it does NOT tell you is WHICH blocks differ; the counter records no block
numbers and the log carries no per-block line. If that is ever needed,
per-block logging has to be added at the comparison and the replay re-run
(~1.5 h for the full chain).

## Step 0c — the history-bearing base (`--qmdb-history`, since 2026-10-14)

The base above is built WITHOUT `--qmdb-history`: it carries the recent undo
window only (`--qmdb-undo-window 64`), so it can prove state at the tip and
the last 64 blocks, not at an arbitrary height. A base that must answer
any-height proofs is a separate artifact and has to be replayed from an EMPTY
target (the resume point lives in the target DB, see Step 0), so it cannot be
bolted onto an existing base.

Cost is low: the 2026-10-14 full replay of 14,066,100 source blocks took
**1h25m** (2,745 blk/s average) and produced a **90 GB** `mdbx.dat`, against
29 GB for the same chain without history.

```powershell
$env:N42_ETL_TMPDIR='D:/etl-tmp'
Start-Process C:\N42\replay-<date>\n42.exe -WindowStyle Hidden -ArgumentList `
  'replay-v2','--source','D:/mainnet/mainnet','--target','E:/qs-replay-hist-<date>', `
  '--chain','mainnet_qmdb_staggered','--tree','qmdb','--qmdb-history', `
  '--output','E:/qs-replay-hist-<date>/replay_stats-<date>.json', `
  '--log','E:/qs-replay-hist-<date>/replay-<date>.log' `
  -RedirectStandardOutput E:\qs-replay-hist-<date>\replay.out -RedirectStandardError E:\qs-replay-hist-<date>\replay.err
```

- Build the binary and the audit tools from the same commit, without
  `make n42` (it bumps VERSION): `go build -tags nosqlite,noboltdb -o
  C:\N42\replay-<date>\<name>.exe ./cmd/<name>` for `n42`, `qmdb-histcheck`,
  `qmdb-proof-fuzz`, `hotstuff-inspect`.
- Source is the old mainnet only (Step 0), stopped with `sendbreak.exe <pid>`
  after the resync log shows `currentNr == highestExpectedBlockNr`. The
  `replay.err` file fills with MDBX `spill_slowpath` lines (7 MB); that is MDBX
  debug output, not an error. The structured log is the `--log` file.
- A `gap-fill capped ... cappedAt=10000` warning appears at huge timestamp gaps
  (2026-10-14: target blocks 7,481,399 and 7,909,951). It is expected.

### The root in the header is the tree root, and it is checked

With `--tree qmdb` the header's `Root` is the QMDB root. (`--tree bmt` puts the
BMT root there instead, but the replay does NOT persist BMT nodes to MDBX - the
code says "Skip persisting BMT nodes" - nor recompute them, so a BMT base has no
history proofs; `--qmdb-history` is QMDB-only.) Both audit tools compare against
the header, not against a number they computed themselves:

```powershell
# 8 spread heights x 12 keys; reconstructed root vs block header Root
qmdb-histcheck.exe --db E:/qs-replay-hist-<date>/chaindata
# 100,000 random proofs (present and absent keys): the membership proof folds to
# the header root and round-trips the wire codec
qmdb-proof-fuzz.exe --db E:/qs-replay-hist-<date>/chaindata --n 100000
```

GATE: `TOTAL pass=96 fail=0` and `ALL 100014 PROOFS CORRECT` (`headerMissing=0`).
Both open the DB read-only (Accede), so they are safe while the files are being
copied. Measured 2026-10-14: membership proof avg 1,130 B and 3.2 ms
single-thread; absent-key proof 25 us.

## Step 1 — gracefully stop the fleet, record the head

```powershell
# pids live in E:\qs-nodeN\n42.pid (first field; the second is a start-time stamp)
foreach ($i in 0..6) { $p = (Get-Content E:\qs-node$i\n42.pid).Split()[0]; sendbreak.exe $p }
```

Wait until all 7 exit (`wmic process where "name like 'n42%'" get ProcessId`).
CTRL_BREAK only — a hard kill truncates MDBX spill and poisons the undo layer.
`n42 stop --data.dir E:\qs-nodeN --timeout 2m` does the same from the node's own
`n42.pid` (`--pid <id>` overrides; default timeout 2m).

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

`replay-v2` flags (`n42 replay-v2 --help`), those that matter for this chain. Any
flag not listed stays at its default; the defaults of `--chain` (`mainnet_v2`) and
`--tree` (`jmt`) are NOT this chain's, so both must always be passed.

| Flag | Value used | Meaning |
|---|---|---|
| `--source` / `--target` | datadir roots | source old mainnet; target base (resume point lives in the target DB) |
| `--chain` | `mainnet_qmdb_staggered` | required |
| `--tree` | `qmdb` | required (`jmt`, `bmt`, `mpt`, `trie` also exist) |
| `--qmdb-undo-window` | 64 (default) | per-block undo records for the last N blocks; 0 disables |
| `--qmdb-history` | off (default); on for Step 0c | full-history layer for any-height proofs; empty target only |
| `--fill-gaps` | true | fill timeline gaps with empty blocks |
| `--gap-period` / `--gap-tolerance` | 8 / 15 s | fill period and the gap that triggers it |
| `--gap-max` | 10000 | cap on synthetic blocks per gap (`cappedAt=10000` warning when hit) |
| `--compact-headers`, `-txs`, `-receipts`, `-logs`, `-bodies` | true | compact storage codecs |
| `--virtual-td` | true | skip all-zero TD rows |
| `--batch` | 100000 | blocks per MDBX commit |
| `--from` / `--to` | 0 / 0 | do not override the resume point; `--to 0` = source head |
| `--snapshot-at-end` | true | create a snapshot after the replay |
| `--output` | stats JSON path | overwrites; use a dated name |
| `--log` | structured log path | per-batch `qmdbRoot` lines |
| `--pprof.port` | 0 | >0 serves pprof |

Environment: `N42_ETL_TMPDIR=D:/etl-tmp` (ETL temp must not land on the system
drive). Not used on this chain: `--bls-reseal` (+ `--bls-seed`, `--bls-pool-size`,
`--bls-committee`, `--bls-ramp-blocks`), `--verify-mpt`, `--auto-topup`,
`--export-era`, `--leaf-journal`.

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

Parameters (`C:\N42\n42-ancient-seal.exe`, `C:\N42\n42-ancient.exe`):

| Flag | Default | Meaning |
|---|---|---|
| `n42-ancient-seal --source` | required | datadir or chaindata path, read-only |
| `--out` | required | output dir (gets `ancient-era/` and `chaindata/`) |
| `--seal` / `--emit-hot` | — | phase 1 (era files) / phase 2 (hot chaindata copy); the runbook runs them as separate commands |
| `--span` | 1048576 | blocks per era (power-of-two multiple of 64) |
| `--threshold` | 90000 | blocks behind head that stay hot |
| `--commit-rows` | 2000000 | hot copy: commit interval in rows |
| `--creator` | `n42-ancient-seal/1` | descriptive tag |
| `n42-ancient <ls\|verify\|fetch\|prune> --dir` | required | era store directory |
| `verify --deep --source <chaindata> --sample N` | sample 64 | cross-read N blocks per era against the base |
| `prune --class exec\|aux --before-era N` | — | future knob, see above |

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

`E:\build-seed-txindex.ps1` parameters: `-Seed` (default `E:\qs-era-out`), `-Scratch`
(`E:\qs-seedidx`), `-Bin` (default is a stale binary: always pass the current
`n42-v5.7.<latest>.exe`), `-From` (0 = the era `sealed_end` in `SEAL_DONE.json`),
`-TimeoutMin` (90). Inside, it runs an indexer node on the scratch copy with:

```
--chain mainnet_qmdb_staggered --profile n42 --data.dir <Scratch>
--p2p.no-discovery --p2p.min-sync-peers 0 --p2p.local-ip 127.0.0.1 --p2p.host-ip 127.0.0.1
--p2p.tcp-port 32100 --p2p.udp-port 33100
--http --http.addr 127.0.0.1 --http.port 20100 --http.api eth,web3,net
```

with `N42_TXINDEX_TAIL=1`, `N42_MDBX_MAPSIZE_GB=128`; completion signal is the log line
`txindex tail enabled`. `C:\N42\txindex-watermark.exe --db <chaindata> [--set N | --clear]`
reads, sets or clears the start watermark (no flag = read). Linux twin:
`scripts/qs/build-seed-txindex.sh [--seed DIR] [--scratch DIR] [--bin PATH] [--from N] [--timeout-min N]`.

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

`E:\deploy-7node.ps1` parameters: `-Data` (seed dir to copy), `-Bin` (node binary),
`-ValMd` (default `E:\qs-validators.md`, BLS keys, never committed), `-NodeRoot`
(default `E:\qs-node`, giving `E:\qs-node0..6`), `-TxGenMax` (default 31 simulated
txs per block; 0 = off). Environment it sets for every node: `N42_MDBX_MAPSIZE_GB=128`,
`N42_TXINDEX_TAIL=1`. Per node `i` (0..6) the arguments are:

```
--chain mainnet_qmdb_staggered --profile n42 --data.dir E:\qs-node<i>
--engine.miner --engine.etherbase <validator i address>
--p2p.no-discovery --p2p.local-ip 127.0.0.1 --p2p.host-ip 127.0.0.1
--p2p.tcp-port <32000+i> --p2p.udp-port <33000+i> --p2p.min-sync-peers 0
--http --http.addr 127.0.0.1 --http.port <20012+i> --http.api eth,web3,net
--mobileverify.http 127.0.0.1:<21012+i>
--pprof --pprof.port <6090+i> --pprof.mutex --pprof.block
--p2p.peer /ip4/127.0.0.1/tcp/<32000+j>/p2p/<peer id j>      (for every j != i)
node 0 only, when -TxGenMax > 0:  --dev.txgen --dev.txgen.max <TxGenMax> --dev.txgen.key <dev faucet key>
```

The 7 peer IDs and the fixed libp2p network keys (`11…`, `22…`, … `77…`, 32 bytes each)
are in the script; the faucet key is keccak("n42-dev-faucet-v1") and is not
reproduced here. The script also writes `keystore\bls_<address>.key` and
`network-keys` into each node dir after seeding, and starts each node hidden with
output in `E:\qs-node<i>\run.log` / `run.err`.

Rolling one node onto a new binary (verify convergence before the next):

```powershell
pwsh -File E:\roll-one-node.ps1 -Node 3 -Bin C:\N42\N42-gov5\build\bin\n42-v5.7.<latest>.exe
# also: -ValMd E:\qs-validators.md  -NodeRoot E:\qs-node  -SendBreak C:\N42\sendbreak.exe  -TxGenMax 31
```

It takes its environment from `deploy-7node.ps1`'s `$env:` lines, never from its own copy.

Linux counterparts in `scripts/qs/`: `stop-fleet.sh [--timeout S] [--no-inspect]`,
`deploy-7node.sh [--data DIR] [--bin PATH] [--txgen-max N]`,
`roll-one-node.sh --node N [--bin PATH] [--txgen-max N]`; all source `qs-env.sh`
(layout and levers: `QS_ROOT`, `QS_SEED`, `QS_BASE`, `QS_BIN`, `QS_TOOLS`,
`QS_VALIDATORS`, `N42_DEV_FAUCET_KEY`; ports `QS_TCP_BASE` 32000, `QS_UDP_BASE`
33000, `QS_HTTP_BASE` 20012, `QS_MOBILE_BASE` 21012, `QS_PPROF_BASE` 6090). One Linux-only
setup: `sudo sysctl -w net.ipv4.ip_local_reserved_ports=33000-33006` and
`ulimit -n 65536`. Note that `scripts/qs/README.md` still names the stopped fleet's
`qs-node0` as the weekly replay source; Step 0 above overrides it (old mainnet only).

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

### 2026-10-14 (old-mainnet resync + history-bearing replay + copy to Linux)

Order followed: Step 0 (sync the old mainnet), Step 0c (replay from empty with
`--qmdb-history`), audit, copy. The fleet was not touched and no fleet blocks
were folded in.

- **Resync**: `D:\mainnet\mainnet`, `n42q.exe` with `N42_BLOCK_ONLY_SYNC=1`
  (command in `D:\mainnet\n42q.bat`, cwd `D:\mainnet`), 13,612,974 -> **14,066,100**
  (453,126 blocks, ~11 min at ~500 blk/s, 1 peer). Caught up when the log printed
  `currentNr=14066100 highestExpectedBlockNr=14066100`; stopped with
  `sendbreak.exe`, clean shutdown, `mdbx.dat` 45,097,156,608 B.
- **Replay** (new target, from genesis): `E:\qs-replay-hist-20261014`, chain
  `mainnet_qmdb_staggered`, `--tree qmdb --qmdb-history`, binary built from
  `843960f6`, **1h25m24s**, 14,066,100 blocks (1,976,062 empty, 0 missing),
  27,206,380 txs all replayed, **txFailed 0**, receiptMatch 14,063,019 /
  receiptMismatch 3,081 (0.022%, the expected old-vs-new EIP-set difference, same
  as 2026-09-04), 0 errors in the log.
- **checkpoint.json**: `sourceHead` 14,066,100, target `number` **14,105,498**
  (39,398 gap-fill blocks), `hash`
  `0xa7f2c0a16d2d2e0a482b4d5566f18abff3d945ea64731cf044602c7ed6a9c269`.
  Final QMDB state: liveKeys 6,461,830, twigs 32,254, nextSlot 66,054,384, last
  batch `qmdbRoot=0xdc3e8af2ff7861...`. Previous clean base (2026-09-04, no
  history): sourceHead 13,612,974, number 13,652,362, hash `0x7b3f3a8f...7924`.
- **Header root + history audit** (Step 0c gate): `qmdb-histcheck` TOTAL pass=96
  fail=0 over heights 1 / 2,015,072 / 4,030,143 / 6,045,214 / 8,060,285 /
  10,075,356 / 12,090,427 / 14,105,498; `qmdb-proof-fuzz` seed 42, 100,014 proofs,
  0 failures, headerMissing=0, membership proof avg 1,130 B (max 1,167 B), 3.2 ms
  avg / 4.9 ms p99, absent-key 25 us.
- **Copy to Linux**: `/data/blockchain/qs-replay-hist-20261014/` (`chaindata/mdbx.dat`
  plus `checkpoint.json`, `network.json`, `replay_stats-20261014.json`,
  `replay-20261014.log`). `scp` to `mdbx.dat.part`, then renamed. 90,194,313,216 B,
  12m44s (~118 MB/s), **md5 `47b2071a384b1b3a0ab9e3d8f8770d9e` on both sides**.
  n42dev `/data` had 1.2 TB free beforehand.
- **Not done this run**: (1) the old-mainnet SOURCE (45 GB) was not copied to
  Linux, so there is no independent Linux replay to compare checkpoints with
  (Step 0 item 3); (2) the Linux copy was verified by size + md5 only, the audit
  tools were not run on Linux (they are Windows builds); (3) `qs-replay-hist-*` is
  NOT a fleet seed - it has no era layout / txindex (Steps 2b, 2c) and was not
  deployed. The no-history base `E:\qs-replay-win` was left untouched.
- **Traps hit**: `n42q.exe version` does not exit (it starts a node); never use it
  to probe the build. It was started from the repo directory, not `D:\mainnet`,
  and was stopped with `taskkill /F` (plain `taskkill` is refused by a console-less
  process); the repo stayed clean. A bash `sleep N; cmd` over ~100 s is
  rejected by the tool, so long waits go through a Monitor loop.

### 2026-10-21/22 (weekly: old-mainnet resync, incremental history replay, era seed, 7-node restart)

Binary and tools from commit `dcddd8b8` plus the marker fix below, built with
`go build` into `C:\N42\replay-20261021\` (`n42`, `n42-ancient-seal`, `n42-ancient`,
`txindex-watermark`, `qmdb-histcheck`, `qmdb-proof-fuzz`, `hotstuff-inspect`,
`hotstuff-reset`, `txflood`).

- **Sync** (Step 0 command): 14,066,100 -> caught up at `currentNr=14138457
  highestExpectedBlockNr=14138457`, stopped with `sendbreak.exe` (the node had
  followed on to 14,138,895 by then, which is the replay's source head). The
  caught-up line is printed with colour codes, so a plain `grep` for
  `currentNr=N highestExpectedBlockNr=N` misses it.
- **Incremental replay** into `E:\qs-replay-hist-20261014` (resume from the target
  DB, same flags as Step 0c, `--qmdb-history`): 72,795 blocks in **1m03s**
  (1,163 blk/s), 148,651 txs, **txFailed 0**, receiptMatch 72,752 / mismatch 43,
  0 errors. checkpoint: `sourceHead` 14,138,895, `number` **14,178,293**, `hash`
  `0x4996f21e0c3956ca2ac0bead731438b43f6f521ee3b4d6b18185a32b922c3d44`.
- **Audit**: `qmdb-histcheck` TOTAL pass=96 fail=0 (heights up to 14,178,293);
  `qmdb-proof-fuzz` 100,014 proofs, 0 failures, headerMissing=0, membership proof
  avg 1,130 B, 3.5 ms avg / 5.2 ms p99.
- **Seed** `E:\qs-era-out-20261021` (new directory, not the 08-16 seed: the base
  lineage changed): `--seal` 13 eras / 39 files in 8m58s (7.9 GB); `--emit-hot`
  hot MDBX 62 GB (the history tables are carried); `verify --deep --sample 64`
  `deep verify OK: 832 blocks`; `SEAL_DONE.json` head 14,178,293, sealed_end
  13,631,488; txindex 1 segment from 13,631,488 (`txindex-watermark` reads
  13631488).
- **Fleet**: `E:\qs-node0..6` re-seeded (previous generation moved to
  `-pre20261022`), `deploy-7node.ps1 -Data E:\qs-era-out-20261021 -Bin
  C:\N42\replay-20261021\n42.exe`. Startup fingerprint `appliedNum=headNum=14178293`.
- **10-minute window** (sampled every 60 s over 611 s): 305 blocks, ~2 s/block,
  all 7 nodes within one block of each other, **one block hash per height at every
  sample**, 0 `error` lines on all 7 nodes, no `ValidateState` / root-mismatch /
  rolling-it-back. Warnings only at start (peer dial, IPC, a few view timeouts,
  dev-faucet funding retries). `eth_getTransactionByHash` returned the right
  block on nodes 0, 3 and 6 for transactions in block 14,178,600, 14,178,000
  (seed tail), 13,700,000 and 13,632,000 (txindex range).
- **Mobile verification surface**: all 7 `--mobileverify.http` ports
  (21012..21018) answer `/mobileverify/health` (`alarms 0`, `registry 0`,
  `certBlocks 0`, `openWindows 0`), `/mobileverify/alarms` is `[]`, the log shows
  packet service, cohort relay, registration replication and the BLS committee
  pool wired (committee 512, pool 200,000, ramp 1,000,000). **Open:**
  `/mobileverify/packet/<block hash>` returned 404 on all 7 nodes for the 8 most
  recent blocks tested although the blocks carry 5-29 txs and no
  `packet build failed` / `packet publish failed` warning was logged.
  **Cause (read from the code and checked on the stored headers of 9 blocks):** the
  leader builds the packet from a copy of the header taken in assemble, i.e. BEFORE
  sealing, and keys it by that copy's `header.Hash()`. `buildHeaderExtra` reserves
  the last 96 bytes of `Extra` as zeros and `Seal` fills them in place with the BLS
  signature, so the sealed block's `Hash()` (the one a phone knows, and the one
  `/mobileverify/packet/` and the receipt lookup use) differs from the key. On all 9
  stored blocks the hash with the 96 bytes zeroed differs from the sealed hash. Not
  proved on the running endpoint (the cache is in memory and the fleet was stopped
  before the cross-check). **Fixed in `0aeb71e4`:** the transactions and the recorder
  now ride on the task and the packet is built in `writeAndFinish` from the SEALED
  header once the block is stored (speculative builds, which never produced a
  packet before, are covered too). Tested through `commit` -> `handleSealed`; the
  fleet has not been restarted on the fixed binary, so `/mobileverify/packet/<hash>`
  returning 200 is still to be confirmed live. No device was
  registered, so receipts and certificates (`/mobileverify/cert/`) were not
  exercised.
- **Traps hit**:
  1. **`replay-v2` left the QMDB applied marker at block 0** (genesis init writes
     it, replay never advanced it). A node booting from such a target sees PlainState
     at the head and the marker at 0, logs `PlainState is ahead of the applied marker
     ... rolling it back` / `refusing to realign ... startup heal failed`, pulls the
     canonical head back to genesis and builds from block 1 (all 7 nodes did,
     height 0). The 08-16 seeds had no marker at all, which is why they booted.
     Fixed in `37e19255` (the marker follows the head in the same transaction;
     test `TestEngineV2_QMDBAppliedMarkerFollowsHead` fails without it). Bases
     replayed before the fix were repaired once by writing
     `DatabaseInfo/qmdbAppliedHead = (14178293, 0x4996f21e...)` after checking that
     hash is block 14,178,293; the check for a future base is
     `startup state fingerprint` showing `appliedNum == headNum`.
  2. **Stale tools in `C:\N42\`**: `n42-ancient-seal.exe` / `n42-ancient.exe` /
     `txindex-watermark.exe` were 08-16 builds and `--emit-hot` failed with
     `bucket "BadHeaderNumber" not in N42TableCfg` (the table was added on
     09-06). Build all three from the same commit as the binary.
  3. `build-seed-txindex.ps1` hard-codes `C:\N42\txindex-watermark.exe`; a copy with
     the path replaced was used, the script on `E:\` was not edited.
  4. A background shell held by the tool is reaped under memory pressure
     (`--emit-hot` was stopped once at the final commit, with free RAM near zero
     while reth2k was syncing). Start long jobs with `Start-Process` and watch
     them with a Monitor.

### 2026-10-29 (weekly: old-mainnet resync, incremental history replay, era seed, 7-node restart, first end-to-end mobile verification)

Tools from `a0c0c1fd`, built with `go build` into `C:\N42\replay-20261029\`. The fleet
binary was rebuilt twice during the cycle: `f26b1b2d` for the deploy (EIP-7843
`SlotNumber` header layout, EIP-7825), then `66c5d7e5` by a rolling restart (read-log
fix below).

- **Sync**: 14,138,895 -> `Chain sync completed block=14220015`, stopped with
  `sendbreak.exe`.
- **Incremental replay** into `E:\qs-replay-hist-20261014` (`--qmdb-history`): 81,120
  blocks in 1m28s, **txFailed 0**, receiptMismatch 18, 0 missing. checkpoint:
  `sourceHead` 14,220,015, `number` **14,259,413**, `hash`
  `0xc2cf38dfbb3a9e331ff39a09e0110e7d83279f49b1e2383452bebad220fef1e2`, qmdbRoot
  `0x5ac57e7ebeebb1..`.
- **Audit**: `qmdb-histcheck` TOTAL pass=96 fail=0; `qmdb-proof-fuzz` 100,014 proofs,
  all correct.
- **Seed** `E:\qs-era-out-20261029`: 13 eras / 39 files (7.9 GB); `--emit-hot` hot
  MDBX 66.6 GB; `verify --deep --sample 64` `deep verify OK: 832 blocks compared`;
  `SEAL_DONE.json` head 14,259,413, sealed_end 13,631,488; checkpoint.json copied in;
  txindex 1 segment (5.2 MB) from 13,631,488, watermark 13631488.
- **Header layout**: the seed is valid under the new layout (`MobileRegistryRoot`
  moved to RLP position 24 behind EIP-7843 `SlotNumber`) because replay-v2 never
  stamps the registry root, so every base hash is unchanged; blocks the fleet built
  before this cycle would not be (their generation was moved aside, not reused).
- **Fleet**: older generations `-pre20260816`, `-pre20261022`, `-bad20261022` deleted
  (955 GB, E: was down to 538 GB free with a 490 GB seed copy ahead); the current one
  moved to `-pre20261029`. Startup fingerprint `appliedNum=headNum=14259413`,
  `treeRoot 5ac57e7ebeebb11a` (= the replay's qmdbRoot).
- **10-minute window** (10 samples, 60 s apart): 14,259,468 -> 14,259,745, all 7
  nodes at the same height (one sample one block apart), **one hash per height at
  every sample**, 0 new `error`/`crit` lines.
- **Mobile verification, first end-to-end run** (`mobileverify-device`, 7 simulated
  devices, one per node, 20 blocks): registration and the 30 s epoch commit worked
  on the first try; re-execution failed on every block until three bugs were fixed:
  1. the SDK verifier skipped the start-of-block system calls (EIP-4788/2935) whose
     reads open the read log, consumed by position -> `nonce too high ... state: 1`
     (`fc3f59f6`);
  2. the leader's stale-nonce trim read every pending sender through the read-log
     recorder in map order -> `nonce too low/high` (`66c5d7e5`, needs the node
     binary: rolling restart 6 -> 0, ~2.5 min, each node caught up in ~20 s);
  3. the SDK compared the Ethereum receipt trie root while native headers carry the
     concat root (`e798c991`, device only).
  After them: **20/20 blocks certified, 7/7 devices verified and submitted on every
  block, 0 failures** (blocks 14,260,853-14,260,872, 2-30 txs each).
- **Not covered yet**: a receipt from a failed fill candidate (a tx that read state
  and was then dropped from the block) would shift the log the same way; none
  occurred in the run (txgen pending lists are contiguous).
