# Gov5 5M unattended campaign

Updated 2026-10-18. The user authorized continuous work, small commits/pushes,
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

## Latest finite phase diagnostic and durable baseline — 2026-10-18

The explicit finite diagnostic passed on the original retained `s192r1` data
using clean `74de4981` binaries. It preloaded **1,032 complete original frames**:
516,000 original transactions, 315,824 fixed canonical skips and **200,176 new
admissions**, before starting any of the seven producers. All new transactions
then became canonical: block 1,241 contained **163,000 transactions**, with
successful Produce, durable reference Import and public-QC `advanceCanonical`
records joined by the same hash/number/parent; block 1,242 contained the remaining
37,176. This is a finite phase diagnostic, **not a TPS or 5M capacity result**.
The previous failed 20-second measurement remains failed.

After clean producer stop, a complete stable 6,000-account snapshot and a real
nonproducing physical reopen matched exactly, including each sender's known
nonce/balance delta, header root and loaded QMDB root. The new durable baseline
is count **904,301**, height **1,242**, hash
`0x5100080e3d2344a93930269ece9797e503ff202edf6080c0d1476dc3a83f6295`,
root `0x419f3e90d6adda48545aac124a3b023f10f2d708df2dd434288e69db9921b502`,
and vector SHA `0x11fc20d12bc08bba0e961b18b34b603d6a3b04120d13f0b158c88668abc25f58`.
All 18 runtime processes exited zero without forced kill; outer and child also
exited, all groups/sockets disappeared, and owned claims released. The protected
three copies' complete hashes were unchanged. The original remains the same
2 GiB inode in mode 0600; its post SHA is
`15fef847ea7eb0623d5ac4c90bb1d9242cde03c288c1a527d49bdb9f469b482b`.
No worker is active after this released turn. Future sessions must use this new
full-vector baseline and a new plan/epoch; the completed plan is not replayable.

Evidence in `validation/20261018-gov5-finite-phase-target-v1/`:

- `status.json`: `094d694036534aae147e731d1471f521a8a7e678a36e4ca83ecad570364d5006`.
- `finite-result.json`: `fa83de3284a1c456d56e78636345abdbf216fee6545475f322e2473ef113504e`.
- Independent Sol `acceptance.json`: `e8b34b44f799efc0e054b8c19fb31b7e4e58f5a38c86afecb1ecedff15bedafe`.
- Full physical-reopen report is
  `runtime/s192r1/strict-attempts/finite-phase-q1-reopen/recovery.json`, SHA
  `7c7b6e265122b9da75673e07512d1072479cd95f4784c35d5e367ada2259f734`.

The new timing records locate the long preparation interval in the candidate
WAL `db.Update`/`WriteBlock` span (about 5.16–5.29 seconds), with an initial
reference availability-write span of 5.37 seconds. The same block's owner import
RPC took about 0.27 seconds. Canonical RPC spans also reached 5–7 seconds while
the owner's logged canonical transaction itself was about 0.16 seconds. These
are measured call spans, not yet an attribution to disk, lock waiting or encoding.
Read-only diagnosis precedes any optimization; no verification is bypassed.

## Prior bounded window and persistence proof — 2026-10-18

The first real GSR2 bounded-prefix attempt, `bounded-window-q1` on retained
`s192r1`, completed a **20.001335337-second** canonical counter bracket:
211,917 to 215,125, delta **3,208**, or **160.389291312 TPS**. This is a diagnostic
measurement: the overall attempt **failed** its subsequent 45-second drain.
No capacity result was promoted. The 60M original-transaction source bound
cannot establish 5M sustained TPS for 20 seconds. The actual Go parallelism
budget was owner 36, replay 4 and seven validators 2 each; it does not describe
exclusive physical CPU cores. Original blocks remain capped at 163,000
transactions / 32 MiB; the central pool remains 2M transactions / 512 MiB.

All 64 immutable journals independently account for 4,430 original frames,
2,215,000 original transactions, 233,435,154 encoded bytes, **2,003,124 known
new ACK admissions** and 211,876 canonical skips. The source did not exhaust;
Busy and snapshot retries occurred. CryptoVerified counts repeated signature
attempts and is not unique transaction throughput. The last in-window count
was not the later drain observation: reference_final was 541,125 at height
1,239, and shutdown logs subsequently recorded another commit. No pending
queue size was treated as a final persisted or lost count.

A separate **zero-dispatch, nonproducing double physical reopen** passed on
the same data. Both full 6,000-account vectors, canonical heads, header roots
and loaded QMDB roots matched, with distinct empty owner epochs. That recovery’s proven
baseline was height **1,240**, hash
`0xe58f1eb9d4470a492df4372ebaf31cc15636f3bc6474d109ee28ed8dc0e6e7c9`,
root `0x0062215b74827a2dfeeaa35ff1d5b90ded5bdd0afd3b0af39bef3e0bf135e9f7`,
and canonical nonce sum **704,125**. Each sender's actual delta was within its
known ACK bound with the exact original transfer cost. Thus **492,208** of
this attempt's new admissions are canonical, and **1,510,916** known ACK
admissions are not canonical. None were resent. Reusing the old plan, epoch,
nonce-zero workload or volatile pending queue is prohibited; any future
session needs a new plan and fresh complete canonical snapshot.

The audit reused the accepted clean `1356b3a6` binaries with Python runner
`f72ba7e9`. All 18 runtime processes exited zero without forced kills, both
controller/child groups disappeared, nine sockets disappeared and captured
claims released. Full hashes of the three protected copies were unchanged;
the original retained device/inode/mode remained fixed. Its post-audit SHA is
`53a8780c0da28004bce5974a45236b525ce7543a9fded2ae913959f75031e246`.

Evidence under `/data/blockchain/gov5-work/validation/`:

- `20261018-gov5-bounded-20s-target-v1/failure-acceptance.json`, SHA
  `96ba695bf2af7af46001c973bd4470deb7b0da97d63e7b517e3338f7c9e0bd9d`.
- `20261018-gov5-bounded-20s-target-v1/failure-reference-supplement.json`, SHA
  `a2bd454dcc42a17af58d0f8de2dd520005a0b29105b95a83e823e01332319e99`;
  preserves the original record and corrects its mislabeled last-window sample.
- `20261018-gov5-post-bounded-double-reopen-v1/audit-result.json`, SHA
  `af29dbf8f9e3d961df34cf0432d4529f247d4aa1c0cca02b8dd0833de2c68121`.
- Its independent Sol `acceptance.json`, SHA
  `e0867e41e815a73d5bc50416418b08bdf638da85e849ec0c63ecf895434f9c9c`.

The failed measurement remains failed. The reopen establishes durable state;
it does not turn ACK throughput, drain work or a historical raw supply test
into canonical TPS. No gov5 runtime worker remains active after this turn.

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
