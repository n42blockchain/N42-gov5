# Gov5 performance chain: isolation and execution plan

Date: 2026-10-14. Branch: `perf/gov5-5m-isolated-20261014`.
Source worktree: `/data/blockchain/gov5-work/wt-s104-resume`, clean at
`49e03e3e170992f3ea6a03b43d7b2c17f26aa1c4` when this branch was created.
Implementation worktree: `/data/blockchain/gov5-work/wt-gov5-performance-5m`.

## Baseline and evidence

The target is 5,000,000 executed transactions/s on an isolated Go performance
chain with seven consensus processes. The principal candidate uses one shared
Go executor; seven independent executors remain the correctness/control path.
Neither candidate execution nor performance transaction support is implemented
by stage 0. Existing main-chain and Ethereum EL/CL defaults are unchanged.
The original Go worktree (`qs/block-time-budget`) has extensive user changes;
it and `/home/n42/src/n42/n42-rs` must not be edited by this effort.

E1 is an explicitly trusted execution source with a common failure domain:
seven consensus votes do not independently prove the shared executor's state
transition correct. Matching chain/block identity, roots, receipts and gas
checks result consistency and binding, not independent execution correctness.
Any E1 deployment must state this trust assumption, retain the E7 independent
execution control and independent replay/audit, and measure executor failure
and recovery. Asynchronous audit is not a proof available before voting.

| Evidence | Result or qualification |
| --- | --- |
| S103 / n42-r139 | Standing best: 776,967 executed TPS. Logs: `/data/blockchain/wr-logs/r35zzzcc.log`; harness: `/data/blockchain/gov5-work/run-r35zzzcc.sh`. Attestation on; 163k transactions/block, 125ms interval, 8 x 15M corpus, QS_MAXCPU=36, GOGC=100, GOMEMLIMIT=7GiB. |
| Existing n42-r139 artifact | SHA-256 `c0136bcc7eb8ffa677ab4661148643cc6bbc9bd5b6b71a8bbbb02fa7dfacd19c`; `go version -m` reports Go 1.26.8, tags nosqlite,noboltdb, but no VCS revision. This is a current artifact checksum, not proof of the historical measured artifact. |
| `869986c1` | S102 documentation commit only. It does not establish the exact reconstructible source of S103/r139. That provenance remains to be reconciled with preserved sources/artifacts. |
| Source `49e03e3e` | Includes P11/P12 work, bounded attestation waiting and safety repairs (recovery, overflow, archive bounds, prepared state buffer ownership). Safety fixes are retained; S103 performance has not been presumed inherited. |
| S105 | `/data/blockchain/wr-analysis/s105-20261013/result.json`: A1 681,883 / 643,850; B1 695,467 / 646,567; B2 690,033 / 652,000; A2 703,617 / 641,133. No promotion above S103. |
| Rust 4.24M reference | E1, Ed25519, single gateway, import-once, depth 2, 200k/block, 30ms. These workload/protocol/topology differences must be retained in comparisons; this is not a matched Go performance result. |

## Isolation contract

The dedicated profile lives at `scripts/gov5-perf/profile.json`; it is a
validation schema, not a new accepted value of the node's `--profile` flag.
The existing node interface is `--profile n42 --chain private --data.dir PATH`
and `n42 init GENESIS_PATH` for explicit custom genesis initialization.
Reuse `internal/node` datadir network/genesis binding when a launcher is added.
Stage 0 does not initialize a database or launch these commands.

The genesis template has chain ID 4205001, timestamp 1791936001 and a dedicated
extraData marker. Chain ID alone does not isolate P2P: the header/genesis hash
must also differ. The preflight reports a **genesis file checksum**, not a
computed genesis block hash. Dedicated validator public identities, genesis
allocations and runtime keys must be provisioned in a later stage, then the
computed genesis hash and observed P2P fork digest must be checked against the
existing Go fleet and ETH networks. The empty validator/alloc template is
deliberately not launch ready. Never copy the production seed or key files.

Stage 0 enforces canonical absolute runtime paths beneath
`/data/blockchain/gov5-performance-5m/runtime`, including symlink escape
rejection and refusal to reuse nonempty runtime directories. It reserves separate
ports (25000..25700 bases), loopback binding, disabled discovery, no bootnodes,
no public peers, no genesis override, and rejects known Go fleet/ETH ports.
Inherited N42_/QS_/HIVE_ variables are refused instead of silently altering the
profile. No environment file from `scripts/qs` is sourced. No chain data,
validator private keys, network private keys or JWT secrets belong in Git.

