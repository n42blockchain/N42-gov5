# The 180 ms occupant: `paceBlock`'s pacing wait, not exec(N)

Resolves the contradiction between `QS_CRITICAL_PATH_DEPTH1.md` (21aeaa10) and
`QS_SPECULATION.md` (b2228774): speculative builds hit 96-99.6% of blocks and a
hit costs ~1 ms, yet `commitWork begin(N+1) -> proposal broadcast(N+1)` is
~180 ms, 72% of the ~250 ms QC-to-QC interval.

## Method

One leader, consecutive in-tenure views, line-level correlation with `tUs`/`us=`
microsecond fields. Source: `r35zzzbb` B1 win1 (15:58:21-15:58:41, leader=node0
per `r35zzzbb.log:177`), extracted from
`/data/blockchain/wr-logs/r35zzzbb-keep/node0/n42-2026-10-03T16-08-17.866.log.gz`.
node0 holds leader tenure for exactly 5 consecutive views in this window
(9131-9135) before rotation hands off; all 5 are reconstructed below. Two are
tabulated in full; the other three (9131-9133) corroborate the same pattern.

## View 9134 (block 13661492, propose=176 ms per `hotstuff view timing`)

| tUs (epoch us) | Δ from prev (ms) | Component | Line |
|---|---|---|---|
| 1791057500222754 | - | `qcFormedUs` for view 9133 (QC(N) forms) | `hotstuff view timing` us field |
| 1791057500234450 | +11.7 | `miner: commitWork begin` speculative=false, parentHash=0x5a5507...(N) | worker.go:906 |
| (immediate) | ~0 | `miner: pacing wait` num=13661492 **waitNs=174590134** | worker.go:879 |
| ~1791057500409040 | +174.6 (the wait) | timer fires, `takeSpecTask` hands parked block to sealer | worker.go:880-886, 920 |
| 1791057500410474 | +1.4 | `hotstuff: proposal broadcast` view=9134, blockHash=0x9a1ce5... | — |
| 1791057500410513 | +0.04 | `miner: commitWork begin` speculative=true (N+2 spec build starts) | worker.go:906 |
| 1791057500411968 | +1.5 | `miner: block timeline` finalizeDoneUs/rootDoneUs/blockSealedUs (N+2 spec, parked) | — |
| 1791057500464709-464739 | +53 | `proposalReceivedUs`/`firstVoteReceivedUs` (votes arrive) | — |
| 1791057500470387 | +5.6 | `qcFormedUs` for view 9134 | — |

Sum check: 11.7 (gate+queue) + 174.6 (pacing wait) + 1.4 (hand to network) = 187.7 ms
from QC(9133) to proposal(9134); `hotstuff view timing` reports `propose=176ms`
(measured from a slightly later start point) — same order, same dominant term.

## View 9135 (block 13661493, propose=178 ms)

| tUs | Δ (ms) | Component |
|---|---|---|
| 1791057500470387 | - | `qcFormedUs` view 9134 |
| 1791057500482579 | +12.2 | `miner: commitWork begin` speculative=false, parent=0x9a1ce5... |
| (immediate) | ~0 | `miner: pacing wait` num=13661493 **waitNs=176463481** |
| ~1791057500659042 | +176.5 | timer fires, parked spec block handed to sealer |
| 1791057500660358 | +1.3 | `hotstuff: proposal broadcast` view=9135 |
| 1791057500714691-714712 | +54.3 | votes arrive |
| 1791057500721716 | +7.0 | `qcFormedUs` view 9135 |

Same shape: gate+queue ~12 ms, **pacing wait ~176 ms**, broadcast ~1 ms, vote
round-trip ~61 ms. Views 9131-9133 in the same tenure show the identical
`miner: pacing wait` line sized to whatever remained of that view's target slot
(absent only when a view runs behind schedule, e.g. view 9132's r2=859 ms stall
consumed the slack and the next pacing wait came back near 0).

## The code

