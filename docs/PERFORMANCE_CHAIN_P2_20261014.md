# Performance-chain P2: crash recovery and strict throughput

Status: active. No new throughput score has been established. The Go S103
standing best remains 776,967 TPS; the historical Rust 4M run used gateway
attestation and is not a strict Ed25519 baseline.

## Whole-fleet termination exposed a separate defect

The retained P1 V=7/E=1 chain advanced to head 3762. SIGKILL of its execution
layer and all seven validators restored the durable EL head at 3721 while the
consensus vote log and QC remained near view 3764. The fleet could no longer
fetch the certified blocks: their bodies had only existed in memory. This does
not invalidate the two earlier successful normal shutdown/restart checks.

The original data and vote logs remain under
`/data/blockchain/n42-performance-chain/e1/data`; evidence is under
`/data/blockchain/wr-analysis/performance-chain-p2-20261014`. Missing legacy
bodies cannot be recreated by the new implementation.

## Repair and validation

Rust branch `perf/deferred-frontier-recovery-20261013`:

* `e52df8675`: bounded, fsynced proposal body store; ancestry replay through
  normal Engine API validation; live-upgrade snapshot; safe pruning after EL
  persistence.
* `d05a15be7`: leader and follower persist full bodies before voting; write
  failures stop the service before the execution event reaches consensus.
  Startup replays committed/locked QCs without lowering vote watermarks,
  serializes validators sharing an EL, and anchors the driver at the highest
  recovered lock.

150 service/library tests and 14 consensus-persistence integration tests passed.
The full workspace format check finds pre-existing formatting differences;
the new recovery module is formatted. The locked P2 release build passed after
removing stale cached codec artifacts from another checkout. A process kill is
not a power cut.

P2 E1 passed on clean `d05a15be7`: head 152, durable EL height 118, 34 blocks
replayed at startup, then head advanced to 156. All 64 prior successful receipts
and canonical hashes survived; 32 new transfers succeeded. Strict RPC and binary
ingest rejected tampered signatures. Seven validators agreed on 25 post-restart
views, and 60 imports corresponded to 60 distinct hashes with no takeovers or
repeated own execution.

The first E4 run stopped before SIGKILL because the test driver incorrectly
assumed EL directories node0..3; their actual first-validator directories are
node0, node2, node4, node6. `fd860bd1` corrects that and requires the canonical
crash head on every EL. Independently, EL3 stayed at genesis after missing the
first block: direct full-body pushes did not update peer heights, so range sync
never started. Rust `5b44a261f` fixes the height hint; a real TCP/libp2p test
verifies that a pushed child causes its missing parent to be pulled and imported.

P3 profiles preserve the P2 fixtures and match the 4.2G genesis gas limit to the
builder ceiling, with a 70 MiB gossip cap (`5c10a14f`). The former profile only
raised the builder ceiling; its genesis stayed at 480M, unsuitable for a claimed
200k-transaction block tier.

P3 E1 also passed (head 149, durable height 118, resumed above 153, 64 old
receipts preserved). P3 E4 reached SIGKILL/restart, but EL1 failed startup with
duplicate active keys. Its retained delta log shows a real branch switch: old
block 2, replacement block 1, replacement block 2, then block 3. Replay wrongly
treated the backward records as checkpoint coverage and retained the old
branch's active slots. Rust `e515c85ec` identifies coverage by exact checkpoint
hash/height/cursor and applies genuine rewinds. All 21 node-state recovery tests
passed, including branch rewinds, same-height replacement and interrupted
compaction. P4 will retry the retained E4 data and then run E7; no reset is
required or scheduled.

P4 restored the formerly failed EL1 data (head 118, parent result 117), and
replayed 34 durable bodies to 152. E1 again passed a whole-fleet SIGKILL check
(198/180 -> 203, 64 successful old receipts preserved). E4 exposed a second
catch-up issue: already persisted ELs only cached head/head-1 execution fields.
Re-submitting block 153 from the lagging EL therefore rejected it for missing
result(151), although canonical header 153 already commits that result.
Rust `c15390d57` installs a cold canonical-header reader: one consistent read-only
database transaction verifies ancestor identity, fork/depth and every intervening
parent link. It returns exactly the ancestor fields from its depth-D descendant,
and never inserts cold history into the live cache. Four registry/history tests
and the node build check passed. P5 will retry retained E4 and then E7.

