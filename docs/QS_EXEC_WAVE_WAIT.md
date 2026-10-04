# S80: what the Block-STM workers do during the exec wave (round 35zzzbf)

Node3-only diagnostics for round 35zzzbf. Trigger: node3's first "parallel block"
line with txs >= 100000. Captures: 103 goroutine dumps per leg (`?debug=1`,
25 ms apart, 2.575 s span starting at the trigger), plus a 30 s CPU profile
per leg. Data in `/data/blockchain/gov5-work/scratch/s80/`
(`node3-B1-gor-NNN.txt`, `node3-B2-gor-NNN.txt`, `node3-B1-cpu.pprof`,
`node3-B2-cpu.pprof`). Binary for symbols: `/data/blockchain/bin/n42-r110`.

## DIAG wiring post-mortem (why mutex/block pprofs came back empty)

The round log confirms node3's `n42.log` does NOT show contention profiling
enabled, and `node3-B1-block-t0.pprof` / `t1.pprof` are ~690 bytes (empty,
profiling never turned on). The intended wiring:

- `bench-7node.sh` (~line 79-87): `if [[ $i -eq 3 && -n
  "${QS_NODE3_CONTENTION_DIAG:-}" ]]; then N42_CONTENTION_DIAG=1
  qs_launch_node ...`
- `run-r35zzzbf.sh` (~line 386): `export QS_NODE3_CONTENTION_DIAG=1`

`export` in `run-r35zzzbf.sh` only reaches children of that shell's own
process tree. `bench-7node.sh` launches each node's `n42` process through
`qs_launch_node`, which (per the rest of the fleet scripts) backgrounds the
process with `setsid`/`nohup` and, for remote legs, goes over `ssh` to the
node's own host. Both of those are environment boundaries that drop
ordinary shell-exported variables unless the variable is explicitly
forwarded: `ssh host "VAR=$VAR cmd"` or `ssh -o SendEnv=VAR`, and a bare
`setsid cmd &` does inherit the parent's env — so the first suspect is not
`setsid` itself but whether `bench-7node.sh` is invoked as a *new* process
(e.g. via `ssh` to node3's host, or via `sudo -H` which by default resets
`$PATH`/env unless `sudo -E` is used) rather than sourced/forked in-process
from `run-r35zzzbf.sh`. The goroutine dumps and the CPU profiles (which do
NOT require `N42_CONTENTION_DIAG`, only the standard `/debug/pprof`
endpoint) landed fine, which proves node3's pprof HTTP server was reachable
and the process came up — only the env-gated mutex/block profiling flag
failed to arrive.

**One-line fix proposal**: in `bench-7node.sh`, replace the conditional
`N42_CONTENTION_DIAG=1 qs_launch_node ...` (a value set in the parent shell
that must survive a process boundary) with an explicit forward at the
`qs_launch_node` call site — e.g. `env N42_CONTENTION_DIAG=1 qs_launch_node
...` if `qs_launch_node` execs locally, or `ssh "$NODE3_HOST"
"N42_CONTENTION_DIAG=1 qs_launch_node ..."` if it goes over ssh — and add a
one-line post-launch check (`curl -s node3:PORT/debug/pprof/mutex | wc -c`)
to the run script so a future round fails loud instead of shipping empty
690-byte pprofs.

## Method

For each of the 103 dumps per leg: grep for frames under
`internal/parallel` (`Executor.Run`, `executeParallel`, `executeParallel.func1`
worker closures, `executeSingle`) and `internal.parallelApplyTx` /
`internal.(*StateProcessor).runParallel`. Classify every matched goroutine's
leaf frame into: (a) running/runnable in EVM or state code, (b) blocked on
`sync.Mutex`/`RWMutex`/`semacquire` (naming the lock owner), (c)
syscall/cgo, (d) chan receive/select (idle), (e) GC assist/other. Cross-check
against `go tool pprof -focus=internal/parallel|parallel_processor` on the
CPU profile, and separately look at the CPU profile's unfocused top-10 to
see what dominates CPU when the executor is *not* the focus.

## B1 distribution (worker-state tally across 103 dumps, 25 ms apart)

| Dump range | # dumps | parallel-frame count | dominant leaf state |
|---|---|---|---|
| 001-025 | 25 | 0 | no executor goroutine exists (idle validateWorker pool, metrics, netpoll) |
| 026-031 | 6 | 2-47 | **running** — `executeSingle` -> `parallelApplyTx` -> `TransitionDb` / `IntraBlockState.FinalizeTx` / `PrepareAccessList` / `AsMessage` |
| 032-094 | 63 | 0 | no executor goroutine exists |
| 095-103 | 9 | 2-17 | **running** — same stack family as above |
| (total) | 103 | 15 dumps non-zero | — |

Only 15 of 103 dumps (~14.6%) catch any `internal/parallel` frame at all,
clustered into two bursts (~150-225 ms wide each) separated by ~1.6 s of
nothing. In every one of those 15 dumps the matched goroutines are
**running**, not blocked: lock frames (`semacquire`/`Mutex.Lock`/`RWMutex`)
appear 0-4 times per active dump and never rooted under `internal/parallel`
or `MVS` — they belong to unrelated subsystems (libp2p, metrics). No
goroutine was ever caught in `for txIndex := range work` (the worker's idle
channel-receive) or inside `wg.Wait` with a visible worker pool sitting
idle — because `executeParallel` spawns the worker goroutines fresh per
wave and they exit when the wave's `wg.Wait()` returns, so between waves
there is no persistent pool to be idle in: the goroutine profile simply
shows *zero* executor goroutines, not an idle one.

