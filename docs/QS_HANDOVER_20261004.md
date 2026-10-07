# QS handover, 2026-10-04 (America/New_York)

Pause point for the fleet-TPS campaign (goal: approach ../n42-rs, ~280k TPS
on this box). Resume from here; the board is docs/QS_QUEUE.md (rows S60-S82).

## Standing result

| Item | Value |
|---|---|
| Best B (two 60-s windows, B legs) | **146.5k** (35zzzba, 2026-10-03, clean box) |
| Same-config band | 136-146k (noise floor 3.6%) |
| A legs (small blocks) | ~70k |
| Config | lineage binary (r109..r112, same behaviour), QMDB evict lag 4, sender cache 16M, block cache 16, catch-up grace 2000 ms, GOMEMLIMIT 11GiB, interval 250 ms, 32 workers, 8 generators @20000, tenure 8 |

## What the last four rounds established

1. **S78 (35zzzbd)** -- 32 vs 64 executor workers at pacing 125: no gain
   (139.1k vs 140.2k, exec p90 -10%). 7 nodes x 32 workers already fill
   224 of the box's 256 threads. docs/QS_WORKERS_64.md.
2. **S79b (35zzzbe)** -- live CPU profile of follower node3 on full blocks:
   the Block-STM exec wave is nearly CPU-free (parallelApplyTx 5.3%,
   ProcessParallel subtree 0.7% of 374 CPU-s); 53% of the node's CPU is
   ecrecover in internal/ingest hintWorker (background, off the vote
   path). docs/QS_EXEC_WAVE_PROFILE.md.
3. **S80 (35zzzbf)** -- goroutine sampling, weak evidence for "read-bound".
   docs/QS_EXEC_WAVE_WAIT.md. Lesson: env for one node must be forwarded
   explicitly through bench-7node.sh's qs_launch_node.
