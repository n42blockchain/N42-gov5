# Session 2026-07-05/06 — DATC B′ perf fixes + N42 legacy replay-v2 kickoff

Status at handoff: DATC 25M B′ rebuild gracefully stopping (mid-shutdown, waiting for
batch boundary); N42 legacy chain synced to live and stopped; replay-v2 not yet started
(deferred for memory headroom, now proceeding per user decision to prioritize it).

## 1. DATC 25M B′ rebuild (gov5-p4b, D:/n42-datc-bprime2-25m)

**Binary**: `C:\N42\gov5-p4b\build\bin\n42-datc-bf5.exe` (includes tonight's two fixes,
see §3). Source changes are UNCOMMITTED in the gov5-p4b worktree (branch
`concurrent-datc-root`): `modules/state/commitment/{state_overlay.go,trie_overlay.go,
trie_root_computer.go}`.

**Command** (same one used all night, resume auto-picks up `--start`):
```
C:\N42\gov5-p4b\build\bin\n42-datc-bf5.exe build --src mainnet --out D:/n42-datc-bprime2-25m ^
  --end 25439371 --sched 64,1024,16384,1,4194304,4194304 --window=false --leaf-seg ^
  --map.gb 2048 --dirty.gb 16 --stocache.m 64 --gogc 400 --state-overlay --concurrent-root ^
  --pprof.port 6060
```
Logs: `D:/n42-datc-bprime2-25m/build6.{log,err.log}` (this run; earlier runs tonight are
build1..build5).

**Checkpoint (confirmed)**: stopped cleanly via graceful CTRL_BREAK at **block 12,660,000**
("graceful stop at block 12660000 (committed; spill cut at frame boundary)" in
build6.log). Free system memory jumped to **115.4GB** immediately after exit (was 0.5-3GB
while running) — confirms the epoch-boundary dirty-mark map was the culprit, and it did
release once the process exited (whether via the epoch's own flush or just process exit
reclaiming everything — either way, confirmed transient, not a leak across runs).

**Why stopped**: system free memory dropped to 0.5-3GB repeatedly overnight (DATC working
set peaked ~114GB out of 125.6GB total) while the deep-epoch dirty-mark maps
(`stoDirty[4]/[5]`, e=4,194,304) grew toward the epoch boundary at block 12,582,912. The
build crossed that boundary (~block 12,600,000-ish, visible as a `rb=` counter jump in the
heartbeat log) but memory had not yet visibly dropped by the time the user asked to stop
and prioritize replay-v2. **Not a bug** — expected architecture, see
[[project_datc_stateoverlay_btree_copy_hotspot]] for the full profiling writeup.

**To resume**: same command above (binary + args unchanged); `--start` auto-loads from
`DatcMeta/progress`. Expect a `[datc] resume dirty-mark backfill` pass first (replays
changeset marks from the current epoch's start — cost scales with how far past the epoch
start you resume).

## 2. Tonight's two applied DATC perf fixes (uncommitted, gov5-p4b)

Full writeup + all verification data: [[project_datc_stateoverlay_btree_copy_hotspot]].

1. **`newMergedCursorLive`** (trie_overlay.go) — merged cursors over the 4-table
   StateOverlay btrees iterate the LIVE tree instead of taking a `tree.Copy()` snapshot on
   every open. Applied to BOTH `stateOverlayTx` (16 read-only shard workers) and
   `stateOverlayRwTx` (main-thread writer). Root cause: `Copy()` bumped the tree's own
   isoid on every call, invalidating every existing node and forcing the next `Set()` to
   pay a copy-on-write cascade — this was 44% of ALL process allocations before the fix.
   Required companion fix: `deleteAccountStorage`/`deleteTrieOfStoragePrefix` in
   `trie_root_computer.go` now close their cursor immediately after the collect loop
   (was `defer c.Close()`) — the live cursor holds the tree's RLock until Close(), and
   writing to the same table while it's still open self-deadlocks (caught by
   `TestConcurrentOverlayAccumulation` hanging 10 min during dev).
2. **`overlayLess` u64 prefix fast path** (trie_overlay.go) — compares the first 8 bytes
   as a big-endian uint64 before falling back to `bytes.Compare`; keys are keccak outputs
   so this resolves ~99% of comparisons without the `cmpbody` call.

