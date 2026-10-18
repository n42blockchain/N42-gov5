# Skip fresh empty queue selections

The q4 20-second run failed its unchanged drain deadline; its 75,041 canonical
transactions over 20.001005871 seconds remain diagnostic. Frozen phase analysis
`382b3e25` shows 37 fresh empty canonical commits before nonempty work, with
snapshot retries and 19,041,500 signature checks for 5,145,500 acknowledged source
transactions. This identifies avoidable state churn, not a measured CPU limit.

The independently accepted retained-child recovery `11c907e7` / `64ab8836`
establishes the only new baseline: canonical height 1408, hash `010a51ea`,
root `a13e5a10`, nonce sum 4,644,394 and vector `baf9ccf2`. Live state, the snapshot
after stopping seven recovery-only validators, and two ordinary physical reopens
are identical. All 26 runtime processes exited normally. Actual new persistence
is 1,216,041; 501,106 previous ACK admissions are not canonical. No old admission
session is resumed or treated as a state proof.

The narrow change returns `ErrQueueBuildEmpty` only when a fresh owner queue plan
has selected zero eligible transactions. It uses the existing materialization
failure cleanup to delete that unselected plan and refund its retained budget,
before block construction or execution. The same prepared request can select
later admitted work. Pending queue length is not used as an availability test:
actual parent nonce, balance, fee and encoded-byte eligibility still control
selection. Cached bodies and already selected failed/canceled plans retain their
original retry behavior. The Unix API keeps its existing generic HTTP 409 error,
and producer retries already re-enter peer and parent checks.

Producer order is unchanged: own-candidate WAL, AppliedChild recovery, the
recovery-only barrier and request WAL all precede a fresh queue build. Existing
sealed children and their public 5-of-7 QC paths remain available. Per-view request
WAL writes can still occur while idle; this change does not promise a fully idle
runtime. There are no signature cache, snapshot equality, metering, queue geometry,
CPU budget or measurement-window changes.

New fixture tests cover empty selection with no execution/import/generation or
plan-budget change, a later admission using the exact same request, a nonempty
queue that cannot fit its first transaction, and already-applied queue entries
that must not create an empty descendant. Existing selected-plan cancellation,
cache retention, actual applied-parent selection and recovery/QC tests remain.
The plan parser adds only the exact newly accepted historical kind and retains
all complete account/root and second-reopen hash checks.

This source slice is a static draft. It has not been compiled, tested under Go,
or used for a new retained runtime or TPS measurement. Any next run must use
newly accepted binaries and a fresh source-verified plan at the audited baseline.
