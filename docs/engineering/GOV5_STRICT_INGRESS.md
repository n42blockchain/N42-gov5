# Opt-in shared executor admission queue

The owner now has an explicit `EnableStrictIngress` API and an atomic
`AdmitStrictFrame` API. Constructors leave the queue disabled. This first slice
has both an in-process backend and a separate private Unix binary admission
listener/client. A `BuildFromQueue` RPC now consumes selected nonce prefixes into real unsigned
candidates. The validator Producer has a startup-only `UseOwnerQueue` API using this RPC;
NodeCfg/CLI exposure is the next slice.
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
admission retains one owned payload and raw offsets/hash/nonce/cost metadata.
The public in-process API copies its caller buffer; the binary transport transfers
its budgeted receive buffer without another payload copy. A partially consumed slab keeps its **whole byte charge** until its
last pending entry is pruned. Pruning also releases or compacts pointer arrays;
empty sender lanes retain no transaction array. Successful publication transfers
its in-flight credits to retained credits atomically. Returned selector payloads are copies. These
credits are not a bound on metadata, GC, caller buffers, sockets or total RSS.
Recent frame IDs retain 32 payload digests per shard; identical retries report
zero new admissions, conflicting payloads fail, and expired IDs fail. This check
is repeated at the final publication mutex, including concurrently verified frames.

## Private binary admission protocol

`ListenStrictIngressUnix` uses the same absolute, unaliased private-directory
and 0600 socket checks as the existing execution socket, on a distinct endpoint.
It requires an explicitly enabled queue. `GSI1` pins full chain/genesis identity,
exact finalized manifest bytes SHA256 and shard; the welcome pins a fresh queue
epoch. JSON control metadata has a 4096-byte length limit, rejects unknown fields
and trailing values, and never reads ahead into a transaction payload.

A client first sends only the bounded 84-byte `GCF1` frame header. The owner
checks descriptor/range/count/length and reserves global transaction **and** byte
credits before granting an upload. Only after the grant does it allocate/read
the raw payload. Direct signature/state verification and final publication share
the same reservation token and owned bytes; successful publication atomically
transfers this token into retained ownership. A known published duplicate checks
frame ID, digest, count and encoded length and acknowledges it without a body
upload or another signature check. Concurrent grants still repeat these checks
at final publication.

Grant means zero admissions. Only the final identity/manifest/epoch/frame-bound
ACK reports newly admitted and duplicate counts, whose sum must equal the frame
count. Busy is a known zero-admission result and consumes no body; the same
connection remains aligned for an explicit retry. An explicit rejection also
means this request published nothing. Any missing or malformed ACK, wrong
binding or ambiguous post-send IO poisons the client connection and returns
`ErrIngressUnknownOutcome`: no reconnect or automatic frame replay occurs.
An ACK lost **after publication** retains the admitted queue entries.

At most 64 connections are served. IO uses a 2-second hello deadline and a
30-second frame deadline; verification also has a server-lifetime-derived
30-second context. A context cannot hard-interrupt an existing BlockChain mutex
wait. Close gates new registrations, cancels the owner and closes the listener
and live connections before waiting for handlers/verification, then frees queue
payloads. The first client is synchronous, one frame at a time per connection;
this is not a 6M/s strict online-admission claim. CLI exposure is intentionally
paired with the later actual producer-consumption integration.

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

## Queue candidate construction

`BuildFromQueue` accepts a leader-prepared empty-body template, full identity,
queue epoch and transaction/encoded-byte limits. The owner publicly verifies the
unsigned HotStuff template and requires its exact parent to be actual-applied.
A separate bounded parent-account reader returns that parent's nonces/balances,
gas limit, view and derived next base fee. It never supplies the canonical nonce
baseline for admission. The builder checks the prepared fee and unchanged gas
limit, then selects deterministic sender-order round-robin contiguous prefixes
from each **actual parent** nonce, skipping already-applied ancestor entries
without deleting them. Balance, base fee, block gas, transaction and encoded-byte
limits bound selection. It reuses the existing real isolated QMDB builder, which
rechecks the parent under the chain mutation lock and performs normal signature
and state execution. Selection is not a sender-trust or execution exemption.

