# QS cycle anatomy — round 35zzzax, B1 (n42-r107, apply unset)

Source: `/data/blockchain/wr-logs/r35zzzax-keep/node{0..6}/*.gz`, B1 leg,
windows win1 (20:09:47-20:10:47, 104 blocks, 0.577 s/block, ~83k tx/block)
and win2 (20:10:47-20:11:47, 86 blocks, 0.698 s/block, ~92k tx/block), per
`docs/QS_QUEUE.md` S72. Stats are medians / p90 across every leader's
"miner: build phases"/"miner: commit phases" line and every follower's
"blockimport phases"/"blockwrite phases"/"qmdb root phases"/"hotstuff:
commit phases"/"hotstuff: leader gate phases" line that falls in the
window, all 7 nodes pooled (tenure 8 rotates the leader through all of
them within a window).

## Medians (ms) / p90 (ms), n = sample count

| phase | win1 median/p90 (n) | win2 median/p90 (n) |
|---|---|---|
| leader build.total | 148.5 / 593.0 (85) | 558.8 / 726.7 (73) |
| leader build.fillTx | 147.0 / 578.6 (85) | 454.9 / 689.1 (73) |
| leader build.reload | 0.63 / 24.1 (85) | 0.82 / 130.8 (73) |
| leader commit.commitNs | 34.0 / 135.3 (85) | 125.4 / 153.9 (72) |
| follower blockimport.total | 141.7 / 747.6 (564) | 416.5 / 859.3 (405) |
| follower blockimport.exec | 52.4 / 317.8 (564) | 225.1 / 416.0 (405) |
| follower blockimport.proc | 94.9 / 510.6 (564) | 332.2 / 632.4 (405) |
| follower blockimport.root | 31.4 / 143.6 (564) | 118.6 / 171.9 (405) |
| follower blockimport.write | 32.7 / 187.1 (564) | 81.1 / 192.2 (405) |
| follower blockwrite.total | 43.7 / 200.5 (621) | 147.0 / 216.7 (465) |
| qmdb root apply (both roles) | 23.5 / 31.9 (422) | 26.1 / 34.3 (380) |
| hotstuff commit.total | 0.32 / 12.6 (625) | 0.36 / 48.0 (462) |
| hotstuff leader-gate.total | 0.05 / 0.08 (86) | 0.06 / 0.08 (72) |

`finalize` is not logged separately on these blocks (the field exists in
the schema for parallel-apply blocks only, S72; apply was unset in B1 so
every block's `finalize` sample is empty — `collect` is populated and
small, 1.0/3.9 ms median).

## (1) Critical path

Named, logged phases sum to only ~50% of the 577 ms win1 block time
(leader build 148.5 ms + follower import 141.7 ms = 290 ms) and ~81% of
the 698 ms win2 block time (558.8 + a further ~130 ms of import that is
not fully serial with the build — see (2)). The remainder is QC
round-trip / vote propagation time that no phase in this round's logging
captures directly (`hotstuff: commit phases` and `leader gate phases` are
both sub-millisecond — they time the *local* application of an already-
formed QC, not the vote collection that forms it). This matches the
standing, unresolved residual from S12b/6cc: a ~500 ms gap on hand-over
that the existing instrumentation does not explain. win2's named phases
close most of the gap because fillTx alone (455 ms median) approaches the
full block time, so there the leader's own build is most of the critical
path; win1 still has a ~290 ms unexplained remainder. Chain, in order:
**leader fillTx -> leader align/commit -> propose -> vote round-trip
(unmeasured) -> QC -> follower blockimport (exec+root+write) -> next
leader's fillTx**.

## (2) Largest phase / overlap