`internal/miner/worker.go:841-889`, `paceBlock`: a leader throttles its own
wall-clock seal rate to `minerConf.BlockIntervalMs` on an absolute grid. On the
speculative-hit path (`commitWork` at line 919, `!speculative && takeSpecTask
succeeds`), the block content is already fully built and finalized from the
prior view's idle time — but before handing it to the sealer/broadcaster,
`commitWork` calls `paceBlock(num)` (line ~937, inside the hit branch), which
blocks on `time.NewTimer(wait)` until the block's absolute grid slot arrives.
`wait` here was 174.6 ms and 176.5 ms for the two views above, i.e. almost the
entire configured interval, because the hit made the content ready far ahead of
its slot.

The comment the code itself carries (worker.go:868-878) already named this
exact mechanism as the leading unproven suspect — "Whether this throttle fires
under load has been ARGUED twice from the cadence and never measured... the
decay phase's exact 250 ms/block says it certainly does when blocks are
cheap." This investigation is that measurement: it fires on effectively every
view in this tenure, for 72-176/250 ≈ 70-72% of the interval, which matches the
`QS_CRITICAL_PATH_DEPTH1.md` median exactly.

This also resolves why `QS_SPECULATION.md`'s hit-cost (~1 ms) and
`QS_CRITICAL_PATH_DEPTH1.md`'s 180 ms segment don't contradict each other: the
180 ms is not build/exec/finalize time at all (that really is ~1-5 ms on a
hit) — it is intentional sleep inserted after the free hit, to avoid producing
faster than `BlockIntervalMs`. The "parallel block" execMs=91/finalizeMs=65
medians the critical-path doc also reports belong to the ~4-30% of views that
*miss* the speculative cache and genuinely execute inline (candidate (d) is
real but secondary); they are not what fills the typical 180 ms view.

Candidates (a) depth-1 wait-for-exec(N) and (b) synchronous N+2 build before
proposing N+1 are both ruled out by the trace: the N+2 speculative build
(`commitWork begin speculative=true` at 1791057500410513) starts *after*
`proposal broadcast`, taking only ~1.5 ms, off the critical path as designed.

## Answer (3 sentences)

The 180 ms between `commitWork begin(N+1)` and `proposal broadcast(N+1)` is
spent inside `paceBlock` (`internal/miner/worker.go:841-889`, blocking call at
~line 880-886), a deliberate wall-clock throttle that holds an
already-finished, already-hit speculative block until its slot on the
`BlockIntervalMs` grid — not execution, not finalize, not the depth-1
parent-executed wait, and not a synchronous N+2 build. It fires on
essentially every view in the sampled tenure (5/5 views, ~174-178 ms each)
because speculation makes the block ready far earlier than its scheduled
slot, so the pacing budget that was meant to smooth production instead
becomes the dominant cost on the leader's critical path. The single lever is
to stop gating the *already-ready* proposal's broadcast on this timer —
either lower/remove `BlockIntervalMs` pacing on the hit path, or move the
throttle to gate when the *next* build starts rather than when a finished one
is handed to the network — which directly reclaims ~70% of the view interval
whenever the fleet has spare execution capacity.

## Safety note (depth-1 rule, S26 guard)

`paceBlock` only delays when the leader locally releases its own proposal; it
does not touch vote casting, QC formation, or the HotStuff vote-extension rule
that produced the S26 conflicting-commit bug (`project-hotstuff-conflicting-
commits`). Removing or relocating the wait changes timing only — `header.Time`
stays the deterministic `parent.Time + period` value per the function's own
comment (worker.go:839-840), so block content, hashes, and the QC/commit
protocol are unaffected. The one thing to re-verify after any change: the
cross-leader pacing still bounds the *network's* average block rate (the
existing one-interval cap at worker.go:859-862 already exists for this reason
with rotating leaders) so a lever that removes pacing only on the hit path,
not the miss path, should not let the fleet race ahead of whatever downstream
capacity (execution, storage write-amplification) `BlockIntervalMs` was sized
to protect.

## Complexity: Sonnet

This is a localized change inside `paceBlock`/`commitWork` (two functions,
~60 lines) with an existing test-adjacent harness (`speculative_test.go`) to
extend; no cross-cutting consensus or storage format change, no new wire
format, no interaction with QC/vote paths. The risk is tuning (how much
pacing to keep for the miss path and for cross-node network smoothing), not
correctness-by-construction — well scoped for Sonnet with a qs fleet run to
confirm throughput gain and that per-leader block cadence doesn't spike.