Recovery requires full block bodies. Compact-only voting is explicitly refused
when a needed body is absent. A live upgrade cannot reconstruct an Amsterdam
access-list body solely from a header/transaction RPC response. Unsupported or
missing required recovery data stops startup rather than inventing a result.

`scripts/performance-chain/recovery_round.py` verifies strict RPC and binary
ingest, captures an unpersisted tail, kills only exact bundle PIDs, restarts
without resetting data, and compares 64 prior successful receipts and canonical
hashes. It then submits another 32 transactions. Results and rotated node logs
are retained; every exit stops the owned fleet.

## First strict E1 throughput observation

Clean `5b44a261f`, P3 bundle; ordinary Ed25519 ingest with
`N42_INGEST_VERIFY=all`, no gateway claims, frame blocks off, durable pre-vote
bodies on. One EL has 64 physical cores with SMT; seven validators share another
eight cores; the live signing generator has 56 cores. 4.2G gas blocks, 6,000
senders, 2,000,000 scattered recipients, 125 ms requested pacing. The generator
reported zero rejected submissions.

| Window | Committed-log TPS | Blocks / 15s | Mean occupancy | Measured cycle |
| --- | ---: | ---: | ---: | ---: |
| Warm-up | 146,667 | 11 | 81.8% | 1.364s |
| Window 2 | 146,667 | 11 | 100% | 1.364s |
| Window 3 | 133,333 | 10 | 100% | 1.500s |

Seven validators agree on 189 shared commit views. ImportOnce reports 195
imports for 195 hashes, 1,156 served requests, zero takeovers and zero repeated
own execution. **Provisional:** receipt sampling and the repeat leg remain
pending; this is not a peak-capacity claim. Evidence:
`/data/blockchain/wr-analysis/performance-chain-p3-20261014/strict-e1-a-result.json`.

Full-block builds show `fast=0`, `par_txs=0`, `core_layout=off`, roughly 480–520 ms
serial execution, 110–230 ms pool selection and up to 260 ms finish work. The
existing parallel transfer path is not enabled in this baseline. Next controlled
comparison: enable eligible plain transfers and parallel build with the required
pool puller, verify receipts and signature rejection, then separately compare
ahead building. No strict Ed25519 4M result has been demonstrated.

The user reiterated the prior approximately 770k score on 2026-10-14. Preserve
the exact S103 standing best, **776,967 TPS**, as the earlier Go seven-node
benchmark. The performance-chain optimization target is to exceed that figure
and then establish a million-level result under its own documented strict
signature and durability conditions. These workloads/signature paths must stay
labeled separately; the serial Rust E1 baseline does not replace S103.

## Unattended sequence

Independent E1, E4 and E7 profiles are prepared under
`/data/blockchain/n42-performance-chain/p3-20261014`. Run recovery checks first,
then strict-signature throughput windows. Report committed transaction rates,
occupancy, successful execution, identical validator commits, generator supply,
CPU allocation and persistence cost. Change one bottleneck at a time and compare
against the same signature and durability requirements. Commit and push each
logical source change separately.

Ethereum EL+CL, weekly updates, minimal/full/archive and Pro+DATC remain the
completed maintenance track. Development effort stays on the performance chain.

## P5 recovery follow-up and P6 queue

P5 E1 passed on retained data: head 251, durable frontier 229, recovered head
255; all 64 prior successful receipts and 96 new successful receipts checked.
P5 E4 exposed another startup gap: a lagging validator had saved a certified
commit received from peers without importing its body. Startup treated the
missing local body as fatal, preventing the peer synchronization needed to
obtain it. This is distinct from invalid local data.

Rust commit `c4ea27859` distinguishes unavailable ancestry from invalid recovery
input. Missing bodies allow networking to start from the actual EL head while
retaining the recovered consensus lock and vote watermarks. Corrupt bodies,
storage errors, bounds failures and rejected EL validation remain fatal. Six
recovery tests passed, including exact missing-ancestor classification before
any replay and fatal classification of an EL rejection; the validator example
also passed cargo check. The commit is pushed.