At stage 0, `network_namespace` was a planned protocol identity, not a P2P
topic override (stage 2 below implements the runtime boundary). Generic `/rpc` transport, HotStuff topics, frame
transport and genesis/status handshakes require explicit cross-network rejection
tests before any fleet launch. Shared execution must bind Unix sockets or
loopback and authenticate/bind every request/result to chain identity. Neither
profile validation nor a distinct chain ID proves full network isolation.

`bash scripts/gov5-perf/run.sh validate` performs only configuration checks.
`bash scripts/gov5-perf/run.sh preflight` additionally reads current TCP/UDP
sockets (IPv4 and IPv6), hardware claims, memory, load and workload process
names. A conflict returns nonzero. It never starts/stops processes, writes
claims, initializes nodes, reseeds data, deletes files, changes sysctls or reads
private key material. A successful snapshot is not a resource reservation.
Before a future heavy run, follow `/data/blockchain/wr-logs/BOX-CLAIM-PROTOCOL.md`:
three quiet checks, claim both locations, contention delay and reread, heartbeat,
release both claims. Keep this campaign's stronger 90GiB launch floor and 20GiB
runtime watchdog. Active unclaimed cargo/rustc/Go/chain workloads also block.
An eventual launcher must acquire resource ownership atomically and recheck
conflicts before initialization/start; the check-only tool does not eliminate
the gap between a snapshot and a launch. Atomic acquisition and coordinated
contention handling are still to be implemented and reviewed.

## Implementation stages and actual seams

1. Stage 0: isolated profile, read-only fail-closed preflight, baseline accounting,
   and regression tests for path/identity/port/resource rejection. Complete these
   without fleet launch or heavy tests while other sessions use the machine.
2. Add an explicit optional performance-chain configuration with default nil.
   Keep native/ETH signer rules and preset selection unchanged. Validate role
   selection; reject shared roles until a real transport/backend is connected.
   Performance Ed25519 must be gated in ChainConfig; signer caches keyed only
   by chain ID cannot distinguish mode/activation. `LatestSignerForChainID`
   retains Ethereum semantics. No generic acceptance of new transaction types.
3. Wire the Go shared executor at the existing separation between availability/
   deferred execution (`internal/lag_pipeline.go`, includability and applied/durable
   gates), `StateProcessor.Process`, `blockchain_write.go` and node construction
   in `internal/node/node.go`. Seven HotStuff instances retain separate BLS
   identities, WAL/recovery, validation and voting. One writer owns execution
   state; followers check parent/hash/root/receipt/gas/result identity and
   consistency. E1 accepts a trusted execution source explicitly; these checks
   are not equivalent to independent follower execution or a validity proof.
   Shared database access alone must not silently change that trust boundary.
4. Implement import-once, bounded queues/backpressure, authenticated chain-bound
   execution results, failure/restart/reorg handling and independent execution
   equivalence tests. Feature flags select independent/shared modes explicitly.
5. After resource clearance, run matched control/candidate windows with workload,
   root/receipt/gas consistency, executed head, lag, CPU, memory and disk evidence.
   Publish topology/protocol differences and safety outcomes beside TPS. Advance
   toward 1M/2M/5M only as measured results justify each stage.

## Stage 0 validation boundary

Run lightweight Python regression tests and shell syntax checks now. Go source
is unchanged in stage 0, so no Go compile, full tests, benchmark or race run is
needed for this commit. `make build`, appropriate Go tests/race checks and lint
remain required before a subsequent Go-code PR. On initial inspection, another
session was running cargo tests without a claim; no fleet or heavy build was
started here. Hardware snapshots must be refreshed rather than treated as
permanent permission.

## Stage 1: explicit configuration and startup rejection

`params.ChainConfig.Performance` is optional (nil in every existing preset).
Version 1 permits only `independent` execution, `local` role and
`ethereum-secp256k1` transactions on private+n42, chain ID 4205001, HotStuff,
QMDB and the dedicated namespace. Shared execution, validator-only roles and
Ed25519 remain rejected as unimplemented. This field does not modify signer
selection, transaction decoding, consensus voting or the executor.

The node flag `--performance.genesis /absolute/path/to/chain.json` selects the
actual explicit genesis. Startup verifies the configuration, timestamp and
extraData before opening the chain database; the performance flag cannot turn
a nil-performance genesis into a performance chain. No ordinary devnet genesis
is synthesized for this selection, and an empty database requires explicit
`n42 init` rather than automatic initialization. Init validates the actual
genesis before `OpenDatabase`, and P2P genesis overrides are forbidden.

