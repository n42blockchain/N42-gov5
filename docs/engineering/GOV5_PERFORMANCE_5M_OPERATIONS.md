# Gov5 5M unattended campaign

Updated 2026-10-15. The user authorized continuous work, small commits/pushes,
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
The third attempt is pending hardware. There is no new throughput result.
Offline Ed25519 codec changes remain untested work in progress and must not
enter this frozen-baseline retry.

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

The passive repair monitor is `/tmp/gov5-root-rules-fb1671a4.py`; its provenance,
stage logs and `status.json` live beneath
`/data/blockchain/gov5-work/validation/20261015-e1-fcdb22d2/root-rules-fb1671a4`.
The runtime root is `/data/blockchain/gov5-performance-5m/runtime/f1015a`.
Check the actual live monitor and status before starting anything else.

The monitor automates its recorded validation/build/functional steps only. It
does not autonomously diagnose arbitrary failures, implement all later stages,
or measure TPS. The active coding session must inspect its results and advance
the next gate. A live waiting monitor must never be reported as an executing
benchmark, a passed test, or continuous model execution after a session ends.

Keep source commits small and logical. Commit only tested implementation or
clearly identified documentation; preserve unrelated uncommitted work. Report
the exact measured result, source/binary provenance, active gate, hardware wait
reason, and next action. Never commit private runtime keys or populated data.