P6 builds the corrected binaries and retries retained E1/E4/E7 data in sequence.
Evidence: `/data/blockchain/wr-analysis/performance-chain-p6-20261014`.
The unattended runner releases its own hardware claim after cleanup and stops
on a failed case. E4/E7 recovery remains unverified until those rounds finish;
parallel-transfer throughput comparison follows recovery validation.

### P6 evidence and fetched/pushed ancestry fix

P6 E1 passed again on retained data: head 305, durable frontier 283, recovered
head 309; 64 prior and 96 new successful receipts verified. P6 E4 now starts
all validators, but its EL1 remains at height 152, so the pre-crash all-EL
canonical gate correctly fails. Logs show the certified child being fetched
without triggering a range pull: fetched and libp2p-pushed bodies did not update
peer height hints, unlike gossip and the named direct TCP channel.

Rust `4fe847d6c` updates these hints only after decoding valid matching bodies.
101 service tests passed, including a real loopback peer serving the missing
ancestry of a fetched certified child; pushed-body height bookkeeping is also
checked. The commit is pushed. P7 retries retained E1/E4/E7 after P6 cleanup;
artifacts are under `/data/blockchain/wr-analysis/performance-chain-p7-20261014`.

Before enabling throughput optimizations, the existing parallel-transfer suite
passed 34 tests (5 timing/large-workload tests ignored), and fast-transfer passed
10 semantic differential tests. These prove the covered execution cases, not a
new throughput score or completed E4/E7 recovery.

## P7 retained-data recovery matrix passed

Source `4fe847d6c`; all cases use strict Ed25519 and complete-fleet SIGKILL,
not a machine power cut. Each case preserves 64 previous successful receipts,
checks 96 new successful receipts, and checks the pre-crash canonical hash on
every configured EL. The retained E4 data from the failed rounds was repaired
through normal validation and peer synchronization, without resetting it.

| Topology | Before | Durable frontier | After recovery | Result |
| --- | ---: | ---: | ---: | --- |
| V7/E1 | 349 | 331 | 354 | passed |
| V7/E4 | 299 | 270 | 304 | passed |
| V7/E7 | 147 | 118 | 151 | passed |

Evidence: `/data/blockchain/wr-analysis/performance-chain-p7-20261014`.
The hardware claim was released after all three fleets stopped.

P8 starts a fresh serial/parallel V7/E1 throughput comparison using the same
P7 binary bundle, genesis, strict signature checks, CPU allocation and workload.
The checked-in `scripts/performance-chain/strict_throughput.py` collects three
15-second committed-log windows and samples 16 successful receipts from each
window after measurement. Parallel mode adds only fast eligible transfers,
parallel build and the required 4096-entry pool puller. All seven commit streams
and ImportOnce counters are checked. P8 evidence is under
`/data/blockchain/wr-analysis/performance-chain-p8-20261014`; results pending.

### P8 serial baseline verified

Serial windows: 133,333 / 146,667 / 146,667 TPS; full blocks in windows 2–3,
1.364 s cycle. All 48 sampled transactions drawn from the three measured
windows have successful canonical receipts. Seven validators agree across 214
common commit views. Runtime ImportOnce counters: 215 imports / 215 blocks,
1,280 served requests, zero takeovers and zero own-block re-execution. This
repeats the earlier approximately 147k serial baseline with receipt validation.
Parallel comparison is running; no higher score is established yet.

The P7 archived post-restart commit streams also agree at every observed view:
29 common views for E1, 29 for E4, and 27 for E7, with no conflicting views.
These checks are recorded as `commit-consistency.json` in each P7 case.

### P8 parallel result and next bottleneck

Parallel windows: **227,616 / 240,000 / 226,667 TPS**, with full blocks in
windows 2–3 and cycles 0.833 / 0.882 s. All 48 sampled measured transactions
have successful canonical receipts. Seven validators agree across 240 common
commit views; ImportOnce reports 241 imports / 241 blocks, 1,435 served requests,
zero takeovers and zero own-block re-execution. The round stopped and released
its hardware claim. This improves the strict serial baseline but remains below
S103's separate 776,967 TPS benchmark.

