# QS queue (commander's board, 2026-09-20)

Standing goal: the seven-node qs fleet's TPS. Scored metric: the B mean.
Standing best: **B 127.6k** (35zzt, n42-r80 = deferred + fold outside the
writer + tx-bounded tail + packet window 8). Protocol: QS_AGENT_PROTOCOL.md.

| id | step | binary | prediction | status | result |
|----|------|--------|-----------|--------|--------|
| S1 | 35zzx: sixteen generators x 500 senders -- restore supply (37% occupancy at 35zzt) | n42-r84 | 77 (6bq): occupancy >60%, B > 127.6k, no BAD BLOCK | done | falsified (6bu): B mean 47.0k vs 127.6k standing best; occupancy fell to 8.5% mean, not >60%; no BAD BLOCK/divergence, but 23 blocks in a 37 s window of B2 dropped 183,282 candidates to nonceHigh (up to 99.9% of one block) as the sixteen generators ran dry -- the acceptance note's mass-drop trip, so the result cannot be credited as supply-restored |
| S2 | 35zzw: per-block base-read cache (parallel.BaseCache) | n42-r85 | 78 (6br): follower `proc` -10% on full blocks | queued | |
| S3 | why the leader's build fell back (9064 aborts on 23000 txs): a fallback costs the block its parallelism and used to corrupt it | spec first | 79 (6bs) | spec written, cause open | the build's own reader and the executor's per-worker reader disagreed about a deep-nonce sender's account (buildReaderNonce 4500 vs worker-seen state 0); Block-STM cannot tell that miss from a resolvable conflict and burns all 64 waves finding out; prediction 79 not yet tested against a fresh wave-limit exhaustion |
| S3b | do the routine nonceHigh drops (9200/32200, 13500/18500) also come from the reader disagreement, or are they genuine send-ahead | logs only | 80 (6bt) | done | falsified as genuine send-ahead: both blocks are bug-driven, but not the same bug -- 13659302 is the live reader race (S3/pred 79), 13659303 is downstream of the now-fixed c0931aeb corruption (13659302's fallback wiped 17,036 accounts, so 13659303's build reads the same already-emptied state from both readers, which only looks like agreement); no other build in the log (0 of ~991) dropped any candidate to nonceHigh, so "routine" does not describe this log at all |
| S4 | the Prague delegation check reads every recipient (6bp) | not built | not written | candidate | |
| S5 | the leader's write (~0.5 s) off the critical path | not built | not written | candidate | |
| S6 | per-transaction allocation hotspots (receipt, AsMessage, journal dirties, IBS.Reset maps) | not built | not written | candidate | |

## Acceptance note on S1 (added 2026-09-20 after S3b)

S3b closed the reader-disagreement lead: only 2 of ~993 builds in the
13659302 log dropped any candidate to `nonceHigh`, and both were the delta
bug's own cascade -- the block whose 17,036 credited accounts were emptied,
and the next block, whose senders then read nonce 0. The fix removes the
cascade, so 35zzt's 37% occupancy is a genuine supply shortage and S1 stands.

The agent that analyses 35zzx attempt 2 must therefore also report the
`parallel fill drops` nonceHigh total across the B legs. It should be
approximately zero. If mass drops reappear on n42-r84, the occupancy gain is
not supply and prediction 77 must not be credited, whatever the B mean says.

## Closed

| id | step | outcome |
|----|------|---------|
| -- | sequential fallback deleted every credit-only recipient | fixed c0931aeb; cause of 35zzx attempt 1's BAD BLOCK; OPEN_ISSUES.md |
| -- | 35zzt: packet window 8 actually reaching the nodes | B 127.6k, standing best |
| -- | 35zzs: tx-bounded tail with a draining sealer | B 112.7k |
| -- | 35zzr: the fold outside the write transaction | B 89.1k -> 102.0k |
