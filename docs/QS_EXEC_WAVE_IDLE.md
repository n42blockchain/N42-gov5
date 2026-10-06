# S81 -- exec-wave idle time, confirmed (round 35zzzbg, n42-r111)

Source: node3 and node2, `log/n42.log` + the preceding `.gz` rotation, JSON
`"parallel block"` lines, filtered to the B1/B2 windows (14:43:57-14:57:47
and 14:57:47-15:11:33 America/New_York) and to `txs >= 100000 && lenient ==
false` (n=251 on node3, n=252 on node2 -- the round's full near-163k-tx
follower blocks).

## Confirmed numbers (node3, n=251)

| field | p50 | p90 | max | mean |
|---|---|---|---|---|
| runMs | 345 | 467 | 730 | 357.6 |
| execMs | 324 | 440 | 710 | 335.2 |
| waveReads | 163,028 | 163,340 | 163,634 | 160,082 |
| waveColdReads | 36 | 99,206 | 163,544 | 22,369 |
| waveColdMs | 0 | 100 | 218 | 24.3 |
| waveBusyMs | 1,691 | 2,561 | 6,391 | 1,855.6 |
| applyMs | 22 | 33 | 83 | 24.2 |
| finalizeMs | 119 | 157 | 233 | 124.7 |

Matches the task's quoted numbers exactly. Second follower (node2, n=252)
reproduces the same shape: runMs p50 354/p90 510/max 859, waveColdMs p50
0.5/p90 103/max 238, waveBusyMs p50 1,749/p90 2,873/max 4,814 -- the median
block is not read-bound on either follower, and the busy/worker-wall gap is
the same order of magnitude node to node.

Utilisation: median wave busy 1,691 ms / 32 workers = 52.8 ms of "perfectly
balanced" worker time against a 345 ms wave -- utilisation ~15%, matching
the task's estimate. Per-tx exec cost: 1,691 ms / 163,000 tx = 10.4 us/tx.

## Cold-read burst periodicity

45 of 251 node3 blocks (18%) have `waveColdReads > 50,000` (a block-wide
cold scan rather than isolated misses). Their block numbers land almost
exclusively at **n % 4 == 3** (node2's independent sample: n % 4 in {2, 3}
-- the off-by-one between nodes is the two followers importing the same
block numbers at different pipeline offsets, not a different cycle). The
gap between consecutive burst blocks is a multiple of 8 almost everywhere
(diffs: 8,8,8,8,...,16,24,48,... -- never 4), i.e. roughly every *other*
4-block boundary produces a large cold burst, not every one.

This lines up with `qmdb-evict-lag-blocks=4` (S74): the evictor flushes on
a 4-block cadence, and every second flush boundary appears to coincide with
a twig generation old enough to fall out of the resident set, producing a
block-wide cold wave. The remaining flush boundaries (the other `n % 4 ==
3` blocks not in the >50k list) pay little or nothing, consistent with the
evicted twig range varying in size flush to flush. No `"qmdb root phases"`
line was logged in the same per-block JSON stream to cross-reference
directly (that message name does not appear in `qs-node3/log/n42.log` for
this round); the periodicity argument above rests on the evict-lag
constant and the block-number pattern alone, not a captured root-phase
burst. **This is a secondary lever**: it accounts for the p90 tail (up to
218 ms of a 467 ms wave) but not the median (0 ms cold) or the 290 ms of
unaccounted-for wave time at the median.

## Where the median 290 ms (345 ms wave - 53 ms balanced-busy) goes

Not yet isolated by S81's counters alone -- that is S82's job. The
candidates named in the task, now covered by new fields (`internal/
parallel/executor.go` `workerStat` + `WorkerStats()`, wired into the
`"parallel block"` line as `wkWallMaxMs`/`wkWallMinMs`/`wkWallMeanMs`/
`wkQMax`/`wkQMin`/`wkSetupMs`/`wkMvsWrMs`/`wkMvsDelMs`):

1. **Queue imbalance** under the sender-affinity static partition
   (`executor.go:354` `queues[w] = append(...)`, wave ends at the slowest
   worker) -- visible as `wkQMax` >> `wkQMin` and `wkWallMaxMs` >>
   `wkWallMeanMs`.
2. **MVS write/delete loops** around `exec` (`executeSingle`'s
   `mvs.Delete`/`mvs.Write`/`mvs.WriteDelta` loops, per-entry locks on the
   22,857 hot recipients) -- visible as `wkMvsWrMs`/`wkMvsDelMs` relative
   to total worker wall time.
3. **`workerSetup` inside the goroutine** (`executor.go` worker closure,
   before the work loop) -- visible as `wkSetupMs`.
4. **CPU starvation** (7 nodes x 32 workers + ingest ecrecover on one box)
   -- if none of the above explain the gap (all workers' wall time close to
   `runMs`, busy still 15%, mvs small), the workers are being descheduled
   rather than blocked on in-process work.

See the S82 board row in `docs/QS_QUEUE.md` for the round that measures
these.
