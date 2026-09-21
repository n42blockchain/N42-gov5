# QS queue (commander's board, 2026-09-20)

Standing goal: the seven-node qs fleet's TPS. Scored metric: the B mean.
Standing best: **B 127.6k** (35zzt, n42-r80 = deferred + fold outside the
writer + tx-bounded tail + packet window 8). Protocol: QS_AGENT_PROTOCOL.md.

| id | step | binary | prediction | status | result |
|----|------|--------|-----------|--------|--------|
| S1 | 35zzx: sixteen generators x 500 senders -- restore supply (37% occupancy at 35zzt) | n42-r84 | 77 (6bq): occupancy >60%, B > 127.6k, no BAD BLOCK | falsified | falsified (6bu): B mean 47.0k vs 127.6k standing best; occupancy fell to 8.5% mean, not >60%; no BAD BLOCK/divergence, but 23 blocks in a 37 s window of B2 dropped 183,282 candidates to nonceHigh (up to 99.9% of one block) as the sixteen generators ran dry -- the acceptance note's mass-drop trip, so the result cannot be credited as supply-restored. One-line reason: the "restore supply" premise was never tested -- 6bv shows the funded supply was identical (36,000,000 tx/leg) in both rounds; the doubled generator count doubled the aggregate `-target-depth` instead, and the round failed at ~16% of its budget spent, not exhaustion |
| S2 | 35zzw: per-block base-read cache (parallel.BaseCache) | n42-r85 | 78 (6br): follower `proc` -10% on full blocks | falsified | falsified (6bw): B mean 96.1k vs 127.6k standing best (-24.7%), on 35zzt's own eight-generator shape so it is a direct comparison; every B window ran 40-69% slower per block than 35zzt's; occupancy was up (48.9% mean vs 35zzt's 37% steady-state), so fuller blocks are what got slower. The literal `proc` before/after delta could not be checked: n42-r84's full-block `proc` was never captured and its logs had already rotated out by the time this round finished, so BASELINE proc is n/a, not an estimate -- only r85's own number is on record (proc median 988 ms, p90 1131, n=1722 across the B legs, execMs 717 of it). nonceHigh drops 0 and no BAD BLOCK/divergence (acceptance conditions both clean). Ruled falsified on the measured half of the prediction (B mean must rise; it fell), not on the unmeasurable half |
| S3 | why the leader's build fell back (9064 aborts on 23000 txs): a fallback costs the block its parallelism and used to corrupt it | spec first | 79 (6bs) | spec written, cause open | the build's own reader and the executor's per-worker reader disagreed about a deep-nonce sender's account (buildReaderNonce 4500 vs worker-seen state 0); Block-STM cannot tell that miss from a resolvable conflict and burns all 64 waves finding out; prediction 79 not yet tested against a fresh wave-limit exhaustion |
| S3b | do the routine nonceHigh drops (9200/32200, 13500/18500) also come from the reader disagreement, or are they genuine send-ahead | logs only | 80 (6bt) | done | falsified as genuine send-ahead: both blocks are bug-driven, but not the same bug -- 13659302 is the live reader race (S3/pred 79), 13659303 is downstream of the now-fixed c0931aeb corruption (13659302's fallback wiped 17,036 accounts, so 13659303's build reads the same already-emptied state from both readers, which only looks like agreement); no other build in the log (0 of ~991) dropped any candidate to nonceHigh, so "routine" does not describe this log at all |
| S4 | the Prague delegation check reads every recipient (6bp) | not built | not written | candidate | |
| S5 | the leader's write (~0.5 s) off the critical path | not built | not written | candidate | |
| S6 | per-transaction allocation hotspots (receipt, AsMessage, journal dirties, IBS.Reset maps) | not built | not written | candidate | |
| S7 | 35zzx retry: same 16-generator harness, `-target-depth` halved 45000 -> 22500 so the fleet's aggregate in-flight target returns to 360,000 (35zzt's proven number) instead of the doubled 720,000 S1 actually ran at | n42-r84 | 81 (6bv): every B window >=25% occupancy, B mean > 127.6k, no B-leg block drops a double-digit percent of candidates to nonceHigh with fallback:false | falsified | falsified (6bx): B mean 86.2k -- above 35zzx attempt 2's 47.0k (+83%) but 32.4% below the 127.6k standing best. All four B windows land below the 25% occupancy floor (19.4-22.8%, mean 21.1%), including a sharp B1 win2 collapse (17 blocks/60s, blockTime 3.529s vs win1's 0.645s). 20 committed B-leg blocks drop a nonzero nonceHigh share (63,700 candidates total, two ~20s bursts at each leg's flood ramp), six of them at 99.98% (candidates==failed==4491), all fallback:false/lenient:true/waves:1 with no preceding wave-limit exhaustion -- clause 3 fails outright. No BAD BLOCK/divergence/MODE-FAILED/watchdog; 8 view-timeouts inside the B legs cluster with the drop bursts. Halving target-depth alone did not remove 6bv's synchronized funding-gate inrush (all seven nodes' memory jumps ~1.7GB->~9-10GB in under a minute); the funding gate itself is still untouched. One-line reason: all three clauses of prediction 81 fail independently, though the depth-target change is a real partial win over 35zzx attempt 2 |
| S8 | audit: is a block serialized twice on the vote/import path, as n42-rs found (107 ms, 29% of its cycle)? | static audit | none (audit) | done | falsified for the n42-rs mechanism (handover: Duplicate serialization audit, 31a25647): the proposal carries only BlockHash+TxRootHash, the push/gossip encode is shared across peers, tx hashes reuse the cached wire bytes, commit-to-canonical reuses the decoded block. One confirmed second pass remains on the WRITE path: `encodeTxForStorage` re-serializes all 163k txs into the compact storage record although `tx.enc` is cached (35zy: 118 ms of a 213 ms write; halved by the parallel encode in 35zzb). It is after the vote on a follower, so it belongs with S5, not ahead of it |
| S9 | push/gossip race: `rpc_block_push.go` registers `pushInflight` only after its full decode (line 49), so gossip's peek (`validate_blocks.go:47-61`) can miss it and decode the same 26 MB block again | logs only first | not written | candidate | suspected, never observed live; count double decodes per B leg from existing logs before building anything |

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
