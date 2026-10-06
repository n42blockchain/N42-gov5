# S72: QMDB apply parallelism, and the 128 ms "unaccounted" inside proc

Code-reading + package-level benchmarks only (no fleet rounds). Answers two
design questions left open by docs/QS_EXECUTOR_PARALLELISM.md and
docs/QS_BLOCK_TIME_BUDGET.md 6h/6ca: the follower import cycle is
proc 554 ms (recov 26, exec 255, finalize/applyMVSToIBS 145, 128
unaccounted) + write 214 ms, of which QMDB's `ComputeRoot` apply is 99.8%
and runs on one goroutine over one live tree.

## 1. QMDB apply parallelism

**The fleet's root computer does not use the sharded tree today.**
`QMDBRootComputer` is built with a plain, unsharded `Tree`:

- `modules/state/commitment/qmdb_root_computer.go:314` — `return
  &QMDBRootComputer{t: qmdb.New()}` (not `qmdb.NewSharded(n)`).
- `grep -rln NewSharded|ShardedTree` outside `_test.go` turns up nothing in
  the commitment/root-computer path; the only non-test hits
  (`internal/replay/engine_v2.go`, `lib/kv/layered/*`) are an unrelated
  `layered.NewShardedCache`. `ShardedTree` (`lib/qmdb/sharded.go`) exists,
  has this week's tests, and is wired to nothing in production. There is
  **no config/env switch that selects it** — this is not a config-only
  round, it would be new code on the write path.

**Apply today, inside the single `Tree`, is a sequential Go loop, not a
goroutine fan-out.** `Tree.ApplyOps` (`lib/qmdb/batch.go:131`) prefetches,
then runs `for i := range ops { t.Set(...) / t.Delete(...) }` single-threaded
— each `Set`/`Delete` mutates shared, unsynchronized state (the flat index,
`nDirtyTwigs`, `batchTouched`) that only one goroutine may touch at a time.
That is what `ComputeRoot` (`modules/state/commitment/qmdb_root_computer.go:807`,
`r.t.ApplyOps(ops)`) pays for the 214 ms write-phase apply, under
`r.readers.Lock()` (`qmdb_root_computer.go:762`) — a QMDBRootComputer-level
mutex, not an MDBX lock: it exists to keep `ComputeRoot`/flush/evict/reload
mutually exclusive on this one `Tree`, and today it is uncontended (the
write path calls it alone), so it is not itself the serializer. The real
serializer is structural: ops are applied one at a time into ONE tree's
unsynchronized index + twig array.

**The "fold" half is already parallel, and is not the bottleneck.**
`Tree.Root()` → `recomputeDirtyTwigs()` (`lib/qmdb/qmdb.go:718`) fans dirty
twigs out across `runtime.GOMAXPROCS(0)` goroutines (`ParallelRoot = true`
by default) — Blake3 twig hashing is already per-twig independent work done
concurrently, then folded through the small upper tree
(`updateUpperPath`/`rebuildUpper`, `qmdb.go:659-677`), which is cheap
(O(log twigs) per dirty twig). The 214 ms is dominated by the
**apply loop** (index lookups, leaf writes, liveness-bitmap flips,
prefetch misses) — the part `ApplyOps` runs serially — not by hashing.

**Blake3 twig hashing is independent per shard until a final combine** —
confirmed by `ShardedTree.Root()` (`lib/qmdb/sharded.go:216-225`): each
shard's `Root()` is computed independently (cheap when already folded) and
the world root is a 4-level (for 16 shards) binary `hashNode` fold over the
shard roots. `ShardedTree.ApplyBatch` (`sharded.go:184-211`) already does
exactly the fan-out this question asks for: partition ops by the top bits
of `keyHash` into disjoint per-shard slices (zero shared mutable state) and
run each shard's `Tree.ApplyOps` on its own goroutine via a persistent
spin-then-park worker pool (`shardPool`, `sharded.go:44-131`, built to avoid
per-block goroutine spawn/wake cost).