The first valid `(epoch, view)` request binds its full prepared template and
limits; changing them conflicts. Its one immutable selected full `TemplateRLP`
and successful result are pinned until canonical expiry. Input and reserved
output share the existing 128MiB encoded build budget; completed plans retain
input plus actual output. Failed attempts retain input and must reserve output
again before retrying **the same body**. Normal Build cannot evict queue plans;
both paths share the registry slot limit. Canonical reconciliation releases plans
at expired heights, maintaining a monotonic expired-view floor including pruned
abandoned plans, so removing a guard cannot authorize that view at a new height.
This conservative floor can require a newer view after an abandoned higher-view
plan. Exhausted plan capacity fails closed until canonical progress or an
explicit new process/queue epoch, rather than silently replacing a body.

This first RPC holds the owner execute mutex through selection, build and
completion/accounting. Request cancellation does not cancel already selected
owner execution; successful results remain available to an exact retry, even
when actual applied state has advanced. Waiting on this mutex is not a hard
context deadline. This is deliberately still serial and makes no pipelining
claim. Retained budgets exclude transient decoded transactions, responses,
metadata, an optional prepared own-build write and total RSS.

The independent client verifies full body root, transaction/encoded-byte limits,
result commitments, unchanged leader header except TxHash and the existing
execution/fork result fields, and reconstructs the selected template hash.
Leader Seal, persistent own-candidate WAL, full-body Import, execution evidence
and strict CommitQC remain required. The queue tests use separate Build and
Import executions; they do not claim prepared reuse.

## Validator producer integration

`Producer.UseOwnerQueue` is explicit and startup-only, before Start, Produce or
any local pending transactions. It queries the private owner status RPC once,
pins full identity, manifest and epoch, and bounds selection limits. The default
selection cap is 163,000 transfers, matching half of the existing 6,846,000,000
gas limit at 21,000 gas each. The immutable s192a corpus has gas price/base fee
1: blindly selecting 200,000 could raise the next base fee to 2 and stop further
admission. This fixture and fee profile are unchanged.

Production retains sealed own-candidate WAL recovery and AppliedChild lookup
before requesting a new queue candidate. For a new candidate it first persists
the complete prepared request, manifest, epoch, parent, view and limits in its
own metadata WAL, then sends BuildFromQueue. A producer restart reads this
request rather than calling Prepare again; changed epoch/manifest/limits fail
closed. The head inside the prepared request must match the captured view even
if consensus advances during Prepare, and view/leader/context are checked again
before RPC and before signing/proposal. Already sealed candidates remain the
stronger recovery source. Seal is still local to the validator. Persisting the
sealed body/own-candidate binding and deleting its prepared-request WAL entry
share one local transaction; Import and execution evidence precede Notify.
Unsealed abandoned request records remain durable safety evidence; this slice
does not claim a general historical WAL garbage collector.

Queue mode rejects both direct and raw local submissions, including an early
HTTP rejection before large JSON decoding, so it cannot ACK an unconsumed local
pool. Validator status adds an optional owner-queue binding only when selected;
its existing queued count continues to mean **local** pending transactions.
Owner pending/reserved transaction and encoded-byte counts must be queried
independently for draining. Restart does not transparently refresh a volatile
owner epoch or replay old nonce-zero frames.

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

The binary slice adds eight tests, including the original six-transaction
execution/receipt check through actual Unix admission. All 16 strict-ingress,
four codec and four existing commit/build regressions passed together (0.257s)
with the same constrained command and selectors; targeted vet also passed.
Transport negatives cover wrong manifest, oversized/unknown/trailing control
metadata, grant followed by partial disconnect, busy without losing framing,
header-only duplicate, two concurrent conflicting granted frames, 64-connection
cap, and Close while handlers wait for header, body or final ACK. A real private
Unix test drops the final ACK only after successful publication: the client
reports unknown and refuses a second send, while the queue records exactly
three new admissions and releases its reservation. Test setup fixes (a nil
optional fixture callback and a pre-write error mistaken for lost ACK) preceded
these passing checks; they did not change protocol authorization.