**A third optimization was attempted and REVERTED**: a "monotone-seek fast path" on
`mergedCursor.Seek` (skip re-seeking a side whose lookahead already covers the target when
seeks are ascending). It produced a deterministic wrong root at mainnet block 687,649 in
the 2M-block A/B gate; root cause was never isolated. Do not retry without (a) counting
serial-fallback heals as a FAILURE in any A/B (the bug was masked by the per-block
gold-check + serial fallback in one bisection run), and (b) a standalone property test of
random ascending Seek/Next sequences vs a reference cursor. See the NOTE comment left in
`trie_overlay.go` on the `mergedCursor` struct.

**Verification gate used for both applied fixes** (repeat this for any future overlay/
cursor change before touching the production build):
```
go test -tags "nosqlite,noboltdb" ./modules/state/commitment/...
go test -race -tags "nosqlite,noboltdb" ./modules/state/commitment/...
# 2M-block A/B: build old-binary vs new-binary to separate throwaway --out dirs, same flags, --end 2000000
# then: build/bin/tabsum.exe <outdir> DatcAccNode DatcStorNode TrieAccount TrieStorage   (rows+sha256 must match byte-for-byte)
# then: <new-binary>.exe verify --out <outdir> --samples 10   (must be 10/10 root-exact)
# then grep -c "croot\|MISMATCH\|fallback" on both build logs — must be 0 on both sides
```

## 3. N42 legacy chain — synced to live, stopped (D:/mainnet)