**Minimal change, if adopted:** swap `qmdb.New()` for `qmdb.NewSharded(n)`
in `QMDBRootComputer.New` (`qmdb_root_computer.go:314`) and route
`ComputeRoot`'s existing `ops []qmdb.Op` (already sorted by keyHash) through
`ShardedTree.ApplyBatch` instead of `r.t.ApplyOps(ops)`
(`qmdb_root_computer.go:807`). Everything downstream of the `Tree` interface
(`FlushTo`, `LoadFrom`, `GetProof`, undo recording) already has sharded
equivalents (`sharded.go:289-345` for flush/load; `ShardedProof` for
`GetProof`). What must stay ordered: nothing extra beyond what `ApplyBatch`
already preserves — relative op order within a shard (stable partition) is
all determinism requires; cross-shard interleaving does not affect the
root. **This changes the root value** — `sharded.go`'s own doc comment:
"a sharded root is NOT equal to a single Tree's root over the same
history — opt in for new chains only." This is a consensus-format change
(new genesis / new chain only), not a drop-in for the live n42 chain: it
cannot be A/B'd on the existing fleet without a hard fork of the commitment
scheme.

**Benchmark numbers** (`TestShardedScaling`, `lib/qmdb/sharded_test.go:138`,
run `nice -n 15 GOMAXPROCS=16 go test ./lib/qmdb/ -run TestShardedScaling -v`,
200 blocks x 2000 ops, 16 cores):

| shards | upd/s   | vs single-tree speedup |
|--------|---------|------------------------|
| 1 (single Tree) | 1.52M | 1.0x |
| 4      | 2.96M   | 1.9x |
| 16     | 6.43M   | 4.2x |
| 64     | 7.00M   | 4.6x (diminishing past 16) |

Applied to the 214 ms write-phase apply, 16 shards (4.2x) projects to
roughly **214 ms -> ~50 ms** (a ~164 ms saving), with 64 shards buying only
another ~7 ms over 16 for 4x the partition bookkeeping — 16 is the sane
default for this core count. This is a prediction from an isolated
microbenchmark (bare `Op` application, no MDBX flush, no index persistence,
no live-tree readers lock contention); the real write path also carries
`FlushTo`/index maintenance that the sharded path has but has not been
measured end-to-end under the fleet's block shape.

## 2. The 128 ms "unaccounted" inside proc

Reading `internal/blockchain.go`'s "blockimport phases" log
(`internal/blockchain.go:2580-2627`): `proc` = `dProcess` = wall time of the
whole `Process`/`ProcessParallel` call; `recov`/`prep`(unused in the
log line)/`exec`/`root`(labelled `finalize` in the budget doc) come from
`procPhases = sp.LastPhases()` (`internal/blockchain.go:2473-2475`), gated
on `!bc.parallelEVM`.

**Top finding: under `bc.parallelEVM` (the fleet's actual config),
`procPhases` is never refreshed for the block being logged.**
`ProcessParallel`/`runParallel` (`internal/parallel_processor.go`) has its
own `tStart`/`tRecovered`/... timers and its own `log.Info("parallel
block", ...)` line (`internal/parallel_processor.go:637-639`), but it never
writes to `StateProcessor.lastPhases` — only the serial `Process`
(`internal/state_processor.go:226,241,308,313,328-329`) does that. So on a
parallel-EVM block, the `recov`/`exec`/`root` fields printed in
"blockimport phases" are **stale**: whatever the last serial-path call left
behind (rare now — only tiny blocks ≤4 txs or fee-recipient-touching
blocks fall back to `p.Process`, per `ProcessParallel`'s early `touchesAny`
check at `internal/parallel_processor.go:203-206`). Reading 554 ms total
against a 26/255/145 breakdown that belongs to a different, earlier block
is comparing two different things; the "128 ms unaccounted" is largely an
artifact of this wiring gap, not a genuine unmeasured cost inside this
block's `proc`.

**Where the real time actually goes, from `runParallel`'s own (separately
logged, not surfaced into blockimport phases) phases**
(`internal/parallel_processor.go:233-639`):