Eight queue-build tests now pass with all earlier selected regressions: 35
test functions together (0.373s), constrained to `GOMAXPROCS=2 go test -p 1
./internal/sharedexec` with selector
`^Test(QueueBuild|StrictIngress|Corpus|CanonicalCommitOverUnixWithRealExecution|CommitDisabledByDefault|CertifiedCommitUnixRequiresFiveOfSevenCommitDomain|UnsignedBuild)`,
`-count=1 -timeout=90s`. Targeted vet also passed. Real Unix tests use two
independently keyed leaders and one six-transaction queue across two blocks,
verify actual nonce/balance/receipt state, reject 4/7 finality, and free only on
5/7 canonical commitment. They also build both blocks while applied state leads
canonical, then finalize the prefix in order. Further cases cover cancellation
after selection, failure retaining the selected input for recovery, late
admission not changing a view's body, cache pressure, byte-prefix limits,
invalid requests, expired view reuse at a higher height, caller response copies
and malformed/auxiliary-body return data. The client explicitly rejects verifier
and proof payloads and checks scheduled rewards against WithdrawalsHash; this
closes a review finding where transaction root and header hash alone did not
bind those auxiliary fields.

The producer slice adds five real-owner tests. Its six signed transactions
cross private GSI1 ingress, the actual producer's BuildFromQueue, local BLS Seal,
durable candidate WAL, full Import and 5/7 certificate bridge. The validator has
no execution-state tables; the owner has no BLS private key. Tests cover 4/7
rejection and committed counter 6, prepared WAL before a canceled RPC, recreated
producer sending byte-identical requests, changed owner epoch rejecting an
unsealed old request without overwriting it, sealed WAL priority, Stop canceling
a blocked request, and a full seven-view leader rotation during Prepare without
writing the wrong WAL or invoking execution. All 44 selected queue, producer,
reproposal, codec, unsigned-build and commit regressions passed together (0.478s)
with `GOMAXPROCS=2 go test -p 1 ./internal/sharedexec ./internal/sharedvalidator`,
the earlier selector extended with `OwnerQueue|RemoteProducer|NextLeaderReproposes`,
`-count=1 -timeout=90s`. Targeted vet of both packages passed. These are local
fixtures and Unix integration tests, not a seven-process queue fleet or TPS run.

Race and fresh binary/fleet validation of this new backend await an independent
BOX claim window. No queue throughput or new chain TPS has been measured.
Runtime selection is now explicit and default-off. Validators use
`--performance.owner-queue`, optionally
`--performance.queue-max-transactions` (default 163000) and
`--performance.queue-max-encoded-bytes` (default 32 MiB). Equivalent NodeCfg
JSON/YAML fields are `performance_owner_queue`,
`performance_queue_max_transactions` and `performance_queue_max_encoded_bytes`.
Any queue option requires the private n42 performance validator role, explicit
genesis and owner socket; ordinary nil/native/ETH selection remains unchanged.
The constructor pins the owner's epoch/manifest before serving the validator API,
including validators that are not configured to produce blocks.

The owner opts in with `--strict-ingress-manifest /absolute/manifest.json`.
Optional `--ingress-max-transactions` and `--ingress-max-encoded-bytes` bound
credits to at most 2 million transactions and 512 MiB. The manifest is checked
against the actual configured genesis before opening the database. Input must
be a bounded regular file without aliases; nonblocking/no-follow open plus
pre/post file-identity checks reject FIFO and replacement races. The owner binds
both `execution.sock` and `ingress.sock` under its existing private execution-rpc
directory before serving either. Partial startup failure preserves unrelated
existing files; endpoint failure or shutdown cancels and drains work before
BlockChain/database closure. Full bodies, leader-owned seals, validator WALs,
Import checks and strict CommitQC remain in the production path. The present Build/Import test executes both paths separately;
it does not claim own-build reuse or pipelined execution.

