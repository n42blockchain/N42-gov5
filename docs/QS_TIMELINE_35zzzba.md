# 35zzzba: per-block timeline (S75) and the 137.8k -> 146.5k jump

Round 35zzzba, binary n42-r109 (r108 + S75 logging-only timeline instrumentation),
standing config + `N42_QMDB_EVICT_LAG_BLOCKS=4` on both legs. B1 146.0k, B2 146.5k;
0 conflicts/refusals/mismatches/BAD BLOCK. Source logs:
`/data/blockchain/wr-logs/r35zzzba-keep/node*/`, `r35zzzba.log`, `r35zzzba-mem.log`,
`r35zzzba-vm.log`. Comparison baseline: `r35zzzaz-keep/`, `r35zzzaz-mem.log`,
`r35zzzaz-vm.log` (n42-r108, same config, B2 win1/win2 142.1k/133.5k -> 137.8k).

## 1. Per-block timeline, B2 win1 (14:23:59-14:24:19) and win2 (14:24:59-14:25:20)

Joined by epoch-microsecond fields (`tUs`/`buildTriggeredUs`/the `us={...}` blob),
paired index-wise within each node's own event stream (build phases -> block
timeline -> proposal broadcast -> leader view-timing are strictly ordered per
node; hash fields in these three message types did not match 1:1 across
messages for the same block in this instrumentation pass -- noted as a logging
gap below, not a consensus bug. 0 mismatches/BAD BLOCK/conflicts came from the
chain's own counters, which are independent of this log join).

| segment | win1 median | win1 p90 | win2 median | win2 p90 | grows win1->win2? |
|---|---|---|---|---|---|
| build-triggered -> pool snapshot | 1.4 ms | 59.4 ms | 1.9 ms | 126.7 ms | slight |
| pool snapshot -> fill-loop-done | n/a (fillLoopDoneUs=0 on every record) | | | | unmeasured |
| fill-loop-done -> finalize | n/a (same gap) | | | | unmeasured |
| finalize -> sealed | 1.7 ms | 13.8 ms | 10.3 ms | 16.3 ms | **yes, ~6x** |
| **build-triggered -> sealed (leader compute, total)** | **54.5 ms** | 667.0 ms | **603.8 ms** | 794.7 ms | **yes, ~11x -- largest segment** |
| sealed -> proposal broadcast | 196.8 ms | 378.0 ms | 25.3 ms | 429.6 ms | noisy (index-pairing artifact, see caveat) |
| proposal handed to network -> first vote received | 55.1 ms | 66.0 ms | 56.8 ms | 69.1 ms | flat |
| first vote received -> QC formed | 7.4 ms | 59.8 ms | 9.0 ms | 109.5 ms | flat/slight |
| QC formed -> next block's build-triggered (global, cross-node, outliers >3x interval dropped) | 265.2 ms | 565.4 ms | 221.3 ms | 628.3 ms | shrinks |

Gate (a) -- chain sums to the observed block interval within 10% for >= 90% of
in-tenure blocks: **not cleanly measurable from this pass and does not pass as
stated.** The segments above are not strictly additive per block: QC-to-next-
build and build-to-sealed run concurrently across different leaders (tenure=8,
leader rotates every block), so summing medians over-counts pipeline overlap.
A naive median-sum check (build-to-sealed + sealed-to-broadcast + handed-to-
firstvote + firstvote-to-qc + qc-to-next-build) gives ~579 ms for win1 (actual
interval 500 ms, 16% over) and ~916 ms for win2 (actual interval 659 ms, 39%
over) -- fails the 10% bound in win2 specifically because build-to-sealed
alone (603.8 ms median) already exceeds the whole observed interval, which
only makes sense because that segment overlaps with neighbouring blocks'
network/vote segments on other nodes. **Gate (a) is ruled not satisfied as
written**; a correct additive check needs per-leader wall-clock accounting
(a next-round fix), not a log join across 7 independently-clocked nodes.

Gate (d) -- which segments grow win1->win2: **build-triggered -> sealed**
(leader compute: fillScan + fillExec + finalize/root, unioned since
fillLoopDoneUs never got a value in this logging pass) is the one segment that
grows sharply and dominates the interval growth (500 ms -> 659 ms). Within it,
finalize -> sealed also grows ~6x on its own. Network/vote segments (broadcast
-> first vote -> QC) are flat across windows. **Gate (d) passes**: the named
growing segment is leader compute, not network/vote and not the QC-to-next-
build gap (which actually shrinks).