The existing `network.json` binding records the optional performance
configuration and rejects changes or removal before startup opens chain MDBX.
Stored chain configuration and genesis hash are checked again when read from
the database. `CheckCompatible` rejects any addition/removal/change to
performance configuration at every height, including genesis; rewinding does
not make such a change valid. Existing manifests and serialized chain configs
omit this field when nil. The template is still not launch ready because keys,
validator identities, allocations, protocol isolation and resource acquisition
are not provisioned by this slice.

Validation for this slice: complete `params` and `conf` package tests; targeted
node startup/profile/genesis/manifest tests including the default nil node
lifecycle; targeted `cmd/n42` init tests; `go vet` for these four packages; the
11 Python preflight tests; and the Make build target with `GOMAXPROCS=2`,
`GOFLAGS=-p=1` and automatic version bump skipped. `make lint` was attempted
and exited 2 because `golangci-lint` is not installed. No fleet, benchmark,
full repository test/race campaign or new TPS measurement was run.

## Stage 2: runtime P2P isolation

An explicit performance ChainConfig now reaches `p2p.NewService` through
`WithPerformanceChain`. Nil retains the existing native/ETH service behavior.
Performance configuration is cloned, discovery and QUIC are disabled, and
listen/advertise addresses default to IPv4 loopback. Bootnodes, public/DNS/
IPv6 peers, relay, UPnP and genesis overrides are rejected before node storage
opens. Only explicitly configured static peer IDs are allowed on outbound
dials and authenticated inbound connections; score protection cannot bypass
the allowlist. IPv6 support remains outside this slice.

The Noise protocol ID contains namespace, chain ID and the full genesis hash;
the same identity is also set as Noise's authenticated prologue. Performance
hosts do not register the legacy Noise protocol or QUIC transport. Actual
application RPC protocol IDs and gossip topics carry that scope. The host
adapter covers NewStream and Set/Match/RemoveStreamHandler, including direct
block push/fetch and Rotor callers. Logical stream IDs remain available to
existing rate limiters/decoders. Sync validator registration, duplicate topic
detection, decode and unsubscribe use matching wire/logical names.

Bounded loopback tests exercise same-chain connection and rejection of legacy,
non-explicit and different-full-genesis peers (including identical first four
genesis bytes), scoped RPC negotiation and removal, and real gossip delivery.
A sync topic lifecycle test checks validator removal/re-registration and
scoped decoding; default nil/profile/gater regressions remain unchanged.
These tests are not a seven-node fleet, execution benchmark, exhaustive
adversarial P2P review or a separately isolated proof of Noise prologue behavior.
External ingest/shared-execution transports, dedicated validator provisioning,
resource ownership and shared execution remain to be implemented. Another
session was building Rust during this slice, so only restricted Go tests/vet
were run; no heavy Make build or fleet was started.

## Stage 3: real standalone Go execution and Unix import-once transport

`internal/sharedexec` owns an actual `*internal.BlockChain` and a Unix HTTP
import RPC; this is implemented execution, not a cache backed by a stub.
`cmd/n42-execution-service` opens an already initialized execution datadir under
the dedicated performance runtime, verifies genesis and complete stored config,
takes the existing `LOCK`, initializes public HotStuff verification/rewards and
loads QMDB. It starts no miner/P2P/voting service, calls no Authorize/Seal and
loads no validator private keys. Explicit public validator provisioning and
`n42 init` are required; the checked-in empty template remains not launch ready.
Inherited N42_/QS_/HIVE_ overrides are rejected in this prototype.

Requests contain full chain identity (version, namespace, chain ID, genesis),
block hash and original RLP block bytes. Unknown JSON fields, sender claims,
bad RLP, header/hash/body-root mismatches and frame bodies are rejected. Bounds
are 64MiB raw block, 200k transactions, bounded JSON/header/response sizes and
read/client deadlines. The Unix socket uses a private directory and mode 0600;
existing paths and symlinked/public directories are refused. Unix permissions
authorize local clients; there is no public TCP execution endpoint.

The registry key includes full identity and block hash. Capacity is bounded;
active owners are never evicted. A requester cancel does not release ownership
while the real import continues, so another client cannot duplicate that work.
Missing-parent responses are **accepted**, not executed, and are not cached.
Import errors are not retained. A **durable** response requires actual applied
lineage, the QMDB applied marker, persisted header and matching receipts/root/
bloom/gas. Checked/executed/durable flags become true only at that evidence
level. This prototype deliberately rejects all deferred execution configuration
and frame transport: nil from InsertChain or held availability is never treated
as an execution proof. It does not yet stream checked/executed progress before
durability, perform authorized branch switching or build remote candidates.

