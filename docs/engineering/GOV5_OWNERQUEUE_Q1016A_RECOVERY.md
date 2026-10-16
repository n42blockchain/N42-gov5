# q1016a owner queue recovery

The first eight-process owner-queue attempt actually ran on 2026-10-16 at
07:41 UTC. Its retained root is
`/data/blockchain/gov5-performance-5m/runtime/q1016a`; attempt `q1` must never
be overwritten or reinitialized. The runner used source `875f729b` and three
source-`b02d84a2` binaries recorded in the validation artifact. All eight
initializations succeeded, all seven initial metadata counters were zero with
the expected identity and owner epoch, and all eight runtime processes exited
cleanly during failure cleanup.

`attempts/q1/signed-smoke.log` reports:

```
queue admission stopped without replay: strict ingress rejected: canonical accounts are not actual applied state; retry
```

The owner log records empty-block canonicalization at height one. This is the
canonical/applied admission gate, not a signature failure. The frozen client
does not have the later typed snapshot retry protocol. No successful eight-tx
proof or chain TPS result was produced. There is no final admission journal in
this old checker, so recovery must establish actual zero-count canonical
evidence before sending nonce zero rather than infer it from a stopped process
or a new volatile queue epoch.

The recovery checker uses the existing typed zero-admission retry protocol.
Only Busy and SnapshotChanged can retry the exact same frame and connection,
for at most five seconds per frame with a 20ms backoff. Unknown ACK, transport
failure, ordinary rejection, nonzero admission on retry, or a changed welcome
epoch stops the check. Signature verification counts attempts and may exceed
eight after snapshot retries; newly admitted and canonical unique counts must
still equal eight, with all four owner credit counters zero.

Before sending, recovery requires all seven metadata stores to report zero
canonical transactions, the same canonical hash/height as the owner, an empty
owner queue, and an unchanged epoch/manifest binding. Existing empty blocks are
allowed. Any nonzero canonical count refuses nonce-zero replay. Reading a
canonical nonce still requires canonical state to equal actual applied state.

Resume uses the same genesis and databases, a new attempt name, and no
`--initialize`. Attempt `snapshot-retry` ran at 09:58 UTC using the old
`b02d84a2` n42 binary and new source-`c32847b5` owner/checker binaries. Only the
owner started; it exited before serving because its startup account snapshot
required canonical/applied equality. No validator or checker process started.
Its owner log and lifecycle remain under the same root's second attempt.

The next startup slice permits only the exact typed canonical/applied mismatch
to initialize an empty queue with verified canonical metadata, not account
nonces. An independent startup gate blocks admission and new BuildFromQueue
plans. Validators may pin the unchanged epoch/manifest and start their existing
certificate replay and AppliedChild recovery. A duplicate/lower valid QC is
acknowledged without clearing the gate; only an actual canonical account
snapshot matching the new canonical tuple clears it. Missing markers and other
storage errors still fail closed. Default/non-performance paths are unchanged.

This is conditional recovery: a durable pending CommitQC can catch up, and a
validator whose parent precedes an existing applied child can re-propose it.
If LockedQC already names the actual tip and its CommitQC is absent, no child
exists and new builds remain blocked. That state needs an explicit recovery
protocol; this slice does not invent a certificate, rewind state, or claim
automatic recovery. All three runtime binaries now need the startup slice,
including n42's pin-before-replay change. Frozen sources, both attempts, and
their binary artifacts remain unchanged. The next actual recovery, strict load,
and fault gates have not passed.

Validation: eight `TestQueue*` tests passed under GOMAXPROCS=2/-p=1 (0.130s),
including typed retry bounds, exact frame preservation, no unknown/rejection
replay, nonzero retry refusal, and nonzero metadata rejection with a restarted
empty owner. Four Python queue-evidence tests passed (0.009s), including
verification-attempt counts above eight while retaining unique/credit checks.
`go vet ./cmd/gov5-perf-provision` passed.

Startup slice validation: real QMDB/Unix tests cover canonical one/applied two,
owner reconstruction, zero-admission/build gates, duplicate QC acknowledgement,
four-vote refusal, five-vote catch-up, and missing-marker refusal (0.054s).
Real metadata/producer tests cover pinning before persisted certificate replay,
another validator's existing-child reproposal, and LockedQC-at-tip without a
CommitQC remaining blocked (0.078s). Targeted startup and existing strict/queue/
epoch/two-leader regressions passed (0.208s); cancellation and storage-read
failure regressions passed (0.044s). Vet passed for internal, sharedexec and
sharedvalidator. Tests used GOMAXPROCS=2/-p=1; new binaries/race remain pending
the next hardware claim. These tests reconstruct services over a real BC and
invoke Replay/Produce explicitly; they do not reopen the BC from disk or prove
full service.Start/seven-process recovery.
