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