The small Unix RPC test sends one signed secp transfer block from seven separate
clients. All seven obtain matching durable results, the actual import call and
executed hook each occur once, and the recipient balance changes from 1 to 24.
Real Go StateProcessor/EVM/QMDB/receipts run; the fixture uses a test-only header
engine, so this is not a HotStuff quorum test. Other tests exercise wrong chain/
hash/body roots, malformed payloads, sender-claim rejection, unavailable-parent
retry, canceled requester ownership, non-eviction of active jobs, deferred/frame
rejection, and socket/path permissions. Restricted package tests and vet pass.
No heavy build, race campaign, fleet or new TPS measurement was run while the
Rust claim/load/memory gates block the hardware.

Seven full validator nodes are **not wired to this service yet**. Shared roles
remain rejected by ChainConfig. Next: wire availability/import gates and result
delivery into validator-only runtime, preserve each validator's BLS/WAL/locks,
and add owner-side unsigned candidate building for a leader to sign/propose.
The executor must never acquire the seven consensus signing keys. Deferred
results, authorized reorg handling, single-copy payload delivery and E7
comparison must be implemented and tested before claiming a complete E1 fleet
or a throughput improvement. Ed25519 remains a later explicit protocol slice.

## Stage 4: trusted-owner canonical commit RPC

`POST /v1/commit` and the Unix client's `Commit` method now bind full chain
identity, block hash, height and parent. Commit is disabled by default. The
standalone service can explicitly opt in using `--commit-owner-file`, an existing
absolute nonsymlink regular file owned by the current user, mode 0600, containing
exactly 32 nonzero random bytes. Provision this capability outside the repository
and give it only to the trusted finality owner, never to ordinary import clients.
No validator BLS/private keys enter this process. Local users with access to the
service account are within this trust boundary; a capability does not sandbox
malicious processes running as that same user.

This slice accepts a **trusted single owner deciding finality**, not a client QC
proof. The owner must decide finality before invoking the RPC. It is not wired to
seven independent validators yet. `HotStuffCommittedHead`, written by the reused
canonicalization path, records this trusted-owner decision in this service; its
presence here must not be represented as independent verification of a commit QC.
A later validator integration must restrict owner calls to authenticated consensus
commit events or replace this capability boundary with actual public QC validation.

The standalone service exclusively owns the database through the existing datadir
LOCK and serializes import and commit. It reads the stored block, checks request
identity and current canonical prefix, and rechecks actual applied lineage,
persisted execution result commitments and receipts before calling the real
`BlockChain.CommitToCanonical`. Only a direct child of the current canonical head
can advance it; an already canonical block is idempotent without rolling the head
back. Missing blocks, missing parent commits, wrong heights/parents, stored-only
blocks and conflicting forks below the canonical head are rejected. Arbitrary
reorgs are deliberately unavailable in this slice. After the transaction returns,
the RPC independently reads canonical/head/finality rows before reporting committed.
A canceled/lost reply can be retried idempotently. The existing transaction hook
cannot enforce authorization because its errors are swallowed, so this service
does not use it as an authorization gate.

The real BC/StateProcessor/EVM/QMDB fixture now computes and checks state roots
in its test header engine and matches the importer's detached credit setting.
The Unix test imports two real blocks without advancing canonical, rejects
unauthorized/wrong-chain/height/parent and skipped-parent commits, commits both
sequentially, repeats older and current commits, and rejects a stored-only child
and a conflicting finalized fork while checking durable finality rows. This is
still a test header engine, not a seven-validator QC proof. Capability file tests
reject public permissions, symlinks, empty/zero/oversized values. Validation:
`GOMAXPROCS=2 go test -p 1 ./internal/sharedexec ./cmd/n42-execution-service -count=1`
and restricted vet for these packages. Rust ownership still prohibits a fleet,
heavy build or throughput run. No new TPS is claimed.

Next independent slice: unsigned owner-side candidate building, followed by a
separate validator runtime integration. Shared roles remain fail-closed until
that runtime is wired; current capability authority is not automatically given
to seven import clients. Deferred results, authorized branch switching, payload
copy reduction and E7 comparison remain outstanding.

