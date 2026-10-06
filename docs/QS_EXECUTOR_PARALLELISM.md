# S71: why the executor is only ~9% of CPU and the import cycle is still ~550 ms

2026-10-02. Scope: the design question registered for S71 — the leader's
fillTx (~600 ms for ~86k tx) and the followers' import proc (~550 ms) cost
~6.5 us/tx wall, while n42-rs executes the same blocks in 83-96 ms (~1
us/tx) on the same box, and the executor itself is only ~9% of a node's
CPU (6fd). So the gap is parallel efficiency, not raw CPU.

## 0. The measurement this spec asked for did not run

The plan was to vary `N42_PARALLEL_WORKERS` (1/2/4/8/16/32) on the qs-replay
single-node bench and read per-block phase lines plus a CPU profile at 16
workers. That could not be done this round: `qs-replay` requires the
reflink-copied datadir to unwind cleanly through the QMDB undo window
(`AlignAppliedBranch` -> `ensureQMDBTreeAtParent`), and on the current
`/data/blockchain/qs-node0` — reseeded by the harness earlier today — the
live QMDB tree's root does not match ANY header within the 512-block
`qmdbRealignMaxDepth` walk back from the stored head, at every depth tried
(60 and 256 blocks, both failing on the very first replayed block with
`tree root ... matches no header within the undo window`). This is not a
worker-count effect — depth=1 worker and depth=32 workers both failed
identically before a single block re-executed — and it is not a running-node
conflict (no `qs-node*` process was found running; `ps aux` was checked
before touching the reflink copy). The mechanism (`internal/blockchain.go`
`AlignAppliedBranch`/`ensureQMDBTreeAtParent`, ~line 3159-3270): a reseed
that bulk-loads the QMDB tree outside the normal block-by-block apply path
leaves the tree with no ancestor the replay tool's undo window can find,
so `qs-replay` cannot establish its starting state on this datadir. The
reflink copy and scratch directory were deleted; nothing in this analysis
mutated the live fleet datadirs.

**Consequence**: sections 1-3 below use the measurements that already
exist on record (6fd/6h/6i in `docs/QS_BLOCK_TIME_BUDGET.md`, and
`memory/project-qs-offline-replay.md`'s r4/r5 uncontended replay), not a
fresh worker-count sweep. The worker-count speedup curve (1/2/4/8/16/32)
and the 16-worker CPU-profile breakdown this spec asked for are **not
available** and should be the first thing re-run once a datadir produced
by normal block-by-block import (not a harness reseed) is available —
flagged as the next step in section 4.

## 1. What is already measured, assembled into one picture

Two independent readings of the same ~163k-tx / ~86k-tx-class block, both
cited rather than re-derived:

**A. Uncontended single-node replay** (memory/project-qs-offline-replay.md,
r4/r5, 101 full 163k-tx blocks, `N42_PARALLEL_WORKERS` at its 32 default):
body 10, proc 550 (recover 155, setup 32, exec 160, finalize 162), valid 30,
write 157 ms; total 720-790 ms wall, 4.5 us/tx.

**B. Steady-state fleet-shape replay** (QS_BLOCK_TIME_BUDGET.md 6ca,
import_breakdown.py, n=1896, no stall): body 10, proc 554 (recov 26, exec
255, finalize 145), write 214, total 803 ms.

The two differ in recov (155 vs 26 ms) because of overlap scheduling
(2402295b overlaps recovery with execution — 6i), not because the work
disappeared: 6h/6i's 45 s CPU profile puts secp256k1 sender recovery at
**54-64% of a node's total CPU**, same order in both the fleet round (6fd:
54.15-54.21%) and the earlier profile (6i: 63.69% cum, pool + import
combined). The "16-26 ms" recov line in a phases log is wall time on the
critical path, not CPU consumed — most of that CPU is paid concurrently
on other goroutines and only shows up as a wall-clock cost when the
executor is actually blocked waiting for a sender.

**C. What IS parallel today, and what is not**, read directly against the
code (not re-measured this round):

| phase | ms (B, steady state) | parallel across `N42_PARALLEL_WORKERS`? | mechanism |
|---|---|---|---|
| body (decode) | 10 | no | single goroutine, `cmd`/import path |
| recov (sender recovery, visible slice) | 26 | partially | `internal.recoverSenderStride` is sharded, but is sized from GOMAXPROCS, **not** `N42_PARALLEL_WORKERS` (memory/project-qs-offline-replay.md, round 35zd: "recover is sized from GOMAXPROCS (37->28 workers)... nothing moved") |
| exec | 255 | yes | `StateProcessor.runParallel` / Block-STM workers, `internal/parallel_processor.go:231-561`, worker count = `parallelWorkers()` |
| finalize (`applyMVSToIBS`) | 145 | **no** | single-threaded two-pass replay of the validated MVS into the real `IntraBlockState` (`internal/parallel_processor.go:728-800+`) — collects every `LocationKey` from the whole block, then applies accounts/wipes/storage/code sequentially. This is NOT the QMDB "fold" from 6h (different structure, same word) |
| write (QMDB apply+root) | 214 | **no** | `ComputeRoot`'s own split (6h, round 22): apply 56.5 ms / fold 0.1 ms at a median 16,077 ops, **99.8% in `apply`**, which walks a single binary tree (`lib/qmdb`) one entry at a time — no worker-count lever touches it; it is the live tree receiving the already-validated MVS writes, done once, serially, because there is one tree |