Full-block phase medians across the leg (including blocks outside the three
windows): serial pool 217 ms, execution 493 ms, build total 1,034 ms; parallel
pull 101.5 ms, execution 24 ms, graft 76 ms, fold 118 ms, finish 79 ms, build
total 514 ms. Fold includes graft and related work; these overlapping timers
must not be added as independent phases. Evidence: P8 `phase-comparison.json`.

P9 compares the existing address-sharded output path by adding only
`N42_OUTPUT_SHARDS=16` to the P8 parallel configuration. The harness preserves
strict signature verification, ordinary block bodies, journal durability and
receipt/consensus checks. Output-shard semantic tests run before the benchmark;
large child-process layout-equivalence checks are still running, so P9 has no
throughput result yet. Evidence: `/data/blockchain/wr-analysis/performance-chain-p9-20261014`.

### P9 sharded output: valid, no throughput improvement

All 17 output-shard tests passed (one ignored), including child-process layout
equivalence across fold/index/live modes. Sharded-output windows were
226,965 / 213,333 / 226,667 TPS; 48 successful measured receipts and matching
seven-validator commits verified. The full-block build median increased from
P8 parallel 514 ms to 569 ms, despite graft falling from 76 ms to 27 ms.
Additional shard preparation/folding work offsets the cheaper graft. This
single leg does not justify adopting shards as the performance profile default.
Its fleet stopped and hardware was released; source data remains intact.

P10 returns to the P8 parallel configuration and adds only `N42_BUILD_AHEAD=1`
to test pipeline overlap. Three ahead-build service tests passed; the strict
throughput comparison is now running. Evidence:
`/data/blockchain/wr-analysis/performance-chain-p10-20261014`.
The harness also suppresses helper bytecode writes to the source checkout and
preserves result evidence if fleet cleanup fails.

### P10 ahead build and P11 durability profiling

Ahead-build windows: 241,394 / 226,667 / 213,333 TPS, with 48 successful measured
receipt samples and 243 matching common validator commit views. No ImportOnce
takeovers or own-block re-execution. The late windows decline, so the warm-up
peak alone does not establish a pipeline improvement. P8 parallel remains the
control; ahead building is not selected as a new default.

P11 returns to P8 parallel and enables `N42_RECOVERY_PHASES=1`. Rust
`20c77a414` adds opt-in timings for header decode, journal lock, body write,
file fsync and atomic publication/directory fsync, retaining the same durability
operations before voting. Six recovery tests passed. The release build and
strict load profiling are queued under
`/data/blockchain/wr-analysis/performance-chain-p11-20261014`.

Inspection of the existing frame-block architecture found a separate recovery
requirement before reuse: `frame_blocks::root_for_body_counted` checks frame
roots from process-local layout records or the ingest queue. Those records are
absent on a cold restart, while an ordinary RLP body does not encode frame
boundaries. A frame-mode performance profile must persist and replay validated
layout metadata, including historical peer serving, before claiming the same
crash-recovery guarantees. No frame/gateway bypass was enabled in P8–P11.

### P11 measured journal cost and P12 decode-once control

P11 windows: 235,171 / 226,667 / 240,000 TPS; 48 successful measured receipts,
242 common matching commit views, zero ImportOnce takeovers or own-block
re-execution. The seven validators each wrote 56 bodies of approximately 32 MB.
Their journal total medians were 35.8–39.6 ms; write medians 8.6–9.7 ms, file
fsync 23.3–29.2 ms, directory publication approximately 0.7–1.3 ms. Header decode
was 8–9 microseconds and the local journal lock had no measurable wait. These
per-validator writes overlap; summing all seven medians would misrepresent the
vote delay. Journal durability is a cost, but cannot explain the entire roughly
300 ms gap between build time and block cycle. Evidence: P11
`journal-phase-summary.json`. Fleet stopped and hardware claim released.

P12 adds only `N42_BODY_ONCE=1` to the P8 parallel control, using the P11 binary
bundle with phase logging off. The existing execution-body tests passed 16
cases across unit/integration suites, covering raw-frame bounds, normal decode,
refusal fallback, no re-encoding after acceptance, recovery of an own commit,
and rejection without retry when an accepted body fails. Throughput and receipt
checks are now running under
`/data/blockchain/wr-analysis/performance-chain-p12-20261014`.

### P12 decode once verified; P13 sender queue control