Stage 4 follow-up: BlockChain's background future-block retry uses its own
import path, outside the RPC registry lock. The performance-only
`CommitExecutedExtension` now holds the chain mutation lock, joins an in-flight
ordered writer, and rechecks actual applied lineage and direct canonical parent
before invoking existing canonicalization. Native/ETH canonicalization entrypoints
are unchanged. The targeted mutation-lock test changes the applied branch while
the committer waits and proves it rejects stale execution evidence without writing
head/finality. The real two-block Unix RPC test also passes through this guard.
Old canonical duplicates still require execution evidence within HasAppliedBlock's
256-header lineage window; older requests fail closed until historical execution
proof transport is implemented. Restricted internal targeted tests, the two RPC
package regressions and vet pass; no fleet or throughput measurement was run.

## Stage 5: real unsigned candidate Build RPC

`POST /v1/build` and the Unix client now accept full chain identity, a hash of
the entire template RLP and its bytes. The leader prepares this template in its
own consensus process: proposer, view, embedded QC and a reserved zero BLS seal
are already present, while root/receipts/bloom/gas and fork result fields remain
unset. The executor checks canonical extra encoding, the public view leader,
existing public header/QC checks, parent and deterministic prepared timestamp.
It does not call Prepare, Authorize or Seal for that leader. The returned block
still has a zero seal. Signing and proposing remain the leader's responsibility.

The actual BlockChain builder holds its state mutation lock through the snapshot,
parallel execution, block end and FinalizeAndAssemble. Lock order is
`server.execute -> bc.lock`; no path reverses it. An in-flight ordered writer is
joined before the read snapshot. The parent must be the actual applied head,
and NewMinerRootComputer supplies an isolated QMDB tree. Fresh initialized
genesis may lack the applied marker; only its exact canonical/head identity and
actual loaded live QMDB root admit the first build without writing a marker.
Above genesis a missing marker is rejected. BuildParallel performs block start
once; block end and public finalization follow in the same order as the miner.
No live state, applied marker or canonical rows are written by the build.

The first builder deliberately rejects the whole template if BuildParallel's
lenient pass drops any candidates (including nonce gaps), and rejects its
fee-recipient fallback case. It never labels a subset as the requested complete
candidate list. Leader-side repacking and a serial fallback are later work.
Deferred/frame, BAL/mobile/DAO adapters, committee evidence, H2-v4 and future
validator-set epoch transitions remain rejected in this slice. Existing nil
performance/native/ETH behavior and chain-level role validation are unchanged.

Ownership/cache keys include full chain identity and Keccak(template RLP),
binding view, proposer, QC, header and transactions rather than just parent.
Capacity and a 128MiB retained payload budget bound the registry. Active jobs
reserve their owned input plus output allowance; completed jobs account for the
actual encoded output length. No decoded candidate is retained in the cache.
Workers decode/build one candidate at a time; the HTTP route also serializes
JSON framing to avoid seven full templates decoded simultaneously. Caller
responses are defensive copies. The retention budget excludes caller-owned
responses, JSON framing buffers and the single active decoded execution state;
it is not a total process RSS claim. Each request still has raw 64MiB/200k-tx
bounds, and the aggregate reservation can reject a near-limit template even
with an otherwise empty registry. Hash-only waiting and single-copy fanout are
later optimizations. Canceling a waiter leaves the build owner active, active
jobs cannot be evicted, failed jobs are not cached, and Close drains jobs and
releases retained cache bytes.

Real tests use a public HotStuff executor with no signing key, actual
BlockChain/StateProcessor/EVM/QMDB, and a separate test leader that prepares and
signs externally. Build leaves live root, DB balances, applied/canonical rows
unchanged; signed import subsequently matches its root/receipt/bloom/gas and
changes the recipient from 1 to 24. Wrong chain/header/seal/parent/height and
nonce gaps are rejected; a legal build after failure still succeeds. Different
views do not alias, modifying a returned payload cannot corrupt the cache, and
canceled waiters preserve one owner and the active/completed byte accounting.
Restricted sharedexec/standalone regression tests, public verifier and commit
regressions, and vet pass. Targeted race/full Make validation awaits the final
runtime slice and quiet hardware checks. No fleet or TPS test was run.

**Build and Import currently each execute once.** The differential test is not
prepared own-build reuse or Rust-style execution-once proof. Safe prepared-state
reuse, with complete final sealed-header checks, is a later separate slice.
Cached candidates refer to the snapshot they were built on; a runtime leader
must still confirm its parent/QC/view before signing, rather than treating a
historical cached candidate as a current proposal. Shared-validator/deferred
roles remain fail-closed until their actual runtime gates are wired.