The runtime wiring slice passed targeted tests across conf, cmd/n42,
cmd/n42-execution-service, internal/node, internal/sharedexec and
internal/sharedvalidator with the earlier selector extended by OwnerPrivate,
PerformanceValidator, PerformanceRuntimePaths and ExecutionServiceRejects,
using GOMAXPROCS=2, -p 1, -count=1 and -timeout=120s. Package times were
0.004s, 0.020s, 0.018s, 0.531s and 0.526s; sharedvalidator compiled with no
matching tests. Targeted vet of all six packages passed. New cases exercise
actual CLI destinations and YAML loading, invalid options before datadir
creation (including FIFO), constructor pinning without local execution,
startup-only selection, both private endpoints and partial-start/endpoint-exit
cleanup. Fresh binaries, race and seven-process queue validation remain pending
an independent BOX window; these fixture results are not chain TPS.

## Small process fixture

`gov5-perf-provision --owner-queue --run-root /dedicated/fresh/root
--template /absolute/isolated-chain.json` prepares exactly four funded senders
with two genuinely signed nonce-zero/one transactions each. Its private seed
uses the same versioned HMAC key derivation and per-transaction recipient recipe
as the supply corpus. It writes four complete GCF shards and a finalized,
identity-bound manifest under `load/queue`, and separate `queue-configs/vN.json`.
The original `run.json` and base node configs retain their existing schema.
Neither this mode nor preparation initializes a database or starts processes.

The current repository fleet runner adds an explicit `--owner-queue` mode,
requiring `--smoke-binary` and a new `--attempt` name. This mode can initialize
fresh datadirs in that new attempt; default/restart modes retain their existing
rules. It reuses the same three quiet samples and dual BOX claims, verifies
private configs and shard hashes, enables the owner's manifest flag, and starts
all validators with the queue configs. Its checker sends each of the four frames
once through strict ingress; ambiguous ACK/IO errors stop without replay. It
requires initial zero canonical counts and fresh empty owner status. Success
requires eight unique canonical executed transactions, seven matching heads,
the same owner canonical tuple and epoch, and independently all four central
queue credit counters zero. Validator-local queued count zero is insufficient.
Logs/results and process lifecycle evidence remain in the exclusive attempt.
A failed real run retains its datadirs and is not automatically reinitialized.

Eight provision/checker tests pass (0.050s), including actual transaction decode,
direct signature recovery, per-sender nonce uniqueness, declared recipient
mapping, corpus hash corruption, original config preservation, invalid-shape
rejection before writing and a nonzero owner refusing nonce-zero admission.
Eighteen Python tests pass, comprising the original fleet checks plus queue
manifest/config validation, alias/hash drift, central credit-release evidence
and incompatible mode rejection. Targeted provision vet passed. A test setup
initially used the wrong existing config-loader signature; it was corrected
before these checks. These tests do not claim an OS-process queue run: gated
race, clean binary builds and the fresh eight-process functional run remain
required. No strict queue or chain TPS has been measured.

## Typed zero-admission snapshot retry

The strict client exposes its immutable welcome epoch before the first frame.
A new typed `retry` reply is limited to canonical-snapshot changes with zero new
or duplicate admissions. It carries `RetryReason=canonical_snapshot_changed`;
unknown reasons, nonzero counters or a reason attached to another reply kind
poison the connection as an unknown outcome. Human-readable error text never
controls replay. Existing clients fail closed on this extension.

The owner emits this reply only before queue publication. After a grant it has
consumed the complete payload and released that frame's reservation before
sending retry and accepting another header on the same connection. The chain
reader distinguishes a present applied marker that differs from canonical from
missing markers and storage/header failures; only the former maps to snapshot
retry. It still refuses speculative PlainState as a canonical nonce baseline.
A caller may explicitly retry the identical signed frame under its own bounded
deadline; there is no automatic reconnect or replay.