Decode-once windows: 247,741 / 240,000 / 240,000 TPS, with 48 successful measured
receipts, 244 common matching commit views, zero ImportOnce takeovers and zero
own-block re-execution. The warm-up peak is not a steady-state score; late
windows hold 240k. The fleet stopped and released the hardware claim.

P13 tests `N42_TX_QUEUE=1` against P8 parallel, with body-once and ahead-build
off, keeping ordinary block roots and strict signature checks. The builder's
existing sender/nonce queue is fed from accepted pool transactions across RPC,
binary ingest and gossip. It tracks canonical mined nonce watermarks and is
intended to avoid repeated stale-nonce candidates (millions of refusals appear
in the pool-based phase counters). The full queue correctness suite runs first,
including prune/push/selection/untake races, then the same fresh-state three-window
throughput and receipt checks. Evidence:
`/data/blockchain/wr-analysis/performance-chain-p13-20261014`.
Queue tests are still running; no P13 throughput result is established.

### P13 sender-queue failure and late-refusal regression fix

Queue suite: 88 passed, 11 ignored. Runtime queue control regressed sharply:
11,212 / 1,752 / 253 TPS, almost all blocks empty or partial. Logs report
skipped sender runs and nonce holes (e.g. queue head 200 while account nonce 0).
The round stopped and released hardware. Only the first window's 16 sampled
receipts were verified before the old harness threw `StopIteration` when a later
window had no block above its 10k sampling threshold; this is not a passing
three-window correctness result. The harness now samples the largest nonempty
measured block and avoids duplicate samples on very small blocks, preserving
low-occupancy evidence rather than throwing an empty exception.

Source inspection found a concrete late-refusal bug in `payload::Puller`:
exhausting candidates dropped its iterator and refusal receiver before execution
finished, so later rejected candidates could not be returned. The deterministic
regression fails on the old code (late refusal count zero), and passes after
Rust `6c1617382`. An exhausted puller now closes the candidate stream but keeps
its iterator alive until build completion, drains final refusals, and then joins
and drops the iterator. Thirteen payload tests passed. This fixes a confirmed
loss condition; it does not yet prove all observed nonce holes are resolved.

P14 rebuilds and compares the repaired ordinary-pool parallel path with the
repaired sender queue using fresh roots and the same strict load/receipt gates.
Evidence: `/data/blockchain/wr-analysis/performance-chain-p14-20261014`.
The main release build is running; no P14 score is established.

### P14 repair validated; listener-backed queue still stalls

Release build completed in 5m57s at production source `6c1617382`. The additional
real-queue regression in test-only commit `199259ff2` passed: late rejected nonces
0–3 remain available when a subsequent build uses a different parent. This
checks transaction preservation as well as the earlier notification-level test.

Repaired ordinary-pool windows: 235,383 / 226,667 / 240,000 TPS, with 48 successful
measured receipts and 235 common matching commit views; zero ImportOnce takeovers
and own-block re-execution. The sender queue still regressed: 11,200 / 1,526 TPS
in the first two windows, then an empty measured window. Thirty-two measured
receipt samples passed before the empty-window gate stopped the round. Both
fleets stopped and the hardware claim was released. Fixing late refusals does
not fix the listener-backed queue's remaining nonce gaps.

P15 tests the existing direct queue admission (`N42_TX_INGEST_DIRECT=1`) against
P14 sender queue: transactions are cryptographically verified in strict `all`
mode before entering the queue, avoiding the lossy pool-arrival notification
feed. Frame blocks and gateway bypass remain off. All 65 transaction-ingest
unit/coverage tests passed. The fresh-state comparison adds post-window RPC and
binary-ingest bad-signature rejection checks alongside successful receipt checks.
Evidence: `/data/blockchain/wr-analysis/performance-chain-p15-20261014`.

The throughput harness now verifies bundle binary hashes and the prepared
genesis hash before starting, and records actual binary source provenance in
each result. P14 uses its recorded production commit, not the later test-only
HEAD. P15 runtime results are pending.

### P15 strict direct queue verified: 267–293k late windows