Next: a public commit-QC bridge and validator process runtime separation using
NodeConfig/CLI roles and endpoints. All processes must retain the same genesis
ChainConfig; process-specific roles must not be placed in the immutable chain
configuration. Owner finality capability stays with one explicit bridge and is
never distributed to seven ordinary execution import clients.

## Stage 6: public CommitQC transport and durable retry

`POST /v1/commit-qc` and `Client.CommitCertified` now accept a complete public
CommitQC instead of the prototype owner capability. The execution service checks
full RPC chain identity/hash/height/parent and actual durable execution. Under
the chain mutation lock it verifies the strict **Commit** signing domain, a
5/7 quorum against exactly the fixed seven-member genesis key/address set, and
the stored header's canonical extra, view proposer and real BLS seal. It then
uses the same append-only executed canonical helper. Duplicate requests verify
the certificate again. PrepareQCs, 4/7 certificates, invalid seals, wrong chain
identity, unexecuted blocks and conflicting canonical forks cannot authorize it.
No owner capability or validator secret enters this transport/executor.

The current legacy QC signature domain is phase/view/block hash. It does not
sign chain ID/genesis explicitly; those are bound by RPC identity, the frozen
genesis validator set, and the locally stored sealed block's ancestry. This is
not an H2-v4 chain-bound signature claim. H2-v4, committee evidence and
validator-set changes remain rejected in this performance bridge.

HotStuff Service now has an explicit optional CommitCertificateSink. Its actual
OutputBlockCommitted handler preserves and passes the complete output QC.
Before sending, it persists the full certificate and a pending marker with the
resolved consensus/WAL snapshot in its own transaction. A durable remote ACK
only clears the pending marker; the full proof remains for audit. Failed/lost
ACKs retain the original certificate. Startup, block import, embedded header
QCs and an optional one-second retry loop replay pending proofs in height order.
The pending set is bounded at 256; it cannot silently drop older committed
proofs. Persistence failure stops the opted-in service, and malformed persisted
records fail closed. Ordinary nil/native/ETH services keep their existing path.

CertificateBridge builds hash/height/parent requests from the validator's local
metadata database and checks its genesis/config against the client's identity.
It sends public certificates only. The validator store and executor store are
different databases: ACK and local WAL updates have no cross-database atomicity.
This slice deliberately does not pretend remote ACK advanced the validator's
local canonical/head metadata; the next runtime adapter must implement that
separate recovery state machine, preserve availability and independent vote WAL,
and use validated remote execution evidence for its vote gate.

Real Unix/BlockChain tests prove 4/7 rejection, 5/7 Prepare-domain rejection,
5/7 Commit acceptance, duplicate revalidation, wrong identity and unavailable
execution rejection. A separate metadata/WAL database forwards a valid proof,
loses the ACK after the remote canonical transaction actually commits, and
restarts/replays the preserved certificate without re-executing the block or
changing its local canonical head. The actual Service output handler test checks
complete certificate/pending/WAL durability before its sink is called and proves
the sink cannot mutate the saved proof. Restricted sharedexec/CLI, Service/default
regressions, performance canonical regressions and vet pass. No fleet, new TPS,
full seven-validator runtime or prepared own-build execution reuse is claimed.

Next independent slice: a lightweight validator metadata/header/body/WAL runtime
and process roles in NodeConfig/CLI, keeping every process on the same immutable
genesis ChainConfig. It must avoid local NewBlockChain/NewMiner execution paths
and advance local metadata only after authenticated execution and commit evidence.

## Stage 7a: lightweight metadata replica and remote vote gate

`internal/sharedvalidator.Replica` now provides an actual metadata/header/body
store and `consensus.ChainHeaderReader` without a local execution BlockChain,
StateProcessor, root computer or miner. It initializes only genesis metadata,
binds the complete stored configuration/identity, rejects existing account state
or unbound stores, and reopens the same bound metadata after restart. All seven
replicas and the executor use identical genesis/ChainConfig. No chain-level
Role/Mode is changed for a different process.

Replica.Import first checks the actual sealed proposer/header and body root and
persists block availability. It then sends the original bytes to the real Unix
execution service. Only a checked/executed/durable response matching identity,
hash, parent, height and header result commitments becomes persisted execution
evidence. BlockApplied checks that evidence plus local header/body availability;
accepted/missing-parent results remain unavailable for voting. AttachService
wraps the supplied real block fetcher with this gate. Import releases its mutex
before notifying Service, preserving the certificateMu -> replica.mu order and
avoiding a retry callback deadlock.