The largest single phase in both windows is the leader's `fillTx`
(147 ms win1, 455 ms win2). Under depth-1 deferred execution the
follower's execution of block N-1 is supposed to overlap the leader's
fill of N. The medians say that overlap holds loosely in win1 (fillTf
147 ms vs follower blockimport.total 142 ms — almost exactly matched,
consistent with the two running in parallel and the smaller one being
hidden) but breaks down in win2: fillTx (455 ms) now dwarfs
blockimport.total (416.5 ms) on its own, so even with perfect overlap the
leader's build is the pacing phase, not the follower's execution. Given
block time only grew 577 -> 698 ms (121 ms) while fillTx alone grew
308 ms and blockimport.total grew 275 ms, the two are **not** adding
serially (290 -> 974 ms would have been the serial sum); they remain
substantially overlapped, but the pipeline's slack is gone in win2 — it is
compute-bound on the larger of the two legs (the leader build) rather
than idle-bound.

## (3) Leader build composition

win1 fillTx (147 ms) is essentially all of build.total (148.5 ms);
win2 fillTx (455 ms) is 81% of build.total (558.8 ms). `fillTx` is the one
field the build-phase log reports for transaction selection + EVM
execution + finalize combined — it is not split further in this round's
logging (`exec`/`finalize` sub-fields only appear in `blockimport phases`,
i.e. on the follower side, and only the follower's `root` field is
separately available). The remaining ~1-104 ms of build.total is
`align`+`reload`+`syscalls` (persist-wait is 0 — no stall there). So the
available breakdown answers "how much of the leader side is EVM execution
vs everything else" only indirectly: on the follower, running the same
~83k-92k tx block, `exec` is 52 ms (win1) / 225 ms (win2) of a
~142/416 ms import, i.e. EVM execution is 30-37% of the follower's known
total (import.proc, at 95/332 ms, covers exec+root and is the better EVM+root
proxy at 67% of blockimport.total in both windows). If the leader's fillTx
scales the same way, pool selection/ordering is the majority (63-70%) of
fillTx, not EVM execution — consistent with S67/S49's standing finding
that pool pressure (not EVM cost) is the leading edge of block-build time.
This is an inference from the follower's split, not a direct leader-side
measurement; the build-phase log does not currently break fillTx into
exec/select/finalize.

## (4) What grows from win1 to win2

| delta (win2 - win1) | ms |
|---|---|
| leader fillTx | +308 |
| follower blockimport.exec | +173 |
| follower blockimport.proc | +237 |
| follower blockimport.root | +87 |
| follower blockimport.write | +48 |
| follower blockwrite.total | +103 |
| qmdb apply (either role) | +2.6 (noise) |

Every growing phase is on the "do the work" side (fillTx, exec, root,
write); the per-block QMDB apply step itself barely moves (+2.6 ms,
confirming S72: apply was never the bottleneck). Tx count per block only
rose ~11% (83k -> 92k) while fillTx rose 109% and exec rose 329% — so
win2's slowdown is not explained by tx volume alone. The likely driver is
mempool/pool depth: win2 runs 75-135 s into the flood, by which point the
600k/200k pool carries far more pending+queued transactions for selection
to scan and order, inflating `fillTx` out of proportion to the executed
tx count, and the now-larger committed state (more touched accounts per
block from a bigger live set) inflates `exec`/`root`/`write` similarly.

## Ranked levers (no code)

1. **Leader fillTx (147 -> 455 ms, the single largest and fastest-growing
   phase).** Lever: pool pre-ordering / a cheaper selection structure that
   does not re-scan the full pending+queued set per block (S67/S49's
   standing lead) — or a speculative build that starts fillTx against the
   predecessor's expected state before the parent's QC lands, so pool
   selection cost is paid off the critical path.
2. **Follower exec+root+write, bundled in `blockimport.proc`
   (95 -> 332 ms).** Lever: pipeline the state-root computation (`root`)
   with the trailing part of `exec` instead of running them back-to-back
   per block — i.e. root pipelining, so a grown exec no longer drags root
   serially behind it.
3. **The unmeasured vote/QC round-trip** (the ~290 ms win1 residual not
   covered by any logged phase). Lever: this is a measurement gap, not a
   compute cost yet — the next step is new timestamps bracketing vote send
   / QC formation (the S12b-style millisecond stamps), not a code change,
   before a lever can even be proposed with evidence.