Direct admission passed: **280,000 / 266,667 / 293,333 TPS**, full blocks in
windows 2–3 (0.750 / 0.682 s cycles). All 48 sampled measured receipts succeeded;
post-window signature smoke verified bad-signature rejection at RPC and binary
ingest and new successful execution. Seven validators agree across 276 common
commit views; 277 imports / 277 blocks, 1,651 served requests, zero takeovers or
own-block re-execution. Source is the recorded P14 production bundle. The fleet
stopped and released hardware. This is a single verified V7/E1 leg, not a
seven-execution-domain score or proof of exceeding S103's 776,967 TPS.

Direct admission bypasses normal pool admission/storage, not individual
cryptographic signature verification or block execution validation. The result
supports the pool-arrival feed as a remaining queue problem; it does not make
the failing listener-backed mode safe to select as a default.

Full-block medians (68 builds across the leg): pull 100 ms, execution 25 ms,
graft 73 ms, fold 143 ms, overall build 390.5 ms. P16 changes only the sender
rotation run from 1 to 64 (`N42_TX_QUEUE_RUN=64`) on top of P15 direct admission.
The prior 88 queue tests cover multiple run lengths, and the repaired real-queue
late-refusal regression checks cross-parent nonce preservation. P16 retains
strict signature smoke, measured receipt samples and matching commit checks.
Evidence: `/data/blockchain/wr-analysis/performance-chain-p16-20261014`.
Optimized-mode restart and E4/E7 validation remain to be repeated before adoption.

### P16 contiguous sender runs and P17 optimized recovery passed

Run-64 windows: **320,000 / 306,667 / 293,333 TPS**, with full blocks in windows
2–3, all 48 measured receipt samples successful, and RPC/binary bad-signature
rejection plus new execution smoke passed. Seven validators agree across 277
common views; 278 imports / 278 blocks, 1,659 served requests, zero takeovers or
own-block re-execution. The late-window decline remains; this is not a stable
320k claim or a result above the earlier Go S103 benchmark. Fleet stopped and
hardware released.

The same optimized flags (parallel transfers/build, 4096-entry puller, strict
direct queue and run 64; ordinary block roots) passed retained-data whole-fleet
SIGKILL recovery in P17:

| Topology | Before | Durable | Recovered | Common post-restart views |
| --- | ---: | ---: | ---: | ---: |
| V7/E1 | 408 | 380 | 412 | 28 |
| V7/E4 | 355 | 330 | 360 | 27 |
| V7/E7 | 207 | 175 | 211 | 27 |

Each case preserved 64 old successful receipts on the primary EL, checked 96
new successful receipts across its smoke phases, and verified the crash-head
canonical hash on every EL. Archived seven-validator commits have zero
conflicting views. Hardware released after all three cases stopped.

P18 strengthens E4/E7 recovery evidence by verifying every old receipt and each
post-recovery smoke receipt on every EL, rather than relying only on primary
receipts and all-EL canonical hashes. The updated recovery harness checks both
canonical inclusion and receipt status. P18 retains P3 genesis/data and P14
binaries; evidence is under
`/data/blockchain/wr-analysis/performance-chain-p18-20261014`.
The P17 processes had already loaded the previous harness version, so these
additional all-EL receipt checks remain pending until P18 completes.

### P18 all-EL receipt recovery passed; P19 repeat and raw-body comparison

The optimized retained-data recovery cases passed the stronger harness:
E4 head 404 / durable 386 -> 408, with 256 old-receipt checks and 128 new-receipt
checks across four ELs; E7 head 268 / durable 234 -> 269, with 448 old-receipt
checks and 224 new-receipt checks across seven ELs. Each check verifies successful
status and the original canonical block hash. Both fleets stopped and released
hardware. Combined with P17 E1, optimized configuration recovery is now verified
for all three topologies; these remain process-kill tests, not power-cut tests.

P19 repeats P16 direct/run-64 as a fresh-state control and then adds only
`N42_BODY_ONCE=1` to compare raw-body handoff, with the same strict signatures,
ordinary roots, journal durability, measured receipts and post-window rejection
smoke. P14 binaries are verified against their recorded hashes before each leg.
Evidence: `/data/blockchain/wr-analysis/performance-chain-p19-20261014`.
Both P19 results are pending. Data from previous rounds remains retained;
approximately 147 GiB filesystem space was free before these two new legs.

### P19 control reproduced; raw body handoff reached 333k