The replica is also the public CommitCertificateSink. It requires local block
availability and remote execution evidence, rechecks the full CommitQC/seal,
waits for the execution service's authenticated canonical ACK, and only then
atomically appends its local canonical/head/finality metadata. Local and remote
transactions remain separate. If the local transaction rolls back after remote
success, Service retains the complete pending proof and an idempotent retry
converges both stores. It never rewrites a conflicting finalized local prefix.

The real integration tests create seven independent metadata/WAL databases,
seven individually authorized test consensus engines and seven Unix clients on
one identical genesis. Concurrent imports execute only once in the owner; all
seven account tables stay empty. Four votes cannot advance metadata; a valid
5/7 CommitQC through each Service outbox advances all seven only after remote
ACK. Restart preserves bound execution/finality evidence. A stored child whose
parent is not remotely applied stays accepted and cannot satisfy BlockApplied;
parent import followed by child retry converges. An injected local canonical
transaction rollback leaves the remote committed, local unchanged and full QC
pending; retry repairs local metadata with no extra execution. Tests live in the
external sharedexec integration suite using test-only fixture exports, so no
production fixture interface or execution stub is added.

Restricted sharedexec/standalone regression tests, Service/default regressions
and vet pass. These are seven replicas/engines inside small tests, **not seven
running validator processes or a measured consensus fleet**. NodeConfig/CLI
dispatch, real P2P body fetch/catch-up and remote leader production are the next
independent runtime slice. Process roles are not exposed as runnable yet, and
existing shared/deferred chain roles remain rejected. No new TPS is claimed.

## Stage 7b: scoped metadata body fetch and certified catch-up

The lightweight validator now has a real block source over its performance
P2P host, using `/n42/shared-validator/blocks/1` under the existing full-genesis
Noise/protocol namespace and static-peer allowlist. Construction rejects a host
without that actual performance scope. It serves metadata blocks by hash or
canonical number and returns saved public CommitQCs, without using normal sync
or entering a local execution pipeline.

Frames are bounded at 4KiB metadata and 64MiB raw block, with strict identity,
single-response framing and ten-second stream deadlines. Two inbound streams,
128 pending hashes and one fetch/one catch-up worker bound concurrency. Hash
fetch retries missing availability/applied parents through Replica.Import; only
durable remote execution evidence reaches its vote gate. Catch-up trusts peer
height only when a 5/7 CommitQC authenticates the supplied head header hash,
then fetches at most 64 canonical blocks/proofs per pass. A peer's unproven
height claim cannot shut off leader production. Each local canonical advance
still passes the full public certificate/remote ACK path.

Stop removes the handler, cancels workers, resets tracked streams and waits for
their handlers/requests to finish; concurrent/idempotent Stop waits for the same
drain. The real two-host loopback test first fetches an uncommitted signed body
and observes execution evidence without canonical advancement. After the origin
stores a valid CommitQC, certified catch-up converges both metadata stores with
the owner import count still one. It checks the actual genesis-bound Noise
security protocol and drains a held incomplete request on Stop. Frame tests
reject oversized lengths before reading/allocating a body and truncated frames.
Restricted regressions and vet pass; this is not a seven-process fleet or TPS run.

Historical imports/commits older than the executor's 256-header applied-lineage
window still fail closed. This transport does not invent historical execution
proofs or silently widen that guard. Durable historical execution evidence,
parallel/hedged fetch latency and payload-copy reduction remain later work.
Next: actual remote leader producer, then NodeConfig/CLI lifecycle dispatch for
the complete lightweight runtime, with unchanged chain-level genesis roles.

Stage 7b correctness follow-up: HotStuff.Finalize intentionally treats zero root
as its build path, while the generic BlockValidator does not independently check
state root. Performance-only sealed VerifyHeader/VerifyHeaders and shared Import
now reject zero state/receipt commitments before execution. Otherwise an imported
signed zero-root block could be misreported as verified execution. Genesis and
explicit unsigned Build (seal=false) retain their intended unset fields;
nil-performance/native/ETH verification is unchanged. The regression signs such
blocks with a real test leader and proves both the Unix executor entry and direct
performance BlockChain batch import reject them without advancing state/head.

## Stage 7c: remote leader producer with per-view candidate WAL

The metadata validator now has a real BlockProducer: it prepares a template in
its own initialized consensus engine, calls Unix Build, verifies the returned
prepared fields/results, seals locally, and persists the sealed body plus a
per-view parent/hash binding before remote import or proposal. Only durable
remote import followed by a still-current leader/view can call the real
consensus proposal path through a performance-only view/leader/parent/phase
check held under the engine lock. Retrying/reopening a producer in that view loads the
same sealed body instead of building another payload. Hash-only finality through
the producer is refused; CommitQC remains the only runtime commit authority.