1. **Worker/executor setup — `setupMs`+`blockStartMs`+`executorMs`**
   (`tRecovered`→`tRunStart`, lines 271-444): one `p.bc.ChainDB.BeginRo`
   MDBX read transaction PER WORKER, plus a fresh `ParallelStateReader`/
   `ParallelStateWriter`/`IntraBlockState`/`EVM` per worker
   (`internal/parallel_processor.go:320-358`), built fresh every block by
   design (comment at line 311: per-tx allocation was 46% of executor CPU
   in round 35g, so the fix moved allocation to per-worker/per-block —
   it did not eliminate it). With `N42_PARALLEL_WORKERS` scaling up, this
   setup cost scales with worker count the same direction `exec` does, so
   it is easy to mistake for exec time when only `proc`/`exec` are read.
2. **Wave collect — `collectMs`** (`tRunEnd`→`tApplyStart`, line 639):
   the gap between the executor finishing its waves and
   `applyMVSToIBS` starting — validating/collecting the final MVS state
   before the single-threaded replay into `ibs`.

Both are plausible double-digit-ms items at the fleet's ~163k-tx block
size (worker count and MVS size respectively drive them), but neither has
a number attached without a live log — the "parallel block" line already
carries `setupMs`, `blockStartMs`, `executorMs`, `collectMs`, `applyMs`,
`prefetchMs`, `finalizeMs` per block; the fix for the 128 ms question is to
**read that line instead of blockimport phases** for parallel-EVM blocks,
or to make `ProcessParallel` populate `StateProcessor.lastPhases` (mapping
`recoverMs`->Recover, `execMs`+`validateMs`->Exec, `applyMs`+`finalizeMs`->
Finalize, and adding a new bucket for `setupMs+blockStartMs+executorMs+
collectMs+prefetchMs` that today has no home in `ProcessPhases` at all)
so `blockimport phases` stops printing stale numbers.

## Ranked rounds

1. **(code change, no replay-bench gate needed — pure logging fix)
   Wire `ProcessParallel` into `StateProcessor.lastPhases`.** Add a
   `ProcessPhases`-shaped struct (or widen the existing one with a
   `Setup`/`Collect` bucket) populated at the end of `runParallel` from the
   timers it already computes, published the same way `Process` does
   (`p.lastPhases.Store(&phases)`). Zero consensus risk (pure observability,
   touches no state), makes `blockimport phases` stop lying on every
   parallel-EVM block, and directly resolves question 2 with per-block
   numbers instead of estimates. **Prediction: no throughput change** (it
   changes what gets logged, not what runs) **but it correctly reclassifies
   the mislabeled 128 ms** — expect the true split to land close to
   `recov`~recoverMs, `exec`~execMs+validateMs+collectMs,
   `finalize`~applyMs+prefetchMs+finalizeMs, with `setupMs+blockStartMs+
   executorMs` (worker/tx/reader allocation) as the newly-visible bucket,
   plausibly 40-80 ms of the 128 given it scales with worker count the way
   the budget doc says `exec` does.
2. **(code change, blast radius = new chain only, needs its own replay-bench
   gate — NOT A/B-able on the live fleet)** Shard `QMDBRootComputer`'s
   live tree via `ShardedTree` (16 shards). Changes the on-disk root for
   any chain that adopts it; cannot run beside today's unsharded chain in
   the same cluster, so it is validated on a fresh `--chain` the same way
   QMDB itself was bootstrapped, with a full replay-bench gate (not a
   same-config A/B) before any claim of a fleet-wide write-phase saving.
   **Prediction: write-phase apply 214 ms -> ~50 ms (per the 4.2x/16-shard
   microbenchmark), ~164 ms off the follower's write phase** — this is the
   single largest remaining lever in the budget doc's own ranking (the
   214 ms apply is 99.8% of write), but it is the deepest reach because of
   the root-format break.
3. **(investigation only, no code yet)** Profile `setupMs`/`collectMs`
   directly on a quiet box (package-level, not a fleet round) to see
   whether per-worker `BeginRo` transaction cost can be amortized (e.g. a
   warm pool of read transactions reused across blocks, the same instinct
   that produced `shardPool`'s persistent workers) before committing to
   round 1's exact bucket boundaries.

Register round 1's prediction first (config-free, reclassify only): **blockimport
phases' `exec`/`finalize` numbers on parallel-EVM blocks will move to reflect
real per-block values instead of staying frozen at the last serial-path
block's numbers, and a new `setup`/`collect` bucket will appear accounting
for an estimated 40-80 ms of today's 128 ms "unaccounted" gap — no
throughput change expected.**