## B2 distribution

B2's 103 dumps show **zero** `internal/parallel` frames across the entire
2.575 s capture. Either the capture window missed node3's exec wave for
that block height entirely, or (more likely, given the CPU profile below)
node3 spent that window off the exec path altogether, dominated by qmdb/mdbx
reads.

## Time series (burst vs. between-wave)

- B1: active (worker-stack-present) dumps = 15/103 (~14.6%) in two short
  bursts; everything else (85.4%) has no executor goroutine.
- B2: active dumps = 0/103 (0%).

If the 2.575 s window is treated as covering (roughly) one full parallel
exec wave plus quiescent gaps either side, B1's own wave activity occupies
well under a quarter of the capture and B2's capture missed it outright —
consistent with a genuinely short wave (hundreds of ms) bracketed by much
longer gaps that are not spent inside the executor at all.

## CPU profile cross-check

`node3-B1-cpu.pprof` (268 KB, 30 s, 433.63 CPU-s aggregate across cores),
focused on `internal/parallel|parallel_processor`:

```
flat  flat%  cum    cum%   frame
8.42s 1.94%  8.42s  1.94%  sync/atomic.(*Int32).Add
3.14s 0.72%  14.68s 3.39%  internal/parallel.(*MVS).ReadAccount
1.81s 0.42%  24.81s 5.72%  internal.(*StateTransition).TransitionDb
1.44s 0.33%  49.76s 11.48% internal/parallel.(*Executor).executeSingle
0.81s 0.19%  11.90s 2.74%  internal/parallel.readValid
```
Total parallel-executor cost: ~37.4s / 433.6s = **8.6%** of sampled CPU
time. No mutex/lock symbol appears in the top 20 focused nodes; the cost is
entirely in read (`MVS.ReadAccount`, `readValid`, `mapaccess2`) and EVM/state
transition work — i.e. real, useful execution, not blocking.

`node3-B2-cpu.pprof` (27 KB, 30 s, 32.37 CPU-s, note the much lower sample
count — this leg was far less busy), unfocused top-10:

```
flat   flat%  cum    cum%   frame
27.13s 83.81% 27.46s 84.83% runtime.cgocall
0.96s  2.97%  0.96s  2.97%  lib/qmdb.compressNodes16AVX512
0.12s  0.37%  4.46s  13.78% lib/qmdb.(*Tree).loadTwigFrom
0.10s  0.31%  1.41s  4.36%  mdbx-go/mdbx.(*Cursor).Get
```
84% of B2's sampled CPU is `cgocall` underneath qmdb's `Tree.loadTwigFrom`
and mdbx cursor reads — i.e. this profile's window was dominated by QMDB
read I/O (through cgo), not by the Block-STM executor at all. This is
consistent with B2's goroutine dumps catching zero executor frames: the
sampled window simply fell in a read-bound phase of the pipeline, not in an
exec wave.

## Verdict

Of the three S80 hypotheses (lock-bound / read-bound / single-goroutine
handoff): **read-bound**, with a caveat that the "exec wave" itself (B1) is
short and lock-free — its own 8.6% CPU slice is dominated by
`MVS.ReadAccount` / `readValid` (reads), not by mutex frames, so within the
wave the workers are doing useful read+execute work, not waiting on each
other. The bigger finding is that for most of the capture window (85%+ of
dumps, and 84% of B2's CPU time) node3 is not inside the parallel executor
at all — it is blocked in cgo/mdbx/qmdb page reads (`Tree.loadTwigFrom`,
`mdbx.Cursor.Get`). There is no evidence of a single idle goroutine holding
up a parked worker pool (no `for txIndex := range work` channel-receive
stacks were ever caught), so the "single-goroutine handoff" hypothesis is
not supported by this data.

**Dominant stack (verbatim, trimmed), B1 active window:**
```
github.com/n42blockchain/N42/internal/parallel.(*Executor).executeSingle+0x164   internal/parallel/executor.go:434
github.com/n42blockchain/N42/internal/parallel.(*Executor).executeParallel.func1.2+0x37e  internal/parallel/executor.go:385
github.com/n42blockchain/N42/internal/parallel.(*Executor).executeParallel.func1+0x367    internal/parallel/executor.go:389
```
underneath it, by-dump leaf varies across `IntraBlockState.FinalizeTx`
(`modules/state/intra_block_state.go:1296`), `IntraBlockState.Reset`
(`:522`), `parallelApplyTx` (`internal/parallel_processor.go:674,688`),
`StateTransition.PrepareAccessList` (`internal/state_transition.go:557`).

**Dominant stack, B2 (off the executor path entirely):**
```
github.com/n42blockchain/N42/lib/qmdb.(*Tree).loadTwigFrom   (cum 4.46s / 13.78%)
runtime.cgocall                                               (cum 27.46s / 84.83%)
```

## Fix proposal

The exec wave itself is not the bottleneck (8.6% CPU, read-dominated, no
lock contention observed); the time lost is in the non-exec majority of the
window spent on QMDB twig loads via cgo. Prefetch/pre-warm the block's
touched twigs (hypothesis (b) from S80's own framing) ahead of
`executeParallel` so `loadTwigFrom`'s cgo calls land before the wave starts
rather than serializing with it, and re-run the node3-only DIAG capture with
the `bench-7node.sh` env-forwarding fix above so the mutex/block deltas can
confirm or rule out lock contention directly instead of by absence.