The async queue owns normalized Ethereum-wire transaction copies, signature
recovery ignores caller sender claims, and encoded retention/count are bounded
at 64MiB/200k. Canonical inclusion prunes the volatile pool. Uncommitted ancestor
transactions are excluded only for that parent's child, rather than discarded
before finality. This is an exact valid-batch prototype: failed candidate
selection/nonce/gas cases still require repacking instead of silently returning
a subset. The pool is volatile, and general mempool replacement/repacking and
single-gateway intake remain later optimization work.

The optional certificate-sink production gate now checks the actual wrapped
BlockApplied evidence. A metadata store's absent QMDB marker cannot trigger the
legacy diagnostic gate's default-true behavior. Nil/native services retain that
behavior. Parent-unavailable production parks/fetches and the existing durable
import notification resumes it. Producer Stop cancels active Unix requests,
drains attempts and releases its queue. A view change/cancel does not publish a
newly built candidate for the obsolete view.

The real transfer test observes the actual consensus Proposal after candidate
WAL durability, keeps canonical at genesis until 5/7 QC, and proves a new producer
object with an empty pool reuses the existing sealed candidate with BuildCalls=1
and ImportCalls=1. A public observer cannot produce and unsigned admission fails.
An async attempt blocked at the real Unix transport is canceled/drained before
any execution/proposal. Restricted regressions/vet pass. The one Build and one
Import still represent **two executions**, not prepared-state adoption.

Before exposing the complete Node role, view-change recovery must handle an
already executed orphan whose parent is the older HighQC. The proposed next
small slice re-proposes the valid child on the owner's actual applied lineage
instead of constructing a competing sibling or rewinding world state. Node
lifecycle/CLI and runtime transaction ingress follow after that. No running
seven-process fleet or new TPS measurement is claimed here.

### Candidate root cleanup regression

A view-two build/import differential exposed an empty-account discrepancy for
the test proposer at RIPEMD160 address 0x03. Parallel fee folding touches the
zero-fee recipient after the worker transaction cleanup, while serial import
cleans that touch before finalization. The performance candidate builder now
runs standard FinalizeTx cleanup after the fold and before block-end/finalize.
The real HotStuff import must match all root/receipt/gas commitments. Fixtures
with public validator sets now construct their StateProcessor with the real
public HotStuff engine; changing BlockChain.SetEngine alone did not replace its
already constructed processor. Default native/ETH build paths are unchanged.

## Stage 7d: recover an executed child after a lost proposal

The identity-bound Unix AppliedChild query reads the owner's actual applied
lineage under the chain mutation lock and returns a stored sealed direct child
with fresh durable receipt/root evidence. It never rewinds or selects a fork.
Parent/height/full identity must match; future header views, unavailable ancestry
and requests outside the 256-block lineage window fail closed. Large reply
framing shares Build's serialization lock. Empty means the requested parent is
the exact applied head and no child exists, rather than an arbitrary lookup miss.

Before building, a leader with no per-view candidate WAL queries this endpoint.
An existing valid child retains its original proposer/header/seal; the new
leader persists that same payload hash in its own new-view candidate WAL, checks
remote execution, and signs the actual new-view consensus Proposal locally.
CommitQC remains required for canonicalization. Build/import do not run again
for that already durable payload; this is re-proposal recovery, not prepared
state adoption for the initial Build+Import pair.

Shared imports now use a performance-only exact-parent append gate under the
same BlockChain mutation lock as execution. Competing siblings remain retryable
and never enter execution or the generic authorized future queue; any generic
retry queue entry from this RPC is removed before releasing the lock. The
standalone owner has no miner/P2P import path. Native/ETH generic imports retain
their existing behavior. This single-lineage E1 prototype trusts local Unix
clients as previously documented; it does not establish Byzantine liveness
against a client deliberately submitting future-view candidates or add a
certificate-authorized branch-switch protocol.

Tests use an actual lost view-one proposal, a different independently keyed
view-two leader and empty metadata/transaction queue: it emits the real Proposal
for the same executed hash, then a strict five-signature view-two CommitQC
finalizes it. BuildCalls and ImportCalls remain one each; a fresh service with
empty registries can still return durable evidence from the database. Real
competing siblings cannot change the applied marker or execute; future-view,
wrong-chain/parent/height and malformed query requests are refused. Node/CLI
lifecycle and runtime ingress remain next; no fleet or TPS result is claimed.