4. **S81 (35zzzbg, r111 wave counters)** -- decisive. On 163k-tx follower
   blocks (n=251, node3; node2 reproduces):

   | Field | p50 | p90 |
   |---|---|---|
   | runMs (wave wall) | 345 | 467 |
   | waveColdMs (QMDB cold loads inside the wave) | 0 | 100 |
   | waveBusyMs (sum of 32 workers' time inside exec) | 1,691 | 2,561 |

   Worker utilisation 15%; per-tx exec cost 10.4 us; a balanced wave would
   be ~53 ms. **~290 ms of every heavy wave is spent outside exec.** Not
   read-bound in the median; a ~18% tail of blocks (block number % 4 == 3,
   stride 8 = every other evict-lag-4 flush boundary) pays 100-218 ms of
   cold loads -- a secondary lever. docs/QS_EXEC_WAVE_IDLE.md.

## Open question S82 answers (35zzzbh, r112, launched 2026-10-04 17:xx)

r112 prints per-worker wall / queue length / setup / MVS write / MVS delete
on the "parallel block" line (wkWallMaxMs, wkWallMinMs, wkWallMeanMs,
wkQMax, wkQMin, wkSetupMs, wkMvsWrMs, wkMvsDelMs). Pre-registered reading:

| Pattern | Cause | Fix (Opus task) |
|---|---|---|
| wkWallMax >> wkWallMean | static sender-affinity queues imbalanced (internal/parallel_processor.go:419, executor.go:345-416) | work stealing or finer partition |
| wkMvsWr + wkMvsDel >= 50% of wall | MVS per-entry locks on the 22,857 hot recipients (internal/parallel/mvs.go Write/WriteDelta) | batch / lock-free write path |
| all workers' wall ~= runMs, busy 15%, mvs small | goroutines descheduled: CPU starvation (ingest ecrecover 7 x ~6.7 cores) | cut ingest recovery CPU, or GOMAXPROCS/priority for the wave |
| wkSetupMs dominates | per-worker BeginRo + IBS/EVM setup inside the goroutine | pool / reuse |

Read the result:

    grep -E "ROUND|TPS=" /data/blockchain/wr-logs/r35zzzbh.log | tail
    # heavy-block percentiles of the wk* fields: see the python one-liner
    # pattern in docs/QS_EXEC_WAVE_IDLE.md (node3 logs are JSON under
    # /data/blockchain/qs-node3/log, .gz siblings, bound by the LEG windows)

Then: fill the S82 row, pick the row above, dispatch one Opus code change
(env-gated so the fleet can A/B it at the standing config), prediction on
the board first.

## S82 result (35zzzbh, r112) -- 2026-10-07

Node3, "parallel block" lines with txs >= 100k, bounded by the round-log LEG
windows (America/New_York): B1 18:54:58-19:08:16 (n=148), B2 19:08:16-19:22:26
(n=147). All wk* fields present. wkMvsWrMs/wkMvsDelMs/wkSetupMs are sums over
the 32 workers; wkWall*/wkQ* are per-worker max/min/mean.

| field (B1+B2, n=295) | p50 | p90 | max |
|---|---|---|---|
| runMs | 352 | 479 | 746 |
| execMs | 331 | 459 | 709 |
| waveBusyMs | 1715 | 2530 | 4554 |
| wkWallMaxMs | 311 | 440 | 670 |
| wkWallMeanMs | 66.8 | 99.6 | 215 |
| wkWallMinMs | 0.00 | 0.01 | 0.05 |
| wkQMax | 24000 | 33332 | 53000 |
| wkQMin | 0 | 0 | 0 |
| wkSetupMs (sum) | 0 | 0 | 7 |
| wkMvsWrMs (sum) | 142 | 325 | 1388 |
| wkMvsDelMs (sum) | 203 | 248 | 1309 |
| wkWallMax / wkWallMean | 4.42 | 5.98 | 11.1 |
| wkWallMax / runMs | 0.89 | 0.93 | 0.95 |
| wkWallMean / runMs | 0.20 | 0.26 | 0.38 |
| (MvsWr+MvsDel) / (32 * wkWallMean) | 0.16 | - | - |
| wkSetup / (32 * wkWallMean) | 0.00 | 0.00 | 0.04 |
| waveBusyMs / (32 * wkWallMeanMs) | 0.80 | 0.81 | 0.89 |
| waveBusyMs / (32 * runMs) | ~0.16 | - | - |

B1 and B2 separately agree (B1 wkWallMax p50 329, B2 293; queue max p50
24000 / 23179). Note: the table's row 2 ratio, taken literally as
(MvsWr+MvsDel)/wkWallMean, reads 5.3 only because the Mvs fields are summed
over workers while wkWallMean is per worker; normalised per worker the MVS
share is ~16% of worker wall, under the 50% threshold.

Verdict: row 1 matches (queue imbalance under the static sender-affinity
partition). Every heavy block has at least one worker with an empty queue
(wkQMin = 0, wkWallMin ~ 0 ms) while the busiest holds ~24k of 163k txs
(~4.7x the 5.1k mean) and runs for ~89% of the wave; the other workers
finish in ~67 ms on average. MVS locks (16%), setup (~0) and uniform CPU
starvation (mean wall is only 20% of runMs) are each refuted. Next lever:
work stealing / finer partition in internal/parallel_processor.go:419 and
executor.go:345-416 (one Opus task, env-gated for fleet A/B).

Round B TPS (win1/win2): B1 140,887 / 129,168; B2 143,384 / 121,845; mean
133.8k, below the standing best 146.5k (35zzzba) and the 136-146k band. A
legs: A1 71,619 / 73,142; A2 67,809 / 69,714.

## S83 plan (2026-10-07, America/New_York)

S82 located the import bound in wave queue imbalance (wkQMax p50 24k vs mean
5.1k, wkQMin 0). S83 adds `N42_WAVE_LPT=1` (internal/parallel/executor.go
`partitionByAffinity`): sender chains are sorted longest-first and each is
given to the least-loaded worker; indices are appended in index order so every
nonce chain stays in order on one worker. Env unset keeps the modulo
partition. New log fields `wkKeys`, `wkTopChain` (LPT mode only). Binary
n42-r113 = r112 + this diff; round 35zzzbi runs it with the env on all 7 nodes
against S82's 35zzzbh as baseline. Prediction and pass/fail criteria are in
docs/QS_QUEUE.md row S83. If wkTopChain itself is near wkQMax, a single
sender chain is the floor and only splitting chains (not possible with
nonce ordering) would help.

## Operational notes

- Lineage binaries: /data/blockchain/bin/n42-r1NN AND a copy in
  /data/blockchain/gov5-work (chain scripts check the latter). Recipe:
  base f7ec2836 + hotstuff wholesale + blockchain.go/blockchain_types.go/
  log/root.go/sync/* at 537ec21e/f961f63e + S74 evict files + S72 apply
  files + worker.go/miner.go hand-reverts; tree kept in gov5-work/wt-r111-build.
- Box protocol unchanged (claim files, 3 quiet checks, 10-min spacing);
  n42-rs runs 3- and 7-node benches that take ~115 GB RSS -- our chain
  scripts wait on their own gate.
- Disk: /data was at 99% on 2026-10-04; gocache/gotmp and three old
  campaign dirs were removed (now 1.6 TB free).
- qs-replay offline profiling needs a reflink snapshot taken AT ROUND DONE
  (undo window is 256 blocks); the chain scripts now do this
  (qs-replay-node3-<round>).
