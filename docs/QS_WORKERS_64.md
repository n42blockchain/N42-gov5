# Doubling Block-STM workers to 64: exec barely moves, throughput doesn't

Round 35zzzbd (S78), n42-r110, pacing fixed at 125ms in every leg (S77/S78
retire the pacing A/B so follower exec+root time is the thing that binds).
A/B: `N42_PARALLEL_WORKERS` 32 (today's default, warm-up/A1/B1) vs 64
(B2/A2). Logs: `/data/blockchain/wr-logs/r35zzzbd.log` (LEG/ENV/win lines),
per-node `/data/blockchain/qs-node{0..6}/log/n42*.log[.gz]` bounded by each
leg's LEG-line timestamps.

Launch check confirmed: `r35zzzbd.log` ENV lines show
`N42_PARALLEL_WORKERS=32` on A1/B1 nodes and `N42_PARALLEL_WORKERS=64` on
B2/A2 nodes (node0 and node3 sampled, lines 106-107/164-165 vs 227-228/290-291).

## (1) B-mean per leg

| leg | window | win1 TPS | win2 TPS | leg B-mean |
|---|---|---|---|---|
| A1 (workers=32, low-gas) | 19:18:52-19:31:11 | 68,952 (blocks=181, 0.331s) | 73,523 (blocks=193, 0.311s) | 71,237.5 |
| B1 (workers=32, full-gas) | 19:31:11-19:43:44 | 142,313 (blocks=120, 0.500s) | 138,010 (blocks=90, 0.667s) | 140,161.5 |
| B2 (workers=64, full-gas) | 19:43:44-19:56:49 | 139,952 (blocks=122, 0.492s) | 138,261 (blocks=94, 0.638s) | 139,106.5 |
| A2 (workers=64, low-gas) | 19:56:49-20:09:18 | 68,190 (blocks=179, 0.335s) | 72,761 (blocks=191, 0.314s) | 70,475.5 |

B2 vs B1: 139,106.5 vs 140,161.5 TPS, **-0.75%** — B2 is *lower*, well inside
(in fact on the wrong side of) the 3.6% noise floor. The A legs (low-gas,
workers only matters incidentally) are flat across the worker change as
expected (71.2k vs 70.5k, -1.0%, also inside the floor).

## (2) Follower `blockimport phases` exec/root/proc/setup, blocks >=100k tx

Pooled across all 7 nodes' `blockimport phases` lines, bounded by each leg's
LEG-line start/end timestamps (B1: 19:31:11-19:43:44, B2: 19:43:44-19:56:49),
filtered to `txs >= 100000`.

| leg | blocks (pooled, 7 nodes) | exec p50 | exec p90 | root p50 | root p90 | proc p50 | proc p90 | setup p50 | setup p90 |
|---|---|---|---|---|---|---|---|---|---|
| B1 (workers=32) | 924 | 318.10 ms | 441.00 ms | 142.48 ms | 178.80 ms | 525.30 ms | 648.95 ms | 0.98 ms | 2.61 ms |
| B2 (workers=64) | 948 | 293.63 ms | 396.92 ms | 143.86 ms | 203.38 ms | 497.01 ms | 636.63 ms | 0.98 ms | 2.45 ms |
| delta (B2-B1) | +24 | -24.47 ms (-7.7%) | -44.08 ms (-10.0%) | +1.38 ms | +24.58 ms | -28.29 ms (-5.4%) | -12.32 ms (-1.9%) | +0.00 ms | -0.16 ms |

Rule 134(b) required exec p90 to fall by >=25% (274 -> <=205 ms baseline cited
in the row) and proc p90 to fall by >=60 ms, with setup rising by <10 ms.
Only the setup sub-condition passes (flat, ~1-2.6 ms, far under the 10 ms
budget — doubling workers does not meaningfully raise the already-cheap
per-worker setup cost at this tx volume). Exec p90 fell only 10.0%, not 25%,
and root p90 actually *rose* 24.58 ms, largely cancelling exec's gain in the
combined `proc` figure, which fell only 12.32 ms against the 60 ms gate.

## (3) Stability counters, B1 vs B2 (bounded by the same leg windows, pooled
across all 7 nodes)

| signal | B1 (workers=32) | B2 (workers=64) |
|---|---|---|
| refusing block production on unexecuted committed parent | 2 | 0 |
| sealed block is stale | 3,966 | 4,094 |
| hotstuff "f+1 future timeouts observed" | 3 | 0 |
| catch-up: requesting range | 335 | 298 |
| conflicting commits | 0 | 0 |
| BAD BLOCK | 0 | 0 |

No conflicting commits or BAD BLOCK in either leg. B2 is at least as stable
as B1 by every counter except stale-block count, which is up only 3.2% (in
the same range as normal round-to-round noise) — rule 134(d) passes cleanly.

## (4) load1 peak per leg

**Not available.** This round's monitoring files (`r35zzzbd-mem.log`,
`r35zzzbd-vm.log`, `r35zzzbd-memstats.log`) do not log a `load1`/`loadavg`
field anywhere in this round's logs (checked all three; `mem.log` carries
only `avail`/`Cached`/`Dirty`/`AnonPages`/`Shmem`, `vm.log` carries vmstat
deltas, `memstats.log` carries Go runtime heap stats). Rule 134(e)'s CPU
half (`load1 <= ~200`) cannot be verified from this round's artifacts and is
reported as missing data, not inferred. The throughput half of (e) (A legs
must not drop below 60k) is directly measured: A1 71.2k, A2 70.5k, both
comfortably above the 60k floor and unaffected by the worker change.

## Rule 134 verdict

- **(a) PASS** — ENV lines confirm `N42_PARALLEL_WORKERS=64` on B2 nodes.
- **(b) FAIL** — exec p90 fell 10.0% (441.0 -> 396.9 ms), short of the >=25%
  gate; proc p90 fell only 12.3 ms, short of the >=60 ms gate; setup p90 rose
  by -0.16 ms (i.e. did not rise), clearing that sub-gate alone.
- **(c) FAIL** — B2 B-mean (139,106.5) is *below* B1 (140,161.5), -0.75%,
  inside (and on the wrong side of) the 3.6% noise floor. No real gain.
- **(d) PASS** — stability counters at or better than B1's level in every
  leg; 0 conflicting commits, 0 BAD BLOCK in both legs.
- **(e) INDETERMINATE on CPU** — load1 was not logged this round, so the
  "<=~200" condition cannot be checked; the "A legs >= 60k" half passes
  (71.2k / 70.5k).

## Overall ruling

**Exec time does not scale down with more Block-STM workers (32 -> 64) on
this fleet at today's tx volume.** The p90 drop (10.0%) is real but far
short of the 25% gate, root time rose enough to erase most of the exec gain
at the `proc` level, and end-to-end B-mean throughput did not improve at all
(B2 is slightly *below* B1, inside the noise floor). Per the row's own
falsifier: since (b) fails, **the executor is contention/finalize-bound, not
worker-count-bound — the next lever is Block-STM finalize parallelism, not
raising the worker count further.**
