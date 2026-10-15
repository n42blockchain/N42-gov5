# Opt-in shared executor admission queue

The owner now has an explicit `EnableStrictIngress` API and an atomic
`AdmitStrictFrame` API. Constructors leave the queue disabled. This first slice
is an in-process backend with real execution/commit tests: no binary ingress
listener, CLI switch, BuildFromQueue RPC or validator producer consumes it yet.
It does not change the existing native, ETH or shared-validator Submit API.

The preceding [raw supply gate](GOV5_SUPPLY_CODEC.md) passed at 6,499,988.64
receiver-confirmed unique transactions/second. That sink discarded payloads and
performed no online signature checks or execution. Its result is not the
admission or execution throughput of this new queue.

## Admission and memory

Startup pins an operator-provided bounded finalized manifest, its complete-byte
SHA256, full owner chain/genesis identity and a new random session epoch. The
manifest binds codec and frame ranges; it does not grant transaction authority.
Each frame must match its payload digest, descriptor count/nonce range and one
recovered sender. This backend does not yet verify a complete shard stream's
membership against the manifest EOF hash.

Only protected legacy EIP-155 transactions on the explicit performance chain
are admitted. Each signature is recovered directly with the EIP-155 signer:
claimed sender fields, global sender memo caches and offline verification do
not authorize admission. The initial transfer shape requires gas 21000, empty
input, an EOA sender and recipient, sufficient canonical balance for the entire
pending sender prefix, and gas price at least the canonical next base fee.
Canonical old nonces, gaps and same-nonce replacements fail. A late failure
publishes none of the frame, including its new sender lanes and nonce cursors.

Transaction and encoded-byte credits include verification in flight. Limits
are independently bounded at 2M transactions and 512MiB encoded bytes. Successful
admission retains one defensive payload copy and raw offsets/hash/nonce/cost
metadata. A partially consumed slab keeps its **whole byte charge** until its
last pending entry is pruned. Pruning also releases or compacts pointer arrays;
empty sender lanes retain no transaction array. Successful publication transfers
its in-flight credits to retained credits atomically. Returned selector payloads are copies. These
credits are not a bound on metadata, GC, caller buffers, sockets or total RSS.
Recent frame IDs retain 32 payload digests per shard; identical retries report
zero new admissions, conflicting payloads fail, and expired IDs fail. This check
is repeated at the final publication mutex, including concurrently verified frames.

## Canonical state and failure boundary

The owner exclusively controls canonical mutations. HotStuff import does not
advance canonical state. Every server commit path marks the queue reconciling
and increments its canonical generation before calling the existing certified
or trusted-owner canonicalization code. It then scans the actual durable
canonical prefix, including missed bodies, with the server lifetime context.
Build and Import do not remove entries. Only canonical nonce progress prunes
them; duplicate commit cannot free a transaction or slab twice.

Account reads hold the BlockChain mutation lock, join pending writes and require
canonical head == actual-applied state. The exact fresh-genesis root exception
matches the existing unsigned builder. Admission captures generation/hash/height
and verifies both this stamp and the actual account-snapshot identity at the
final queue insertion point. Thus a commit followed by completed reconciliation
cannot make an old snapshot current again. Speculative applied state causes a
retry instead of supplying a false canonical nonce/balance.

A canceled request after a successful canonical write still triggers owner-owned
reconciliation. Any reconciliation read/body/recovery failure keeps admission
and selection closed. A later idempotent commit can complete the scan and reopen
the queue. This is a volatile queue: restart chooses a new epoch and must recheck
canonical nonces. Old ACKs or a nonce-zero corpus cannot resume blindly.

## Validation and remaining work

Eight targeted queue tests and four existing default commit/unsigned-build
regressions passed together (0.178s), using low-priority `GOMAXPROCS=2 go test
-p 1 ./internal/sharedexec`, their explicit test-name selectors, `-count=1` and
`-timeout=90s`. They include two senders with three genuinely signed transactions each through
actual QMDB Build, local leader Seal, Unix Import and 5/7 certified canonical
commit; nonce, balance and persisted receipts match. Tests cover 4/7 rejection,
duplicate commits, atomic invalid-frame rejection, credits and payload ownership,
partial slab retention, post-write request cancellation, completed-reconcile
snapshot races, and owner read failure closing admission until recovery.
Concurrent same-ID/different-payload publication, identical duplicate publication,
credit invariants and empty-lane metadata release also pass. Targeted
`go vet -p 1 ./internal/sharedexec` passed.

Race and fresh binary/fleet validation of this new backend await an independent
BOX claim window. No queue throughput or new chain TPS has been measured.
Next slices must add a distinct identity/epoch-bound binary admission transport,
then stable queue-selection leases and BuildFromQueue with actual-applied parent
accounts, followed by the default-off owner-queue producer integration. They
must retain full bodies, leader-owned seals, validator WALs, Import checks and
strict CommitQC. The present Build/Import test executes both paths separately;
it does not claim own-build reuse or pipelined execution.
