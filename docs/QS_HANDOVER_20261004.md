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

## S83 result (35zzzbi, r113, B1 only) -- 2026-10-08

Round 35zzzbi (binary n42-r113, N42_WAVE_LPT=1 on all 7 nodes, confirmed by the
ENV lines) ran A1 and B1, then was ABORTED on memory at 16:51:14 ET
2026-10-07 (round ended 16:54:17); B2 never ran. Node3 "parallel block" lines
with txs >= 100k, bounded by the round-log leg windows (A1 16:28:07-16:40:26,
B1 16:40:26-16:54:17). A1 has no qualifying block (max txs per block 22,857);
B1 n=154 (every heavy block has exactly 163,000 txs). S82 column is B1+B2,
n=295, from the S82 section above.

| field | S82 p50 | S83 p50 | S82 p90 | S83 p90 | S83 max |
|---|---|---|---|---|---|
| runMs | 352 | 244 | 479 | 381 | 743 |
| execMs | 331 | 219 | 459 | 337 | 722 |
| finalizeMs | n/a | 119.5 | n/a | 179 | 318 |
| waveBusyMs | 1715 | 1864 | 2530 | 3270 | 9998 |
| wkWallMaxMs | 311 | 178 | 440 | 288 | 672 |
| wkWallMeanMs | 66.8 | 73.5 | 99.6 | 127 | 401 |
| wkWallMinMs | 0.00 | 0.00 | 0.01 | 0.01 | 0.06 |
| wkQMax | 24000 | 12000 | 33332 | 12000 | 12000 |
| wkQMin | 0 | 0 | 0 | 0 | 0 |
| wkSetupMs (sum) | 0 | 0 | 0 | 1 | 3 |
| wkMvsWrMs (sum) | 142 | 163 | 325 | 438 | 1305 |
| wkMvsDelMs (sum) | 203 | 222 | 248 | 326 | 1460 |
| wkKeys (new) | - | 16 | - | 21 | 28 |
| wkTopChain (new) | - | 12000 | - | 12000 | 12000 |
| wkWallMax / wkWallMean | 4.42 | 2.45 | 5.98 | 2.69 | 3.76 |
| wkWallMax / runMs | 0.89 | 0.75 | 0.93 | 0.81 | 0.90 |
| wkWallMean / runMs | 0.20 | 0.31 | 0.26 | 0.37 | 0.57 |
| (MvsWr+MvsDel) / (32*wkWallMean) | 0.16 | 0.17 | - | 0.19 | 0.32 |
| waveBusyMs / (32*wkWallMean) | 0.80 | 0.80 | 0.81 | 0.82 | 0.89 |

Mean queue is 163000/32 = 5,094. wkQMax equals wkTopChain (12,000) in every
block: the busiest worker holds exactly the longest single-sender nonce chain
(2.36x the mean queue), and the block has only ~16 distinct senders, so with
LPT the queue floor is that chain. wkQMin stays 0 (16 senders cannot fill 32
workers). 12,000 txs take ~178 ms wall at the observed rate.

Ruling against the S83 row (pre-registered):
- runMs p50 352 -> 244 = -30.7%: PASS on the >= 30% line, by 0.7 pt (not the
  <= 200 ms stretch target; p90 479 -> 381).
- wkWallMax p50 <= 120: FAIL (178). The remaining cost is the single 12k chain.
- wkQMax p50 <= max(1.2 x mean queue, wkTopChain) = max(6.1k, 12k) = 12k:
  PASS (equal to the bound).
- TPS half (B mean >= 146.5k) is PARTIAL, one B leg: B1 win1/win2 149,877 /
  133,725 vs S82 B1 140,887 / 129,168 (win1 +6.4%, win2 +3.5%) and vs the
  standing best 146.5k (win1 above, win2 below). The B2 leg is missing, and the
  memory abort (16:51:14) fired inside win2 (87 blocks, blockTime 0.69 s vs
  0.50 s in win1), so win2 is suspect.
Overall: INCONCLUSIVE. The executor half works as designed (wkWallMax/mean
4.4 -> 2.5, runMs -31%) but the 120 ms target is not reachable while one
sender owns a 12,000-tx chain; only a workload with more senders or splitting
chains would lower it. Needs a clean B1+B2 rerun for the TPS criterion.

Abort cause (r35zzzbi-mem.log): total AnonPages peaked at 83.6 GB in B1 (80.1
GB in A1) and MemAvailable fell to 18 GB, the same anon footprint as 35zzzbh
B1 (84.5 GB peak, MemAvailable min 29 GB). The difference is Shmem 28.4 GB
in 35zzzbi (already present at 16:08) vs 13.8 GB in 35zzzbh, i.e. ~14.6 GB
less available at equal node+flood anon, not growth in the n42 nodes or
txflood (floods 290-420 MB each, flat). Per-node RSS is not in the mem log
(nodesAnon field empty), so r113 vs r112 per-node RSS is not separable here.

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
