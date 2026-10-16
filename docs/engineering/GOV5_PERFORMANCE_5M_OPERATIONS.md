# Gov5 5M unattended campaign

Updated 2026-10-16. The user authorized continuous work, small commits/pushes,
and alternating hardware use with the other session. The implementation and
evidence history is in [the main plan](GOV5_PERFORMANCE_5M_PLAN.md).

## Scope and measured state

Optimize the isolated **Go gov5** performance chain from the historical
776,967 executed TPS toward 5,000,000. Read `../n42-rs` as an architectural
reference; Rust throughput is not a Go result. Keep the original seven-node
chain and completed ETH EL+CL system separate. E1 retains seven consensus
identities but trusts one Go executor; it is a common execution failure domain.

The first two real eight-process attempts failed before a canonical executed
transaction was observed. The Unix HTTP authority correction is committed.
The cold-validator transaction-root initialization fix is `ee83d5bf` on the
main performance branch, with the minimal baseline repair at `fb1671a4`.
The third attempt passed on the original `f1015a` data: seven validators
converged at height 98 with exactly eight committed/executed transactions.
A subsequent normal restart advanced all seven from height 98 to 133 with
count eight and empty queues. See [the evidence record](GOV5_E1_FIRST_FUNCTIONAL_20261015.md).
These are functional results, not a throughput measurement. Offline Ed25519
codec changes remain untested work in progress and did not enter either run.

## Hardware turns

Follow `/data/blockchain/wr-logs/BOX-CLAIM-PROTOCOL.md`: three quiet samples
30 seconds apart, check both claim locations and live workloads, claim both
locations exclusively, wait the contention interval, and recheck before work.
Keep the campaign's 90 GiB launch floor. Never remove another driver's claims
or stop its processes, even if a timestamp appears old. Preserve runtime data.

An occupied machine means wait and do read-only review or small Python work;
do not compile, run Go tests, or benchmark beside the other fleet. Our claim
must be released when the bounded turn finishes, including on errors. The next
turn must obtain ownership afresh. Build before measurement windows and never
between the legs of a measured round. Treat the first measurement leg after a
machine-state change as warm-up. Do not keep a claim while analyzing results.

The existing protocol provides exclusive access, not a FIFO queue or proof that
the other driver has acknowledged a handoff. A waiting log alone does not prove
fair rotation. If repeated foreign rounds prevent the three quiet samples,
record that condition and arrange a handoff through an authorized coordination
channel; do not claim over the running fleet or silently weaken admission.

Resource contention before any attempt is created can be retried. Once an
attempt starts or mutates chain state, a failure requires evidence review and a
new explicitly named recovery attempt; never blindly replay the batch.

## Acceptance sequence

1. Run the frozen minimal repair's targeted race tests and rebuild only `n42`.
   Preserve source commits, binary SHA-256 values and command logs. Reuse the
   unchanged baseline owner and functional-checker binaries only after their
   hashes match the recorded provenance.
2. Retry the existing `f1015a` chain, with the same genesis and data, in a fresh
   attempt log directory. Do not initialize, reseed, reset, or change genesis.
   Require all seven validators to converge on a nonzero canonical head and
   exactly eight unique committed-and-executed transactions. The validators'
   counters are not summed; this seven-validator fixture counts eight, not 56.
   This is a functional check, not a TPS measurement.
3. Record the successful head/count, release hardware, then verify a normal
   restart on those same nonzero data. Require monotonic canonical evidence,
   unchanged transaction count without new load, and agreement after catchup.
   The original checker requires an initial count of zero and cannot prove
   this step. A later new batch must use correct sender nonces and compare the
   count increment with the new unique committed transactions.
4. Exercise bounded faults on owned processes: two validators unavailable
   still permit the required five votes; three unavailable cannot form five;
   owner disconnection must not produce unsupported execution evidence. Restore
   the owned processes and check convergence without duplicate execution or
   double counting. Record the expected behavior before each fault. Do not
   treat failure-free unit tests as proof of these OS-process outcomes.
   The user also authorized an earlier, separate finite-corpus diagnostic
   load after normal restart. It uses a fresh workload fixture and cannot
   substitute for these fault gates or establish a promoted capacity baseline.
5. Only after these gates, establish a reproducible Go E1 throughput baseline.
   Fix workload, block size, pacing, signatures, executor count and hardware
   allocation. Record committed-and-executed unique transactions, execution
   lag, root/receipt/gas consistency, CPU, memory, I/O and major faults. Use a
   warm-up and matched control/candidate/control legs. Publish losses as well
   as gains; stop a bad candidate rather than promoting a peak window.