**Largest segment: leader compute (build-triggered -> sealed)**, not
network+vote and not the QC->next-build gap. This tracks occupancy climbing
22.4% (win1) -> 28.3% (win2) in the same leg: later in the window there is
more pool/backlog for the leader to scan and apply.

**Instrumentation gap found**: `fillLoopDoneUs` is 0 on every "miner: build
phases" record across all 7 nodes and both windows -- it was never wired up
in the S75 patch, so fillScan/fillExec cannot be isolated from the rest of
leader compute this round. Also, the block hash in "block timeline" did not
match the block hash in "proposal broadcast"/"hotstuff view timing" for the
same block in this pass (same-node, same-time pairing was used instead,
verified 1:1 by count per node) -- worth a source check before relying on
hash-joins in a future round.

## 2. Why 137.8k (35zzzaz-B2) jumped to 146.5k (35zzzba-B2) with the same config

35zzzaz ran on n42-r108 (pre-S75, no timeline fields); 35zzzba on n42-r109
(r108 + S75, logging only). Compared on what both rounds do have:

- **Page cache / fault pressure, B2 window**: nearly identical.
  - pgmajfaultD sum: az 4,502,643 vs ba 4,712,610 (+4.7%)
  - refaultFileD sum: az 4,717,899 vs ba 4,699,259 (-0.4%)
  - MemAvailable at B2 start: az 112G vs ba 112G (same)
  - Shmem at B2 start: az ~7.3G vs ba ~9.2G (modestly higher on ba, not lower)
- This **contradicts** a simple "cleaner tmpfs -> less cache pressure ->
  faster" story: ba's B2 window shows the same or slightly worse major-fault
  and Shmem numbers than az's B2, yet runs faster. The round's own framing
  ("tmpfs had just been cleared, MemAvailable 116G at start, nothing else
  running") describes the whole-run start (13:29, avail=116G, Shmem=8.9G),
  not the B2 window itself, and that starting condition is not statistically
  distinguishable from az's own start (00:05, avail=112G, Shmem=7.1G) either.
- Since n42-r109 vs n42-r108 is logging-only (S75 adds JSON fields to existing
  log lines; no execution-path code in `internal/miner` or
  `internal/consensus/hotstuff` changed besides the log calls themselves per
  the diff recipe in QS_QUEUE.md row S75), **it should not and does not
  explain a throughput change** -- the binary is not the cause.

**Conclusion: the 137.8k -> 146.5k jump is not explained by anything this
round measured** (box memory/cache state was equivalent; the binary change
is logging-only). The most likely remaining explanation is unmeasured host
contention -- CPU scheduling noise, a quieter box in the CPU/IO dimension that
`Cached/Shmem/MemAvailable/pgmajfault/refault` do not capture (e.g. no other
tenant process competing for cores) -- consistent with the existing 3.6%
noise-floor finding, except this gap (+6% over the prior best leg, +9% over
the same-config az leg) is larger than that floor. **The standing score should
be treated as box-state dependent until a same-config rerun on a verified-quiet
box reproduces it**; it is not yet safe to attribute to any lever.

**Box-state precondition for a comparable round**: tmpfs cleared immediately
before the run, MemAvailable >= 115 GB and Shmem < 10 GB at leg start, and no
other process/tenant running on the box for the full leg (not just at start).

## 3. Queue update

See `docs/QS_QUEUE.md` row S75 (ruled) and the standing-score block (new
standing best, box-state precondition noted, previous values kept under
"Before:").

## 4. Next lever

The largest and only clearly growing segment is leader compute
(build-triggered -> sealed), and within it the still-unmeasured fillScan vs
fillExec split (fillLoopDoneUs=0 bug) hides whether the growth is pool
scanning or EVM execution. The next lever should be **config-only**: wire up
`fillLoopDoneUs` (a one-line fix matching the existing S75 recipe, not a new
design) and rerun one same-config leg to get a clean fillScan/fillExec split
for win1 vs win2, before spending a Sonnet/Opus pass on redesigning the fill
loop itself -- it would be premature to touch fill-loop code without knowing
which half of it is actually growing.