Repeated run-64 control: 320,000 / 306,667 / 306,667 TPS. Raw-body handoff:
**333,333 / 320,000 / 333,333 TPS**, full blocks in the late windows and
0.600 / 0.625 s cycles. Each leg verified 48 measured receipt samples plus
post-window signature rejection/new execution smoke. Matching common validator
views: 292 control, 286 raw; zero ImportOnce takeovers or own-block re-execution.
Both fleets stopped and released hardware. This supports a modest handoff gain;
it remains below S103 and has not demonstrated strict-signature 4M throughput.

P20 Rust `a4b8dfab4` adds opt-in transaction-root worker isolation through
`N42_TX_ROOT_THREADS=<1..128>`. Absent/invalid values retain the global Rayon
pool. The same encoding and ordered-MPT implementation runs inside the optional
pool; consensus roots and header rules are unchanged. Worker tasks share the
EL CPU affinity with other work, but have a separate task queue from signature
verification. Four existing root/encoding equality tests passed with 16 workers,
including every key-shape boundary and residue sweeps (one exhaustive test
ignored in that first run).

After the main release build, the P20 runner executes the exhaustive four-byte
key sweep in release mode, then compares raw-body control against the same
binary with 16 dedicated root workers. The harness requires the worker-pool
activation log before accepting the result. Evidence:
`/data/blockchain/wr-analysis/performance-chain-p20-20261014`.
The build is live; exhaustive test and performance results remain pending.

### P20 results and P21 high-load cold-recovery failure

The exhaustive four-byte-key comparison passed all 320 roots in release mode
(12.57 s). P20 raw control: 333,333 / 333,333 / 253,333 TPS; dedicated-root pool:
333,333 / 320,000 / 320,000 TPS. Both legs checked 48 measured receipts, signature
rejection and matching commits. Runtime activation of the 16-worker pool was
observed. The control's late regression differs from P19, so this pair does not
establish a consistent worker-isolation gain; the setting remains opt-in.

Retaining every test datadir left roughly 58 GiB free. The new offline utility
checks completed V7/E1 provenance, no live PIDs, regular same-owner files and
exact byte equality before atomically replacing a duplicate with a hard link.
It verifies the linked SHA-256 and fsyncs the directory, retaining every original
path and body byte. P20 root-pool dry run found no mismatches. P21 applied this
only to that stopped case: 480 duplicate paths, 15,291,961,344 allocated bytes
released. Mutation is limited to recovery-body files; DB and QMDB files are not
part of deduplication. Evidence: P21 `dedup-apply.json`.

The subsequent cold-start check exposed an unresolved high-load recovery gap.
Reth storage consistency healing rolled static-file heads from 293 to database
stage 177; QMDB's compacted heap checkpoint is at 199. Startup asks for the root
of canonical parent 176 and fails before QMDB initialization:
`checkpoint 199 ... has no matching parent result for 176 ...`.
Read-only `db stage-checkpoints get` confirms every stage, including Execution
and Finish, at 177. The bounded frontier file contains checkpoints 132, 143,
163 and 199; its debug-only inspection is not used as trusted recovery input.
The recovery round failed before its intended SIGKILL, stopped owned processes,
and released hardware. The original datadir and all deduplicated bodies remain.

This demonstrates that small-transaction recovery passes in P17/P18 do not prove
cold recovery after the 15M-transaction throughput workload. Online 333k remains
a measured score, not a completed high-load durability result. Current priority:
retain checkpoint/log history until the EL's durable database frontier covers
it, inspect the existing canonical-prefix/body coverage, and restore the retained
P20 case through validated history without inventing roots or resetting data.
Do not expand fresh throughput legs or adopt the optimized profile as complete
until this failure is repaired and the 48 earlier measured receipts survive
cold start and process-kill recovery.

### Heap rollback-history preservation implemented

Rust `f18b4c60a` preserves the overwritten heap checkpoint and sealed delta log
as an immutable, durably published pair before compaction removes the originals.
Historical-root reads can replay these pairs to an exact requested number/hash.
Startup with a newer heap checkpoint can reconstruct the database's older head,
archive the displaced checkpoint/current/sealed logs, and atomically normalize
the active checkpoint before validated body replay resumes. No root is guessed
and consensus vote/lock watermarks are unchanged.

