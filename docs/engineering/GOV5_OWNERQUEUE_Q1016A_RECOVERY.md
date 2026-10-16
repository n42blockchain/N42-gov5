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
allowed. Any nonzero canonical count refuses nonce-zero replay. Startup still
requires canonical state to equal actual applied state; this patch does not
bypass that gate or repair a divergent database.

Resume uses the same genesis and databases, a new attempt name, and no
`--initialize`. The old `b02d84a2` n42 binary can be reused; owner and checker
need binaries containing the typed retry protocol and this checker change.
Frozen sources and q1 evidence remain unchanged. The resumed real run is still
pending; subsequent strict load and fault gates have not passed.

Validation: eight `TestQueue*` tests passed under GOMAXPROCS=2/-p=1 (0.130s),
including typed retry bounds, exact frame preservation, no unknown/rejection
replay, nonzero retry refusal, and nonzero metadata rejection with a restarted
empty owner. Four Python queue-evidence tests passed (0.009s), including
verification-attempt counts above eight while retaining unique/credit checks.
`go vet ./cmd/gov5-perf-provision` passed.