6. Change one measured bottleneck at a time: own-build reuse, root replay,
   shared execution transport, signature/admission costs, frames, and pipeline
   depth are separate candidates. Enable no unvalidated protocol feature to
   improve a number. Use phase evidence to choose the next change, and require
   functional/fault regressions plus matched measurements before promotion.

At 200,000 transactions/block, 5M requires an effective 40 ms committed-and-
executed cycle; at 163,000 it requires 32.6 ms. Seven-process agreement, isolated
microbenchmarks, queued transactions and Rust figures do not satisfy this gate.

## Persistence and reporting

### Queue integration checkpoint — 2026-10-16 10:57 UTC

The first actual strict owner-queue eight-process gate passed on the original
`q1016a` data in attempt `startup-qc`. Earlier attempts `q1` and `snapshot-retry`
failed and remain retained. The startup recovery source is clean `757c2ded`;
claimed targeted race, full Make build and all three binary builds passed.
All seven initial metadata heads had height one/count zero. Recovery reached
height two/count zero before sending; final seven and owner agreed at height
15/hash `d4a2455d7dd4e632dabf0ac596eaf6a8daa98938192796c769fab0d21884e7d3`
with exactly eight unique committed-and-executed transactions and all four
owner queue credits zero. CryptoVerified 64 counts attempts, including 36
known-zero snapshot retries, rather than unique transactions.

The unique recovery worker PID 1256875 completed and released its claims.
Its controller `/tmp/gov5-ownerqueue-startup-757c2ded.py` and validation directory
`/data/blockchain/gov5-work/validation/20261016-gov5-ownerqueue-startup-757c2ded/`
record commands, source/binary hashes and cleanup. Actual process evidence is
under `runtime/q1016a/attempts/startup-qc/`; every owned process exited zero.
See [the recovery record](GOV5_OWNERQUEUE_Q1016A_RECOVERY.md) for precise limits.
This specific restart recovery does not replace the remaining process fault
gates, and eight transactions do not establish a chain throughput result.

The separate signed-corpus runtime, strict replay client and guarded launcher
are tested and pushed, frozen at `df691d4f`; the startup source `757c2ded`
also contains those unchanged files. Next build only the replay binary and
reuse the three proven startup binaries with explicit source/SHA provenance.
Provision a separate runtime from the exact `s192a` genesis/validator identities;
preserve both existing runtime roots and all source corpus bytes. Obtain each
hardware turn afresh. The first schema-2 strict observation subsequently ran and failed; see below.
No new valid chain TPS exists.

Raw supply qualification reached 6,499,988.64 unique wires/s for 20.000034967 s;
this excludes online signature verification and on-chain execution. Historical
Go chain best remains 776,967 TPS. The first strict observation uses one
validator's canonical counter, a full twenty-second window and independent
drain, never the sum of seven counters. Its default disk planning estimate
needs 796.422 GiB free for the 192M source plus reserve; recheck at admission.
Do not delete source corpus or runtime evidence to force the disk check to pass.

The first actual schema-2 run `s192r1/strict-q1` verified all source SHA values,
entered the measured window and stopped on an unknown ingress outcome before
WindowEnd. It produced no valid TPS. Its last checker count was 47,500 at height
253, but owner canonicalized height 254 during cleanup; the stopped state must
be reconciled before any nonce-based resume. Owner admitted 2,032,000 versus
2,008,500 client final ACKs and retained 1,984,500 pending transactions at the
last observation. The controller stopped, preserved data and released claims.
See [the retained first strict failure](GOV5_STRICT_S192R1_FIRST_OBSERVATION.md).
Afterward about 791.75GiB remained, below the same default disk gate. Next is
same-data recovery without dispatch, not automatic replay or a replacement root.

The actual same-data `recovery-q1` attempt subsequently failed in owner startup:
QMDB twig zero could not reproduce its stored commitment. Only the owner
started and exited one; no validators, nonce audit or transactions ran. A
6000-account real MDBX regression reproduced the missing sealed-genesis-leaf
mechanism after old entry reclamation. The prevention fix is `aa60e65b`; it
persists genesis leaf blobs without changing the computed root and does not
repair existing databases. See [the reload investigation](GOV5_QMDB_GENESIS_RELOAD.md).