Archive retirement reads the actual MDBX `Finish` stage through a registered
read-only callback. Unknown durability retains history. Folded archives remain
until the durable boundary covers their parent frontier; rollback forensic
archives are retained. The regression uses different account nonces/balances:
checkpoint 4, database rollback to 2, historical parent root 1, then a second
restart at 2, plus archive-retirement boundary checks. All 22 node-state tests
passed; the main binary passed cargo check.

This first repair covers heap checkpoints used by the performance profiles.
Entry-file rollback archiving is not implemented by this patch. The already
stranded P20 case predates these archives, so this code cannot recreate its
missing checkpoint/log history retroactively. Its canonical prefix and durable
proposal-body coverage still need inspection and a validated replay repair.
No high-load recovery success is claimed; release/runtime validation and the
retained P20 repair remain required work. Keep throughput expansion deferred.

### Rollback archive review follow-up

Rust `cc86e7bcf` copies and fsyncs the mutable current log when archiving a
rollback. Hard links remain restricted to immutable checkpoints and sealed
logs. Each publication has a unique directory, so repeated rollback attempts
at the same height retain their separate histories. The new regression changes
the live log after two archives and verifies both earlier byte sequences remain
intact. All 23 node-state tests passed (51.96 seconds). This is source-level
evidence; release/runtime recovery validation remains pending.

The standing self-developed seven-node reference remains S103/r139 at
776,967 TPS. The Rust strict-verification online result of approximately
333,333 TPS uses a different workload and shared-execution configuration;
its unresolved high-load cold-recovery failure prevents treating it as a
durable performance baseline.

### Retained P20 journal coverage and P22 validation started

A read-only prefix inventory of every retained recovery body found contiguous
header heights 117–287 on node0 and 117–293 on nodes1–6. Parent references whose
filename was present had consistent consecutive heights. Evidence:
`performance-chain-p21-20261014/journal-header-inventory.json`. This structural
scan reads RLP header prefixes and checks the outer length against file size;
it does not verify Keccak header hashes, transaction roots, certificates, or
execution. It identifies candidate history for repairing the durable database
head at 177, rather than proving recovery.

P22 is building the joint release from `cc86e7bcf` and will run the existing
retained-data E1/E4/E7 recovery matrix with strict verification and direct
sender queues. Its runner acquires the hardware claim only after successful
build and clean-source checks, stops on failure, and retains all data. The
matrix uses small transaction samples and cannot prove the unresolved P20
high-load recovery requirement. Evidence and binary SHA256 provenance are
written under `performance-chain-p22-20261014`.

### P22 retained-data recovery results

Joint release `cc86e7bcf` completed in 6m25s; all three strict Ed25519 recovery
cases passed. Each case killed its whole owned fleet with SIGKILL, restarted
the retained datadir, preserved the crash-head canonical hash and 64 earlier
successful receipts, and accepted 96 subsequent transactions.

| Configuration | Before / durable / after height | Old receipt checks across ELs | New receipt checks across ELs |
| --- | --- | --- | --- |
| V7/E1 | 462 / 441 / 466 | 64 | 32 |
| V7/E4 | 460 / 435 / 464 | 256 | 128 |
| V7/E7 | 323 / 293 / 327 | 448 | 224 |

Evidence: each P22 topology's `result.json`, logs, profiles, and the joint binary
SHA256 manifest. The runner completed and released its hardware claim. These
are process-kill tests with small transaction samples, not power-cut tests or
a repair of P20's 48 retained throughput receipt samples. High-load recovery
and validated reconstruction of P20 remain open.

A separate read-only storage query confirmed P20's canonical height-177 hash
matches the corresponding journal filename. Node1's candidate height-293 body
links back to it over 116 consecutive parent references. Evidence:
P21 `header-177-readonly.log` and `journal-durable-head-link.json`. Hash and
execution validation of that candidate journal chain remain pending.

Rust adds `bin/n42/examples/export_recovery_prefix.rs` to open source storage
read-only and export heights 1 through a requested durable `Finish` boundary
into a new directory outside the datadir. It checks canonical header hashes,
ancestry and transaction roots, fails on unsupported access lists, and leaves
execution validation to replay into a separate EL. The completion manifest is
written only after the entire prefix succeeds. The original P20 datadir and
consensus watermarks remain the source of record.