So of the 554 ms `proc` + 214 ms `write` = 768 ms total (excluding body),
roughly:

- **exec (255 ms, 33%)** is the only phase `N42_PARALLEL_WORKERS` actually
  scales.
- **finalize (145 ms, 19%) + write (214 ms, 28%) = 359 ms, 47%** is
  single-threaded by construction, today, regardless of worker count:
  `applyMVSToIBS` is one goroutine replaying one block's worth of
  `LocationKey`s, and `ComputeRoot.apply` is one goroutine walking one
  QMDB tree.
- **recov (26 ms visible, but 54-64% of total node CPU)** is sharded
  internally but NOT governed by the worker-count knob this spec was
  built to sweep, and most of its cost is hidden by overlap rather than
  removed.

## 2. What fraction of the ~550 ms (proc) is inherently sequential today

Using reading B (`recov 26, exec 255, finalize 145`, proc 554 — the 128 ms
unaccounted-for in that phases line, `554 - (26+255+145) = 128`, is schedule/
validation/setup overhead inside `runParallel` not broken out by the
existing phases line, and must be assumed non-parallel until it is
profiled directly):

```
sequential-by-construction = finalize (145) + unaccounted (128) = 273 ms
parallel today             = exec (255) ms
partially-parallel         = recov (26 ms visible / much larger hidden CPU)
```

**Sequential fraction of `proc` alone: (554 - 255) / 554 = 54%.**
Adding `write` (214 ms, also sequential) against the full
`proc + write = 768 ms` import-critical-path cost:
**sequential fraction of the full cycle: (554 - 255 + 214) / 768 = 67%.**

Both numbers are upper bounds on what more `N42_PARALLEL_WORKERS` can buy:
Amdahl's law on a 67%-sequential, 33%-parallel split caps the achievable
speedup from infinite workers at **1 / 0.67 ≈ 1.5x** over the current
single-digit-worker exec time, nowhere close to closing a 6-8x gap to
n42-rs's ~1 us/tx. This matches 6fd's own finding that the executor is
only ~9% of CPU: the lever this spec was built to sweep (worker count)
only ever touches the smallest of the three serial-or-parallel buckets.

## 3. Which single component would have to change to get under 200 ms

Not `exec` — it is already the parallel piece, already ~255 ms at the
current worker count, and Amdahl's law says shrinking it further (even to
zero) leaves `finalize (145) + write (214) + unaccounted (128) = 487 ms`,
still over 200 ms on its own.

**The write path — `ComputeRoot`'s single-tree `apply`.** It is the
largest single serial cost (214 ms of the write phase, of which 6h
measured 99.8% is `apply`, not `fold`), and it is serial for a structural
reason the exec phase is not: there is one live QMDB tree, so every
validated write must land on it through one goroutine to keep the tree
consistent. The two candidate changes, in order of how directly they
attack this:

1. **Shard the live tree.** QMDB is a binary tree over a twig forest
   (6h); if the forest can be partitioned by address/key range across N
   independent subtrees with an upper combining root (the same shape
   Block-STM already uses to partition read/write sets), `apply` becomes
   N parallel single-tree applies plus a cheap combine, the same way
   `exec` is already parallel. This is the only change that touches the
   214 ms directly; it is also the most invasive (changes the on-disk
   forest layout QMDB persists, affects every reader of
   `lib/qmdb`/`modules/state/commitment/qmdb_*`, and needs its own
   correctness pass before being proposed as a lever — not attempted
   here).
2. **Overlap `applyMVSToIBS` (145 ms) into the parallel exec phase**
   instead of running it after: since `applyMVSToIBS` only needs the
   validated MVS (available once Block-STM's last validation pass
   clears), restructuring it as a pipeline stage that starts consuming
   already-validated keys while the last few transactions are still
   executing would hide some of the 145 ms behind `exec`'s 255 ms, the
   same overlap trick 2402295b already used for `recov`. This does not
   reduce total CPU (6i's lesson: overlap hides, it does not remove) but
   would cut wall time on the critical path, which is what `proc` reports.

Either change targets the 359 ms (finalize + write) that no amount of
`N42_PARALLEL_WORKERS` tuning reaches; (1) is the one that would actually
need to land to get under 200 ms, since (2) alone caps out at hiding at
most ~145 ms behind `exec`'s already-shorter 255 ms window, leaving `write`
(214 ms) exposed on its own.

## 4. Next step (blocked on this round)

Re-run the worker-count sweep (1/2/4/8/16/32, `GOMAXPROCS=32`) and the
16-worker CPU profile this spec originally asked for, once a replay-capable
datadir exists — either a reflink copy taken right after a normal fleet
round (not a harness reseed) or a `qs-replay`-specific fixture seeded by
the normal block-by-block import path. That run should directly measure:
(a) the exec-phase speedup curve across worker counts (expected to plateau
well before 32, per 6fd's 9% CPU-share finding), (b) whether the 128 ms
"unaccounted" gap in section 2 is scheduler/validation overhead in
`internal/parallel/block_stm_scheduler.go` or measurement granularity in
the existing phases line, and (c) a direct `apply`-vs-`fold` split at the
write phase's actual worker count to confirm section 3's reasoning against
fresh numbers instead of round 22's (6h, 2026-09-04).
