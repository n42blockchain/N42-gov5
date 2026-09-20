# QS queue (commander's board, 2026-09-20)

Standing goal: the seven-node qs fleet's TPS. Scored metric: the B mean.
Standing best: **B 127.6k** (35zzt, n42-r80 = deferred + fold outside the
writer + tx-bounded tail + packet window 8). Protocol: QS_AGENT_PROTOCOL.md.

| id | step | binary | prediction | status | result |
|----|------|--------|-----------|--------|--------|
| S1 | 35zzx: sixteen generators x 500 senders -- restore supply (37% occupancy at 35zzt) | n42-r84 | 77 (6bq): occupancy >60%, B > 127.6k, no BAD BLOCK | running | |
| S2 | 35zzw: per-block base-read cache (parallel.BaseCache) | n42-r85 | 78 (6br): follower `proc` -10% on full blocks | queued | |
| S3 | why the leader's build fell back (9064 aborts on 23000 txs): a fallback costs the block its parallelism and used to corrupt it | spec first | 79 (6bs) | spec written, cause open | the build's own reader and the executor's per-worker reader disagreed about a deep-nonce sender's account (buildReaderNonce 4500 vs worker-seen state 0); Block-STM cannot tell that miss from a resolvable conflict and burns all 64 waves finding out; prediction 79 not yet tested against a fresh wave-limit exhaustion |
| S3b | do the routine nonceHigh drops (9200/32200, 13500/18500) also come from the reader disagreement, or are they genuine send-ahead | logs only | 80 (6bt) | done | falsified as genuine send-ahead: both blocks are bug-driven, but not the same bug -- 13659302 is the live reader race (S3/pred 79), 13659303 is downstream of the now-fixed c0931aeb corruption (13659302's fallback wiped 17,036 accounts, so 13659303's build reads the same already-emptied state from both readers, which only looks like agreement); no other build in the log (0 of ~991) dropped any candidate to nonceHigh, so "routine" does not describe this log at all |
| S4 | the Prague delegation check reads every recipient (6bp) | not built | not written | candidate | |
| S5 | the leader's write (~0.5 s) off the critical path | not built | not written | candidate | |
| S6 | per-transaction allocation hotspots (receipt, AsMessage, journal dirties, IBS.Reset maps) | not built | not written | candidate | |

## Closed

| id | step | outcome |
|----|------|---------|
| -- | sequential fallback deleted every credit-only recipient | fixed c0931aeb; cause of 35zzx attempt 1's BAD BLOCK; OPEN_ISSUES.md |
| -- | 35zzt: packet window 8 actually reaching the nodes | B 127.6k, standing best |
| -- | 35zzs: tx-bounded tail with a draining sealer | B 112.7k |
| -- | 35zzr: the fold outside the write transaction | B 89.1k -> 102.0k |