Eight selected retry/transport regressions passed (0.099s), using low-priority
GOMAXPROCS=2, go test -p 1 ./internal/sharedexec with selector
`^TestStrictIngress(SnapshotRetry|MalformedRetry|MissingApplied|NonceRejection|UnixGrant|ClientLostPublishedAck|CanonicalReadFailure)`,
-count=1 and -timeout=90s. Real QMDB Import without CommitQC yields retry and no
retained credits; after 5/7 commitment the same frame succeeds on the same
connection. CryptoVerified counts six verification attempts while NewlyAdmitted
counts three unique transactions in that case. Header-only retry reads no body;
missing applied markers and nonce rejection remain fatal; malformed retry data
is unknown and never replayed. Targeted sharedexec vet passed. Race and fresh binary validation of this optional
extension remain separate from the frozen b02d84a2 eight-process fixture.

## Provisioning from an existing signed corpus

`gov5-perf-provision --supply-corpus /dedicated/supply/s192a
--run-root /dedicated/runtime/fresh-name --base-port 28000` is a separate opt-in
preparation path. It writes schema 2 with a `supply` binding (source root, exact
manifest SHA, corpus ID, genesis file SHA and per-validator pacing); it does not
pretend to have the v1 `load_keys` fixture. `--supply-block-interval-ms` defaults
to 2000 and can explicitly set runtime pacing without changing genesis. The
source manifest, genesis and all seven validator keys are checked before a new
runtime root is created. The output genesis is an exact byte copy. Each fresh
validator receives only its corresponding BLS key and a new P2P identity; the
owner directory stays empty. Sender seeds/keys and chain databases are not read
or copied. Existing roots are never overwritten.

Twelve provision/checker tests pass (0.084s), including the unchanged v1 fixture
checks. Small actual-key fixtures verify exact source genesis bytes, all seven
corresponding BLS keys, newly generated P2P identities, effective owner-queue
configuration, manifest binding and no source file changes. Wrong genesis,
wrong/order-swapped BLS keys, source aliases, invalid pacing and noncanonical
validator addresses fail before creating the output root; FIFO source input
fails without blocking. Review caught a mismatch between normalized address
lookup and raw-address key filenames; exact address validation and canonical
filenames now agree with node startup. Targeted provision vet passed.

This slice only prepares the schema-2 runtime. Its full shard SHA precheck,
strict replay driver and fleet schema-2 validation remain subsequent work.
Neither preparation nor these tests prove real queue throughput. The frozen
b02d84a2 eight-transaction functional worker remains independent.


## Functional runner claim ownership

The b02d84a2 targeted race suite and Make build completed successfully, and its
three immutable binaries and fresh q1016a fixture are retained under
`validation/20261016-gov5-ownerqueue-b02d84a2`. The eight-process queue run has
not started. During the build, another gov5 driver replaced the shared gov5
claim (epoch 1792124415, 2026-10-16 04:20:15 UTC) and started its n42-r143 fleet
at 04:20:39 UTC. Therefore this build interval is not evidence of exclusive
hardware use. No throughput was measured in that interval. Its files and
processes were left untouched.

The functional runner now takes distinct Codex epoch claims in both BOX
locations, plus the legacy gov5 mirrors. It checks immutable inode/epoch and
its private owner token before every spawn, during owned child waits, after
each status RPC, before successful evidence publication, and after cleanup.
Resource scans reject new or unreadable foreign claims and any unrecorded
workload. Only runtime load/memory thresholds and recorded owned PIDs are
exempt; quiet-start thresholds still apply. These are bounded polling checks,
not a continuous watchdog or an OS resource lock. On contention it stops only
its recorded children, preserves foreign files, releases only its own claims,
and yields for 60 seconds. A result file alone is insufficient: the runner must
also exit successfully after its final ownership check.

Twenty-four Python fleet/queue-fixture tests pass, covering replacement even
with the same epoch, foreign claims without any process, runtime contention
yield, and status checks after successful or failed IO. The q1016a root and its
original genesis are preserved; the pending run reuses the b02d84a2 binaries
with this separately pinned runner, without rebuilding or reprovisioning.
