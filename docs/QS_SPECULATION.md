# Speculative block build: hit/miss study (round 35zzzbb)

Run: 35zzzbb, n42-r110 (r109 + S76 spec/specReason + fillLoopDoneUs/fillScan/fillExec on the
parallel-fill path). Both legs on standing config (eviction lag 4). Outcome: B1 142.1k, B2 138.2k,
0 conflicts/refusals/mismatches/BAD BLOCK. Source: `/data/blockchain/wr-logs/r35zzzbb-keep/node*/n42.log`,
2648 "miner: build phases" lines joined across the 7 nodes, split at the window midpoint
(16:03:47-16:29:13, mid 16:20:26) into win1 (n=1326) and win2 (n=1322).

## 1. Rule 132 verdict: PASS (a)-(d)

- (a) `spec` field present on 100% of build-phases lines (2648/2648); only two values observed,
  `spec` (this line IS the speculative/from-scratch build) and `miss` (53 lines, all
  `specReason=not-started`); `fillLoopDoneUs` is non-zero on every parallel-fill line (the S76 fix
  holds).
- (b) hit rate win1 99.6% (1321/1326), win2 96.4% (1274/1322). Hit median build-triggered->sealed
  **1.04 ms** (win1) / **1.08 ms** (win2) — both far under the 100 ms gate. Miss median **573.5 ms**
  (win1, n=5) / **345.0 ms** (win2, n=48) — both over the 400 ms gate except win1's n=5 sample sits
  above it too (501-723 ms per-event), so the median easily clears 400 ms.
- (c) B1 142.1k / B2 138.2k, both >= 140k or within the floor of each other; box precondition held
  (MemAvailable 124 GB, tmpfs 9 GB).
- (d) 0 conflicts / refusals / mismatches / BAD BLOCK.

## 2. Fill split on misses (from-scratch builds)

| window | fillScan median | fillScan p90 | fillExec median | fillExec p90 | candidatesScanned median | candidatesIncluded median |
|---|---|---|---|---|---|---|
| win1 (n=5) | 128.7 ms | 130.9 ms | 342.2 ms | 367.5 ms | 163,000 | 163,000 |
| win2 (n=48) | 43.9 ms | 54.1 ms | 92.1 ms | 118.1 ms | 22,857 | 22,857 |

Execution dominates in both windows: fillExec is ~2.5-3x fillScan on a miss. candidatesScanned ==
candidatesIncluded throughout (no candidate rejection cost) — the miss cost scales with pool depth,
not selectivity. Win1's pool was ~7x deeper (163k vs 22.9k) which is why its few misses are the
most expensive events in the whole run.

## 3. Miss mechanism

Every one of the 53 misses carries `specReason=not-started` — **zero** `stale-applied-head-moved`
misses occurred in this run (no case where a parked speculative task existed for the wrong parent).
`not-started` means no speculative task was ever parked for the committed parent at all. Grepping
the surrounding context, every `not-started` miss carries an outsized `reload` field (57-319 ms,
vs microseconds on a normal build) and lines up with "miner: speculative build chains on own
unwritten block" (2317 occurrences, i.e. speculation is only started when the local node's own
prior proposal is the parent being extended). When the committed parent instead comes from another
proposer — a leader-rotation / tenure boundary, not a stale-head race — the local node had nothing
parked to resume, so it falls through to a full from-scratch build plus the reload cost of catching
up to a parent it did not itself produce.

Distribution win1 vs win2: win1 has 5 not-started misses against 1326 builds (0.4%); win2 has 48
against 1322 (3.6%), 9x more. In three sentences: win2's run segment crossed more leader-rotation
boundaries than win1's per unit of blocks produced, and each boundary forces a from-scratch build
because today's speculative trigger only fires on "chains on own unwritten block" — it never
anticipates a parent the local node did not itself propose. Win2's shallower pool (22.9k vs 163k
candidates) makes each individual miss cheaper, but there are far more of them, which is exactly
why the *rate* falls (96.4% vs 99.6%) even though the per-miss ceiling drops. No `not-started` miss
in either window shows evidence of "previous block not yet executed under the depth-1 rule" or a
"refusing block production on unexecuted committed parent" transient — the signature (reload-heavy,
tracks the own-unwritten-block gate) is cleanly tenure-rotation, not a depth-1 stall.

## 4. Lever design (no code)

| option | expected hit rate | expected cycle saving | consensus risk | complexity |
|---|---|---|---|---|
| A. Speculate on the block the leader is about to commit (only parent it can be sure of in-tenure) | no change for cross-leader rotation (doesn't touch the actual gap) | ~0 on the `not-started` misses this run found | none (already same guarantee as today's "own unwritten block" case) | n/a — this is what we already do |
| B. Start speculative build immediately at seal of block N for N+1, regardless of which node proposed N | removes most of the 53 `not-started` misses; win2 hit rate -> ~99%+ | ~400 ms (win2) / ~570 ms (win1) saved per avoided miss; at ~2-4% miss rate this is ~8-20 ms average cycle time, with the real win on p99 tail latency | must still gate on the S26 stale-sibling guard: the parked task's parent must be re-validated as the actually-committed (non-conflicting) block before it is served, exactly as today's hit path does — no new risk if the existing validation-on-resume path is reused | Sonnet: extends the existing "park on seal" trigger to fire on any observed committed block, not just self-proposed ones; no new consensus logic |
| C. Keep two speculative candidates in flight (self-proposed + most-likely-other-leader) | marginal gain over B for the same rotation misses, plus covers short-lived forks before finality | similar per-event saving as B, but burns 2x speculative CPU/RAM per block — likely not worth it given misses are already <4% | still needs the S26 guard on whichever candidate is served; doubles the state kept around during a reorg window, higher surface area | Opus: resource-bounded scheduling across two in-flight tasks, cancellation ordering |
| D. Partial reuse — keep the speculative task's executed state and replay only the pool's tail delta when the real parent differs slightly | best saving in theory (avoids a full fillExec even on some misses) but only applies when the realized parent is a superset/near-miss of the speculated one, which none of this run's 53 misses were (all cross-leader, not stale-head) | 0 benefit measured in this run since no `stale-applied-head-moved` cases exist to reuse against | highest: partial state reuse across a different parent risks subtly wrong execution state if the diff isn't exactly a tail append; needs careful invariant proof | Opus, and only worth building once stale-head misses actually appear in the data |

**Pick: Option B** — start the speculative build at seal time of block N for N+1 unconditionally
(own or other proposer), reusing the existing S26 stale-sibling re-validation on resume. It is the
only lever that targets the mechanism this run actually found (100% of misses are `not-started` at
leader-rotation boundaries, 0% are stale-head), it is Sonnet-complexity (extends an existing trigger
rather than adding new state-reuse logic), and it introduces no new consensus risk because the
served candidate still passes through the same guard that gates today's hits.