As of 2026-10-16 15:25 UTC the sole new monitor is PID 3509411,
`/tmp/gov5-qmdb-audit-b0745937.py`, source `b0745937`, controller SHA256
`1ca4b917ab7496963384c3b78f65d78f202f0b0c17ab77537424cb9b15fcc82e`.
Monitor log: `/tmp/gov5-qmdb-audit-b0745937-monitor.log`; state/artifacts:
`/data/blockchain/gov5-work/validation/20261016-gov5-qmdb-audit-b0745937/`.
It is `waiting-validation-build`, with no stages started and foreign
compilation/tests blocking BOX admission. It will separately claim for
race/Make/binary validation, release and yield, then claim for a file-level
backup and read-only working-copy audit. The original DB and untouched backup
are never opened through MDBX. Four targeted tests and vet have passed; actual
retained-copy audit, any precise repair, nonce reconciliation and resumed load
remain unproven. Neither a successful copy audit nor the monitor's existence
authorizes replay of previously unknown ACKs or establishes new chain TPS.

Update 2026-10-16 16:22 UTC: that monitor completed both targeted race suites,
full Make build and the audit binary build, all exit zero, then released its
claim and yielded. Its immutable binary SHA256 is
`8d244aac8ecc4571c6f8039e3f45ddede9a7ab83bd956399659973fe653374d1`.
The phase is now `waiting-copy-audit`: competing compilation/tests resumed
before the separately claimed copy turn. No backup, working DB copy or audit
result has been produced. The original source tree remains frozen at b0745937.

An additional sealed-job reference RPC is committed as `56d50aa0` on the main
branch, with targeted tests and vet passing. Replica/CLI activation remains off;
it is excluded from the frozen first strict observation. Stop expanding
optimizations until actual admission/execution phase data identifies a bottleneck.

The completed repair monitor's provenance, stage logs and `status.json` live beneath
`/data/blockchain/gov5-work/validation/20261015-e1-fcdb22d2/root-rules-fb1671a4`.
The runtime root is `/data/blockchain/gov5-performance-5m/runtime/f1015a`.
Check the actual live monitor and status before starting anything else.

The finite-load monitor `/tmp/gov5-load32k-afb24bc0-v3.py` was explicitly paused
before any build/provision/run stage, following the user's supply correction.
PID 832560 has exited; log `/tmp/gov5-load32k-afb24bc0-monitor.log`. Its source/runner
proofs, controller history and `status.json` are under
`/data/blockchain/gov5-work/validation/20261015-e1-load32k-afb24bc0/`.
It pinned the clean isolated workload worktree to `afb24bc0`, waiting for complete
BOX admission, rebuilds only the load CLI with clean source provenance, and
provisions `/data/blockchain/gov5-performance-5m/runtime/load32k-20261015a` once.
It releases the build claim before independently claiming for the load run.
The state is now `paused-user-supply-correction`, with no stages begun. The pause
verified no owned claim, no child processes and no new runtime directory.
`user-supply-pause-proof.json` and before/after statuses retain that evidence.
There is no automatic resume. No 32k workload result was produced.

The prepared diagnostic corpus has 64 senders, 512 nonces each, four batches
of 8,192 transactions, 64 fixed distinct recipients and one ingress validator.
Signing precedes the 20-second observation; drain is bounded at 30 seconds.
Only explicit head-conflict rejection permits a bounded batch retry. The owner
receives GOMAXPROCS 36, each metadata process two; these are thread budgets,
not exclusive physical core allocations. Report actual canonical counter
delta/time, batch timings and corpus exhaustion as a finite-supply result,
not 5M capacity or an S103 promotion. Its fixed 32,768/20-second supply caps the
observed rate at 1,638.4 TPS even with instantaneous execution. It must not use
the next capacity window. The user's next requirement is a Rust-informed Go
feeder that can sustain supply above 5M/s; the prepared 32k tools remain only
diagnostic tools. Frozen node/owner binary SHAs and the original f1015a data
remain retained.

When active, the monitor retries only explicit resource refusal before `load-run` exists.
Once a child may have started, it preserves a failure and stops for diagnosis;
it never creates another root to erase the outcome. TERM/INT unwind command
and claim cleanup; functional process groups receive up to 90 seconds to clean
their owned children. No foreign process or claim is altered.

The monitor automates its recorded validation/build/functional steps only. It
does not autonomously diagnose arbitrary failures, implement all later stages,
or measure TPS. The active coding session must inspect its results and advance
the next gate. A live waiting monitor must never be reported as an executing
benchmark, a passed test, or continuous model execution after a session ends.

Keep source commits small and logical. Commit only tested implementation or
clearly identified documentation; preserve unrelated uncommitted work. Report
the exact measured result, source/binary provenance, active gate, hardware wait
reason, and next action. Never commit private runtime keys or populated data.
