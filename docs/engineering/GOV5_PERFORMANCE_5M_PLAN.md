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

`network_namespace` is a planned protocol identity, not an implemented P2P
topic override. Existing generic `/rpc` transport, HotStuff topics, frame
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