**Binary**: `D:\mainnet\n42q.exe` (block-only import patch, env `N42_BLOCK_ONLY_SYNC=1`
— see [[project_n42_mainnet_genesis_dual_hash]] for why this exists: HEAD's header-hash
refactor (commit 9d942f6e) can no longer reproduce the real legacy mainnet genesis
`0x138734b7...`, so this is a pre-refactor binary (v5.6.823, commit 951ce8a8) with a
gas/bloom/receipt/state-root-check downgrade patch, importing blocks WITHOUT validating
state (state is rebuilt independently by replay-v2 downstream).

**Command** (`D:\mainnet\n42q.bat`):
```bat
set N42_BLOCK_ONLY_SYNC=1
n42q.exe --data.dir ./mainnet --chain mainnet_compat --p2p.tcp-port 10186 --p2p.udp-port 10185 ^
  --p2p.min-sync-peers 1 --p2p.bootstrap-node "enr:-Je4QGJb6IZbaceKodV55AtTd9oxwiqeVeZ1LwHA9MId-k8XAsKAAyxTi_Pf_nkRTuH-vQnICSLg2Uu_PmG3Vwx8eaIChmFzdEVucoQThzS3gmlkgnY0gmlwhAWhUVqJc2VjcDI1NmsxoQICbV6ssdll4ktSNrWY2FaYTFjio7WqVJLSPNwJMv2SBYN0Y3CCJ2aDdWRwgidl" --log.level info
```

**Result (this session, 2026-07-05 15:22-15:35)**: resumed from block #12,679,790, synced
100% to live tip **#12,973,899**, stopped gracefully (CTRL_BREAK, clean 31-step shutdown).
Datadir: `D:/mainnet/mainnet` (chaindata). Working-set was tiny (~2.85GB) — block-only
import is cheap; the earlier gas-mismatch/state-root bad blocks (~block 11.93M, see
[[project_n42_mainnet_genesis_dual_hash]] §"gas-skip 不够") are already behind this head,
imported header+body only, no state.

**To resume syncing further** (if the user wants to catch up to an even newer tip before
replay): re-run the same `n42q.bat` — it auto-resumes P2P sync from wherever the chaindata
head is.

## 4. replay-v2 — the actual task at hand

**Binary/command**: `cmd/n42` subcommand `replay-v2` (in this repo, N42-gov5; see
`cmd/n42/replay_v2_cmd.go`, backed by `internal/replay/engine_v2.go`). Full re-execution
with JMT/QMDB/BMT/MPT commitment, gap filling, EraE export.

**Planned invocation** (user wants tree=qmdb specifically — "检查树qmdb"):
```
n42.exe replay-v2 --source D:/mainnet/mainnet --target <NEW_TARGET_DIR> ^
  --chain mainnet_compat --tree qmdb --qmdb-history --pprof.port 6062
```
- `--target`: pick a fresh directory (not yet decided at handoff — do NOT reuse
  D:/mainnet/mainnet, that's the source).
- `--qmdb-history`: journals full history (death stamps, key versions, top band) — needed
  if we want any-height proofs / full audit rather than just a final-state check. Consider
  omitting if only checking final-state consensus/EIP correctness (cheaper).
- Memory: this is historically a heavy consumer (JMT/QMDB, `--no-gc` full-history is ON by
  default via `cfg.DisableGC`/`--no-gc` flag defaulting true). Given tonight's OOM scare,
  consider `--no-gc=false` for a lighter run, or verify free memory first
  (`(Get-CimInstance Win32_OperatingSystem).FreePhysicalMemory`).
- `--from`/`--to` default to full range (0 / auto=chain tip). Could restrict to a smaller
  range first as a smoke test before committing to the full ~12.97M-block replay.

**What the user wants checked** (their words): "共识、树qmdb、最新EIP" — consensus
correctness, the QMDB tree specifically, and latest-EIP checkpoints. Concretely:
- `--tree qmdb` exercises the QMDB commitment path specifically (as opposed to jmt/bmt/mpt).
- Output stats file (`replay_v2_stats.json` by default, or pass `--output <path>`) has
  `receiptMatch`/`receiptMismatch`/`txFailed`/`skipReasons` — this is the primary
  consensus-correctness signal. A PRIOR partial run's stats file already exists at
  `C:\N42\N42-gov5\replay_v2_stats.json` (2026-07-03, blocks 1-200,000 only: 267 txFailed
  all `evm_error`, 232 receiptMismatch out of 199,768+232 — this is STALE/small-range data
  from before tonight, not representative of a full run; don't confuse it with a fresh
  result).
- "最新EIP" (latest EIP checkpoints): verify the chain config resolved for
  `mainnet_compat` includes the newest hardfork activations (check
  `params.ChainConfigByChainName("mainnet_compat")` — cross-reference against whatever the
  newest N42-side EIP work was, e.g. EIP-4444/2935/7702 mentioned elsewhere in memory) and
  that blocks past those activation heights replay/verify correctly (no txFailed spike
  right after an activation height would be a red flag).

**Sequencing decision made this session**: user explicitly deferred replay-v2 for
several hours due to a real low-memory situation (DATC's build alone pushed system free
memory to 0.5-3GB repeatedly), then decided to stop DATC and prioritize replay-v2 instead
of continuing to wait. So: DATC is being stopped now (see §1), replay-v2 should be
started once DATC has actually exited and released its ~100+GB working set.

## 4b. replay-v2 LAUNCHED (2026-07-06 02:00, PID 26352)

Command actually used (differs slightly from §4's plan — chose GC-enabled for memory
safety after tonight's OOM scare, and skipped `--qmdb-history` since the ask is
correctness verification, not archival proof-serving):
```
C:\N42\N42-gov5\build\bin\n42.exe replay-v2 --source D:/mainnet/mainnet --target D:/n42-replay-v2-qmdb ^
  --chain mainnet_compat --tree qmdb --no-gc=false --pprof.port 6062
```
Logs: `D:/n42-replay-v2-qmdb.log` (stdout, progress bar) + `D:/n42-replay-v2-qmdb.err.log`
(structured INFO/DBUG lines — this is the one with the useful per-batch stats:
`qmdbRoot`, `receiptOK`/`receiptFail`, `txOK`/`txFail`, `liveKeys`, `twigs`, `heapMB`,
`cacheHitRate`). Stats JSON will land at the default `replay_v2_stats.json` path (cwd of
the launching process) unless `--output` was passed — it wasn't, so check
`C:\N42\N42-gov5\replay_v2_stats.json` when it finishes (this OVERWRITES the stale
2026-07-03 200K-block one already there — that old one is NOT representative, see §4).

First few batches (blocks 1-500,000): 0 receiptFail, 0 txFail, QMDB roots being produced
every ~100K-block batch, ~35-36K blk/s (early sparse-era blocks — will slow down a lot
once it reaches 2017+ dense blocks, same density effect DATC sees).

Monitor: `monitor_replay_v2.sh` (scratchpad), hourly, tails the last `replay progress` /
`batch committed` lines + greps for error/panic/MISMATCH.

## 4c. eth-el minimal snapshot regeneration LAUNCHED (2026-07-06 ~02:20, PID 2972)

Piggybacked on the big free-memory window opened up by stopping DATC (see §1) — this was
the backlogged "minimal snapshot regen, needs >60GB free" item from
[[project_weekly_data_topup]]. Source picked per user's call ("找下最近生成witness的,
或者直接用d:\reth2k" → used reth directly): **D:\reth2k\db**, a real full reth mainnet DB
(confirmed via `n42-reth-head.exe`: `BlockBodyIndices lastKey block = 25439434` — i.e. this
IS current chain tip, not the older H₀=25,188,781 checkpoint the backlog note mentioned;
user explicitly OK'd using whatever reth has now instead of insisting on the old
checkpoint). Table sizes confirmed via `mdbx-table-stat.exe`:
`PlainAccountState=398,785,792 entries (24.0GB)`, `PlainStorageState=1,602,375,128 entries
(132.5GB)` — matches the memory note's "3亿账户+10亿槽" estimate closely.

**Tool**: `cmd/reth-snapshot-export` (built fresh: `build/bin/reth-snapshot-export.exe`).
**Command**:
```
reth-snapshot-export.exe -db D:/reth2k/db -out D:/n42-snapshot -end-block 25439434
```
(reth's real table names `PlainAccountState`/`PlainStorageState` are the tool's defaults —
did NOT pass `-n42`, that flag is only for when the source is an N42-format DB.)

Output: `D:/n42-snapshot/` (accounts.0-25439434.* / storage.0-25439434.* — RecSplit MPHF +
zstd, per the tool's naming). Logs: `D:/n42-snapshot-export.log` (progress) /
`D:/n42-snapshot-export.err.log`.

At launch: phase 0 (counting entries) running, working set only ~1.5GB so far (will grow
once it starts the ETL sort + RecSplit build phases — no ETL-tmpdir flag was found on this
tool, so it presumably sorts in the output dir itself; watch D: free space too, not just
RAM, if it turns out to spill).

Monitor: `monitor_snapshot_export.sh` (scratchpad), hourly, tails last log lines.

**Running concurrently with replay-v2 (§4b)**: both are read-only against DIFFERENT
sources (D:/mainnet/mainnet vs D:/reth2k/db) writing to different targets
(D:/n42-replay-v2-qmdb vs D:/n42-snapshot) — no data collision. Combined working set at
launch was tiny (~24GB) against 89.5GB free; re-check both before assuming this holds once
snapshot-export's RecSplit build phase ramps up (that's historically the memory-heavy
part per [[project_weekly_data_topup]]).

## 4d. mainnet_v2 replay COMPLETED — confirms genesis-time-activation is fundamentally
incompatible with this chain's real history (2026-07-06 03:40)

Result: `receiptMatch=1,969,509 / receiptMismatch=11,004,390` out of 12,973,899 blocks
(~85% mismatch). Expected and uninformative beyond confirming the obvious: `mainnet_v2`
activates Shanghai/Cancun/Pectra/Osaka/Fusaka/Glamsterdam all at genesis, but these real
historical N42 blocks were mined under `mainnet_compat` rules (no Shanghai+) — replaying
them under different EVM rules than they were sealed with is guaranteed to diverge, this
is not a code-correctness signal.

## 4e. Calendar-parity analysis + staggered chainspec (2026-07-06 03:45-03:50)

To get a MEANINGFUL "does N42 correctly implement each EIP" signal, computed the N42-chain
block heights that correspond (by real wall-clock date) to when each fork actually
activated on real Ethereum mainnet, using `hdrtime-probe` (new tool, built this session:
`cmd/hdrtime-probe`, reads `rawdb.ReadBlockByNumber` + `.Time()` from a raw N42 chaindata
dir — usage: `hdrtime-probe.exe <chaindata-path> <blocknum> [<blocknum>...]`, NOTE: pass
the `.../chaindata` subdir, not the parent datadir, and do NOT use Accede mode since no
writer process is attached when running standalone).

**N42 legacy chain genesis**: 2023-03-07 (timestamp 1678174066). Current tip
(#12,973,899): 2026-07-05.

| Fork | Real ETH mainnet date | N42-equivalent (probed) |
|---|---|---|
| Shanghai | 2023-04-12 10:27 UTC | **block 305,000** (block 300,000 probed = 2023-04-12 00:00) |
| Cancun | 2024-03-13 13:55 UTC | **block 3,935,000** (interpolated between 3.9M=03-10 and 3.95M=03-15) |
| Prague/Pectra | 2025-05-07 10:05:11 UTC | **timestamp 1746612311** (time-based field, no block conversion needed — N42 timestamps are real wall-clock) |
| Osaka/Fusaka | ~2025-12-09 (ESTIMATE, moderate confidence — publicly targeted date, not independently verified this session) | **timestamp 1765238400** (same for both — real Ethereum merged Fulu+Osaka into one "Fusaka" fork) |
| Glamsterdam | not yet activated on real Ethereum as of this session | **timestamp 1798761600** (2027-01-01 — deliberately set beyond current N42 tip so it does NOT activate in this replay) |

**New chainspec**: `params/chainspecs/mainnet_v2_staggered.json` (copy of `mainnet_v2.json`
structure with the above staggered values; `ltHashTime`/`beijingBlock` left at genesis like
`mainnet_v2` since those are N42-internal, not real-Ethereum-EIP-timing questions).
Registered as chain name `mainnet_v2_staggered` in **`params/config.go`**
(`ChainConfigByChainName` + `GenesisHashByChainName`, new `MainnetV2StaggeredChainConfig`
var) AND **`internal/genesis_block.go`** (`GenesisByChainName` + new
`mainnetV2StaggeredGenesisBlock()` — copies `mainnetV2GenesisBlock()`'s alloc/timestamp/
miners, only swaps `Config`). **Both registrations are required** — missing the
`genesis_block.go` one was caught immediately (first attempt loaded a near-empty 5-account
fallback genesis instead of the real 2,326-account mainnet alloc, causing spurious
"insufficient funds"/"nonce too high" failures from block ~83 onward; fixed by adding the
second switch case, killed the broken PID 25044 run, rebuilt, relaunched).

**Currently running** (2026-07-06 03:50, PID 13232): `n42.exe replay-v2 --source
D:/mainnet/mainnet --target D:/n42-replay-v2-qmdb-staggered --chain mainnet_v2_staggered
--tree qmdb --no-gc=false --pprof.port 6064`. Genesis now correctly loads (accounts=2326,
same root `8e0be73e13f12b26` as the other two runs — confirms it's genuinely the same
underlying chain data, only the fork-activation schedule differs). Logs:
`D:/n42-replay-v2-qmdb-staggered.{log,err.log}`. Monitor: `monitor_replay_staggered.sh`
(scratchpad, 30-min interval, tracks `receiptFail`/`txFail` growth and counts "tx apply
failed" WARN lines).

**Interpretation caveat for whoever reads the eventual result**: some mismatch in the
transition zones right after each staggered height is EXPECTED and not necessarily a code
bug — the calendar-parity heights are an approximation (interpolated from a handful of
probed blocks, not exact-to-the-second), and the REAL N42 protocol upgrade heights (if
they were ever actually decided/shipped) could differ from "whatever block matches real
Ethereum's calendar date" — this analysis assumes N42 *should* track Ethereum's calendar,
which may not reflect an actual N42 governance decision. Treat a LOW, LOCALIZED mismatch
count right at each transition as expected noise; a HIGH or SUSTAINED mismatch rate deep
into a fork's active window (like mainnet_v2's 85%) would be the real signal of a code bug
in that EIP's implementation.

## 4f. Genesis alloc patch for the 6 known-affected addresses + C: cleanup (2026-07-06 ~04:25-04:30)

Per user decision: instead of root-causing the apos-reward/Shanghai interaction (§4e), patch
genesis balances for the 6 affected addresses so their observed failing transactions pass.
New file `internal/allocs/mainnet_v2_staggered.json` (copy of `allocs/mainnet.json`, 2326→2330
entries): the 2 addresses already present (`0x0Ec245...41D2`, `0xedd50B8a...fB4C1`) got
+50,000 ETH added to their existing genesis balance; the 4 addresses NOT present
(`0xCa3a6d22...09fe`, `0x0E263683...5e153`, `0xA480bCe9...e367F`,
`0x53C80AD2...3bDB3`) got new entries at 50,000 ETH each. Wired via a new
`mainnetV2StaggeredGenesisBlock()` in `internal/genesis_block.go` (see its doc comment for
the full rationale/caveat — **this chain is no longer a faithful balance-accurate replay of
real N42 history, only useful for checking EIP mechanics at a sane activation height**).

**Also this session**: C: drive hit 98% full (9.6GB free) — cleared ~10.4GB of stale
gitignored debug logs from the repo root (`deletes.log` 8.2GB, `audit_nil.log` 1.9GB, plus
various `*_trace.json`/`*_TRACE.json` dumps — all confirmed `git check-ignore`d before
deletion, all dated April-May, unrelated to tonight's work) → C: now ~20GB free. Go build
cache (`C:\Users\10200\AppData\Local\go-build`, 8.2GB) was identified but NOT cleared (still
useful, lower priority once the acute 9.6GB crisis was resolved). **New replay-v2 outputs
now go to E:** (811GB free) instead of D: (943GB free but heavily used by DATC + the two
earlier replay runs + snapshot-export) per explicit user instruction.

**Relaunched** (2026-07-06 04:30, PID 11584): `n42.exe replay-v2 --source D:/mainnet/mainnet
--target E:/n42-replay-v2-qmdb-staggered-hist --chain mainnet_v2_staggered --tree qmdb
--qmdb-history --no-gc=false --pprof.port 6065`. Genesis now loads accounts=2330 (2326+4 new),
root changed (expected — balances changed). At block 900,000 (past the first previously-
failing block 538,482): **0 tx-apply-failed lines** — patch appears to be working; still
need to watch through block 1,324,122 (the last of the 6 known failure points) before
calling it confirmed. Monitor: `monitor_replay_patched.sh` (scratchpad, 30-min interval).

**Memory note**: snapshot-export's RecSplit build over the 1.6B-entry storage table hit
**83.6GB working set** (bigger than the backlog memory's "数十GB" estimate) — free system
memory dropped to 15.3GB at one point. Flagged to user; user chose to keep watching rather
than intervene, and separately decided to sequence DATC's resume AFTER snapshot-export
finishes (§4g) rather than run three heavy jobs at once.

## 4g. DATC resume queued behind snapshot-export (2026-07-06 ~04:35)

Per user request: wait for snapshot-export (PID 2972) to fully finish, THEN auto-resume
DATC bf5 from its checkpoint (block 12,660,000, see §1). Automated via a background script
(`wait_snapshot_then_resume_datc.sh`, scratchpad) that polls for PID 2972 to exit, then
launches the exact same DATC resume command from §1 (binary unchanged: `n42-datc-bf5.exe`,
same flags) with logs going to `D:/n42-datc-bprime2-25m/build7.{log,err.log}` (build6 was
the pre-stop run). **When this fires, the reader picking this up should**: note the new
DATC PID (printed by the script as `DATC_RESUMED_PID=<n>`), re-attach the hourly leaf-
progress monitor (same pattern as `monitor_datc_bf2.sh` used all night — LEAF_TARGET
13325924190, TARGET 25439371), and keep watching the two replay-v2-staggered-hist run
(E: drive) since it'll now be running alongside DATC.

## 5. Immediate next steps (in order)

1. Confirm DATC (PID may differ on next check) has actually exited —
   `Get-Process -Id 24636` should return nothing; check `build6.log` tail for the
   `graceful stop at block N` line to record the final checkpoint.
2. Recheck free memory — should jump dramatically once DATC's ~100GB+ working set is
   released. Confirm there's comfortable headroom before launching replay-v2.
3. Pick a `--target` directory for replay-v2 (not yet chosen).
4. Launch `n42.exe replay-v2 --source D:/mainnet/mainnet --target <dir> --chain
   mainnet_compat --tree qmdb ...` — likely a multi-hour run for ~12.97M blocks; consider
   backgrounding + a progress monitor (mirror the `monitor_datc_bf2.sh` pattern used all
   night: poll a stats/log file, report hourly, watch memory).
5. Once complete, inspect `replay_v2_stats.json` (receiptMatch/receiptMismatch/txFailed/
   skipReasons) + spot-check EIP-boundary blocks + confirm the QMDB root check the tool
   reports (look for whatever pass/fail line `internal/replay/engine_v2.go` prints at the
   end — read that file if the exact success/failure log format needs confirming).

## References
- [[project_datc_stateoverlay_btree_copy_hotspot]] — tonight's full DATC perf investigation
- [[windows-graceful-stop-ctrlbreak]] — how to stop native Go processes safely
- [[project_n42_mainnet_genesis_dual_hash]] — why the legacy chain needs a patched binary
- [[project_datc_m2_bprime_design]] — the B′ architecture DATC is building
