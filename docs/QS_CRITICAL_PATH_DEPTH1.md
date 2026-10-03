# Depth-1 leader-side critical path (r35zzzbb, node1, view 9696-9811 window)

Source: `/data/blockchain/wr-logs/r35zzzbb-keep/node1/n42.log`, joined per leader
view via `qcFormedUs`/`proposalHandedToNetworkUs`/`firstVoteReceivedUs` (hotstuff
view timing, role=leader), `miner: commitWork begin` (tUs), `blockimport phases`,
and `parallel block`. 644 consecutive-view (tenure-internal) pairs.

## Chain medians (leader side, one tenure, node1)

| Arrow | Median | % of interval |
|---|---|---|
| QC formed(N) -> commitWork begin(N+1) | 10.3 ms | 4.1% |
| commitWork begin(N+1) -> proposal handed to network(N+1) | 180.4 ms | 72.1% |
| proposal handed -> first vote received | 54.3 ms | 21.7% |
| first vote -> QC formed(N+1) | 5.3 ms | 2.1% |
| **Sum of arrows** | **250.3 ms** | **100.0%** |
| **Measured QC(N)->QC(N+1) interval** | **250.3 ms** | — |

Coverage: 100.0% (sum 250,347 us vs measured interval 250,261 us). The chain is
complete for this run; there is no unaccounted idle gap on this leader/window.

## Hypothesis verdict

Partially confirmed, but the dominant segment is **not** an idle "QC formed ->
build triggered" gap: that gap measures only 10.3 ms (4%) here, not the 220-265
ms suggested by the speculative-build docs. The dominant cost is **inside**
`commitWork begin -> proposal handed to network` (180.4 ms, 72% of the block
interval) — i.e. real block construction/execution/finalize/seal/broadcast, not
idle time before it starts. `blockimport phases` on this leader's own (fast,
speculative-hit) path is sub-5ms median (exec 85us, write 1.66ms, root 147us,
hdr 2.38ms, total 4.5ms) — negligible — so the 180ms is not showing up as
classic "blockimport" cost; it is better explained by the `parallel block`
executor stats on this window: execMs median 91ms, finalizeMs median 65ms
(91+65=156ms, close to the 180ms build gap), i.e. these are the non-cached /
slow-path builds, not the >96% 1ms-hit builds the speculation doc describes.
Network+vote (proposal handed -> QC) is 59.6ms combined (24%), matching the
docs' ~65ms figure closely.

So: largest segment = the build/exec/finalize work between commitWork-begin and
proposal-broadcast (180ms, dominated by exec ~91ms + finalize ~65ms per
`parallel block` logs), not a separate idle gap and not root (147us, trivial).

## Supporting data

- **Executor workers in use**: no `N42_PARALLEL_WORKERS` env line found in any
  node's log for this run -> default from `internal/parallel_processor.go:57-63`
  (`parallelWorkers()`) applies: **32 workers** (box has 256 threads shared by 7
  nodes; NumCPU is intentionally not used here per the comment at line 54-56).
- **`parallel block` log** (743 samples, node1): executorMs/setupMs/collectMs
  medians are all 0 (fast path dominates the *count* of samples), but
  execMs/finalizeMs/runMs medians are 91/65/95 ms — a bimodal distribution: most
  blocks are ~1ms hits (as the sample line shows: execMs=4, finalizeMs=1,
  runMs=5) but the median across all logged blocks is pulled up by a sizeable
  population of slow (non-hit) blocks, which is what shows up in the
  commitWork->proposal-handed gap above.
- **Follower overlap**: followers' own `blockimport phases` total is also
  sub-5ms median (same cheap/speculative-hit path), far under the 54ms
  proposal->first-vote network latency that dominates that arrow. Follower exec
  therefore completes well inside the network/vote window and is **not** the
  binding constraint; the prepare-vote latency here is network-bound, not
  follower-exec-bound. (We did not have per-block hash join across nodes in
  this pass, so this is inferred from magnitude, not a direct N-vs-N timestamp
  overlap.)

## Two levers on the largest segment (commitWork -> proposal-handed, 180ms)

1. **Make the slow-path exec+finalize the common case instead of the rare one**
   (attack the bimodality, not the median of one mode): the `parallel block`
   samples show most individual blocks execute in ~1-5ms but the aggregate
   median across the window is 91ms/65ms — find why so many builds miss the
   speculative/hint-fill fast path (sample line shows `hintHits:0` despite
   `hintFills:1000`) and fix the hint-hit rate. Expected gain: if the 180ms
   build collapses toward the ~5-10ms fast-path blocks already observed,
   interval could drop from 250ms toward ~90-100ms (network+vote bound).
   Complexity: medium — requires tracing why hint hits are 0 in the sampled
   block and whether that correlates with tenure boundaries or specific tx
   patterns, not a config flip.
2. **Raise `N42_PARALLEL_WORKERS` above the 32 default** on the slow-path
   blocks, since the box has 256 threads across 7 nodes (~36 threads/node
   budget): a config-only round testing 48/64 workers costs no code change.
   Expected gain is speculative until measured — if the slow blocks are
   genuinely exec-bound (not memory/contention-bound), raising workers could
   cut the ~91ms execMs materially; if they are serialization/contention-bound
   (Block-STM aborts, lock contention) more workers could instead regress via
   oversubscription on a 7-node shared box. Complexity: low — pure env var
   sweep, one round per worker count, watch CPU and wall time together (per
   the CPU-seconds judging rule) before adopting.
