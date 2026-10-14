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
