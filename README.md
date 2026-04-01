# N42 Blockchain

[![Go](https://img.shields.io/badge/go-1.25%2B-blue.svg)](https://golang.org)
[![GitHub Workflow Status](https://img.shields.io/github/actions/workflow/status/n42blockchain/n42/ci.yml?branch=main)](https://github.com/n42blockchain/n42/actions)
[![License: GPL v3](https://img.shields.io/badge/License-GPLv3-blue.svg)](https://www.gnu.org/licenses/gpl-3.0)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![GitHub Stars](https://img.shields.io/github/stars/n42blockchain/n42)](https://github.com/n42blockchain/n42/stargazers)

## Introduction

N42 is a high-performance, AI-native Layer 1 blockchain built in Go. It combines Ethereum-compatible EVM execution with a mobile-first consensus architecture, pluggable BFT consensus, and a complete AI safety infrastructure — all at the protocol level.

Featuring Block-STM parallel execution, HotStuff-2 BFT consensus, and mobile verification nodes, N42 delivers high transaction throughput with instant finality. Its permissionless framework enables effortless integration for building the next generation of decentralized AI-powered applications.

**Disclaimer:** This software is currently a tech preview. We will do our best to keep it stable and avoid breaking changes, but we make no guarantees.

Latest validation (2026-03-26): Hive/EEST broad consume-engine shard reruns are green on latest `main` for Paris+Shanghai (`3573`), Cancun (`17783`), Prague (`20964`), and Osaka (`21583`), with no remaining blocker in the current shard matrix.

## Key Features

### Consensus

- **HotStuff-2 BFT**: Two-round optimistic consensus with instant finality, BLS12-381 aggregate signatures, adaptive pacemaker, and dynamic validator reconfiguration
- **Authority PoS (APoS)**: Epoch-based validator rotation with tiered staking (token, NFT, testnet), checkpoint snapshots every 3000 blocks, and post-quantum STARK verification path
- **Authority PoA (APoA)**: Lightweight proof-of-authority for private networks and development
- **Mobile Verification**: Lightweight mobile nodes verify block state via BLS-signed attestations without re-executing EVM — enables smartphones as consensus participants
- **Rotor Single-Hop Relay**: Deterministic relay-based block propagation — leader sends proposals directly to stake-weighted relay nodes who forward to validators in one hop. SHA256-seeded selection ensures all validators agree without communication. Gossip fallback for liveness
- **On-Chain Randomness (0x0302)**: VRF beacon from CommitQC BLS aggregate signatures — unpredictable by any single validator (threshold VUF). Smart contracts access per-block randomness via precompile
- **Baby Raptr DA Verification**: Data availability commitment in HotStuff proposals — TxRootHash tracked per-proposal and verified post-import to detect invalid state transitions

### Zero-Knowledge Proof System

- **ZISK zkVM Fast-Path Proving**: Block producers generate ZK proofs after execution; validators verify proofs in milliseconds without re-executing the EVM
- **RISC-V64 Guest Program**: Standalone EVM execution binary (`cmd/zkguest`) compiled to RISC-V64 for zkVM proving, with pure Go EVM core (no CGO)
- **gRPC Prover Integration**: External ZISK prover cluster connection via gRPC, with configurable concurrency, timeouts, and proof types (STARK/SNARK/SP1)
- **Block Witness Infrastructure**: TracingReader-based witness capture during block production, binary encoding, LRU caching, and P2P witness request protocol

### Execution

- **Block-STM Parallel EVM**: Optimistic parallel transaction execution with multi-version state, 3.9x speedup on independent workloads
- **Deferred Execution Pipeline**: Consensus-execution separation for higher throughput
- **EOF (EVM Object Format)**: Full support including EOFCREATE, RETURNCONTRACT, and sub-containers
- **Blob Transactions (EIP-4844)**: Native support for blob-carrying transactions with V2 wire format
- **Pectra EIPs (9 complete)**: EIP-7702, EIP-2537 BLS, EIP-6110, EIP-7251, EIP-7002, EIP-7623, EIP-2935, EIP-7685
- **Glamsterdam Gas Repricing (EIP-7904)**: Simple transfers drop from 21000 to 4500 gas (-78.6%), data costs reduced 75%, contract creation 75% cheaper. Activated via timestamp fork
- **Dependency Prediction**: Pre-execution tx reordering based on contract+selector grouping — clusters conflicting transactions for better Block-STM wave efficiency
- **Predicted Executor**: Wraps Block-STM with prediction-based reordering, identity fallback for small batches (≤4 txs)
- **Performance**: 374 Ggas/s execution throughput (32-core), 661K TPS simple transfers, 153ns per EVM call, 86 Ggas/s batch processing

### State & Storage

- **Jellyfish Merkle Tree (JMT)**: Blake3-hashed state commitment with Merkle proofs and reference-counting GC for online pruning
- **MDBX Storage**: Memory-mapped B+ tree database with layered caching
- **Ancient/Freezer DB**: Automatic archival of historical data beyond a configurable threshold
- **Flat Snapshot Acceleration**: DiffLayer tree with MDBX persistence, journal crash recovery, and background generator
- **JMT Cross-Payload Cache**: 65536-entry LRU node cache persisted across block validations with CachedStore read-through layer, reducing MDBX reads 50-80%
- **Overlay State Warmup**: Pre-loads recent account state into ShardedCache on startup, eliminating cold-cache penalty after node restart
- **Storage Tiering**: Configurable NVMe/HDD split — hot data (chaindata ~50GB) on NVMe, cold data (history/indices ~800GB) on HDD, reducing hardware costs ~60%
- **LtHash Lattice State Digest**: Homomorphic hash for O(k) incremental state verification per block — `newDigest = oldDigest ⊕ BLAKE3_XOF(new) ⊕ BLAKE3_XOF(old)`. 2048-byte digest, 128-bit security. Runs alongside JMT for fast full-validator verification. Fork-gated via `LtHashTime`
- **PooledDBStore**: Long-lived MDBX read transaction for JMT backing store, replacing per-Get() transaction overhead. Auto-refresh after block commits. Combined with 128K node cache
- **JMT Batch Flush**: `BatchNodeStore` interface with `PutBatch()` for efficient bulk JMT node writes. `SnapshotDirty()`/`ClearDirty()` for pipeline commitment stage
- **Haystack JMT Archive**: Compressed historical JMT nodes in seg files with RecSplit O(1) perfect hash index for historical JMT proof/archive workloads

### Networking & Sync

- **5 Sync Modes**: Full, Snap, Checkpoint, Backfill, and Staged (7-stage pipeline with forward/unwind/prune)
- **P2P Networking**: libp2p-based with Kademlia DHT, peer scoring, rate limiting, and NAT traversal
- **ExEx Extensions**: Execution Extension framework for pluggable post-block processing
- **Decentralized Messaging**: 6-layer P2P messaging platform (relay, E2E encryption, RLN anti-spam, persistent storage, MLS groups, DID identity)
- **OtterSync (BitTorrent Sync)**: Export/import chain data as EraE segment files via BitTorrent, shifting ~98% of initial sync from CPU to network bandwidth
- **Stateless Validation Mode**: Verify blocks using only JMT Merkle proof witnesses (~10GB disk), no full state DB required. Foundation for lightweight mobile and edge validators

### High-Performance Pipeline

- **Deep Pipeline (5-Stage)**: Superscalar block processing — Prefetch ∥ Execute ∥ Commit ∥ Persist stages run on different blocks simultaneously. Channel-based backpressure with configurable depth, reorg recovery via Reset, halts on error to prevent state corruption
- **Tile Architecture**: Lock-free SPSC ring buffer IPC between pipeline stages. Cache-line-padded Lamport queue (~50ns/op vs ~65ns channel). Optional CPU core pinning via `runtime.LockOSThread()` + Linux `SchedSetaffinity`. Crash recovery with configurable auto-restart
- **Async I/O Prefetcher**: Request-generation goroutines parse transactions and dispatch to bounded I/O channel. Separate I/O worker pool performs MDBX reads concurrently with EVM execution
- **Predictive Slot Prefetching**: SLOAD access recording feeds PrefetchPredictor — learns hot storage slots per contract from real execution. Top-N predicted slots prefetched before next block. Periodic decay adapts to workload changes. Batch prediction with single-lock acquisition

### API & Tooling

- **Comprehensive JSON-RPC**: Broad Ethereum-style JSON-RPC coverage including `eth_getProof` (JMT-based / partial EIP-1186 semantics), `debug_*`, `trace_*`, Engine API v1-v4, Otterscan, and ZK proof endpoints
- **MCP Server**: Model Context Protocol server for AI-assisted blockchain interaction with 16+ tools
- **GraphQL API**: Full GraphQL endpoint with schema-driven queries
- **RPCDaemon**: Standalone RPC server connecting to core node via gRPC for read scaling
- **Mobile SDK**: iOS/Android SDK via gomobile with V2 wire format, BLS signing, and WebSocket/QUIC verification
- **`eth_getStorageValues` (EIP-7834)**: Batch storage value retrieval — query up to 1024 storage slots in a single RPC call
- **EraE History Format**: Standardized binary archive for blocks + receipts with random access via block number index

### AI-Native Infrastructure

- **AI Agent Wallets**: L1-native agent accounts with session keys (time-limited, contract-allowlisted, spend-capped), composable spending policies (rate/cap/allowlist), and gas sponsorship via paymaster
- **AI Inference Precompile (0x0301)**: Smart contracts call AI models on-chain — submit inference requests, read verified results, query model registry. Gas-metered with tiered verification (ZK/Optimistic/TEE)
- **ZKML Verification**: Zero-knowledge proofs of ML inference correctness — circuit generation from model structure, execution trace capture, proof generation and verification
- **AI Data Governance**: On-chain training data provenance with human ethics committee voting (quorum/threshold, secp256k1-signed ballots). Datasets must pass fairness, privacy, content safety, and transparency review before use in training
- **ZK Training Verification**: Cryptographic proofs binding trained models to approved datasets and training processes. Prevents model forgery and weight tampering
- **ZK Inference Attestation**: Signed attestations for inference results with chain-of-custody validation. Multi-hop pipeline support (perception→planning→control) for autonomous driving and robotics safety
- **AI Block Building**: AI-optimized transaction ordering with MEV detection, sandwich attack protection (fairness guard), and EWMA-based gas prediction
- **Agent Discovery**: P2P agent registry with capability-based discovery, task negotiation protocol, and weighted reputation system

### Distributed Infrastructure

- **ZK Coprocessor**: Off-chain compute with tiered on-chain verification (ZK/Optimistic/TEE), provider marketplace, and economic slashing
- **WASM Engine**: Sandboxed execution with fuel-based gas metering and host functions
- **Storage Bridge**: IPFS/Filecoin bridge, BitTorrent seeder, content-addressed storage precompile (0x0300)
- **Push Notifications**: Contract event → wallet stream delivery

### Cross-Chain Bridge

- **ZK-Native Bridge**: Cryptographic cross-chain verification via header proofs, state proofs, and evidence chains — no trusted relayers or multisig committees
- **Trust Chain**: HotStuff-2 BLS aggregate signature → SP1 ZK proof → JMT Merkle state proof → Ethereum on-chain verification (N42Verifier.sol)
- **Bridge Components**: HeaderProver and StateProver (`internal/bridge/`), Relayer for event monitoring and proof submission, Router for cross-chain message lifecycle
- **Solidity Contracts**: N42Verifier.sol (ZK proof verification) and N42Bridge.sol (asset lock/release) in `contracts/bridge/`
- **Phase 1 Complete**: N42 → Ethereum single-direction bridge operational

### Security

- **Post-Quantum Cryptography**: Falcon, Dilithium2/3, SQIsign precompiles (isolated activation via `PQPrecompilesTime`)
- **3 Rounds of Security Audit**: 47+ fixes across critical, high, medium severity
- **Encrypted Mempool**: Transaction privacy before block inclusion

## Architecture

```
cmd/
  n42/              Main entry point and CLI commands
  zkguest/          RISC-V64 ZK guest program (standalone EVM for zkVM)
  evmsdk/           Mobile SDK (iOS/Android via gomobile, V1/V2 wire format)
  rpcdaemon/        Standalone RPC daemon (gRPC remote KV)
  clef/             External signer (IPC + rules + audit log)
internal/
  consensus/        Pluggable consensus engines
    apoa/             Authority PoA
    apos/             Authority PoS (epoch, checkpoint, BLS, PQ-STARK)
    hotstuff/         HotStuff-2 BFT (2-round, pacemaker, reconfiguration, Rotor relay)
  miner/            Block production with MEV bundle support and witness capture
  txspool/          Transaction pool with persistence and encryption
  vm/               EVM execution engine with EOF, BLS, P256, CAS, AI precompiles
  sync/             Chain synchronization (full, snap, checkpoint, backfill, staged)
    torrentsync/    OtterSync BitTorrent chain sync (exporter/importer/manifest)
  api/              JSON-RPC backend (eth, debug, trace, zk, engine, witness, graphql)
  zkprover/         ZK prover service (STARK/SNARK/SP1, ZKML)
  zkverifier/       ZK proof verifier (block proofs, ZKML)
  mcp/              Model Context Protocol server (16+ tools)
  mev/              MEV relay + AI block optimizer
  bundler/          ERC-4337 account abstraction bundler
  deferred/         Deferred execution pipeline (3-stage + 5-stage deep pipeline)
  tile/             Tile architecture (SPSC ring buffer, CPU affinity, crash recovery)
  parallel/         Block-STM parallel executor + dependency predictor
  stateless/        Stateless block validation (witness-based, code cache)
  peerdas/          PeerDAS data availability sampling (EIP-7594)
  distributed/      Distributed infrastructure
    coprocessor/      ZK coprocessor (tiered verification, marketplace, slashing)
    compute/          WASM engine, batch MapReduce, AI inference (opML)
    messaging/        P2P relay, E2E encryption, RLN, MLS groups, DID, SSE streaming
    storage/          IPFS bridge, BitTorrent, eDonkey2000
    notify/           Push notifications
  ai/               AI-native infrastructure
    wallet/           Agent wallets (session keys, spending policies, paymaster)
    coord/            Agent coordination (discovery, negotiation, reputation)
    governance/       Training data governance (ethics committee voting)
    training/         ZK training verification (model provenance)
    attestation/      ZK inference attestation (signed results, chain-of-custody)
  bridge/           ZK-native cross-chain bridge
    header_prover     HotStuff-2 BLS → SP1 ZK header proof
    state_prover      JMT Merkle state proof generation
    relayer           Event monitoring + proof submission to target chain
    router            Cross-chain message routing and lifecycle
modules/
  state/            State management with JMT commitment and witness generation
  rawdb/            Raw database operations (MDBX + freezer)
  rawdb/era/        EraE history archive format (reader/writer)
lib/
  jmt/              Jellyfish Merkle Tree implementation (Blake3, 16-ary)
    archive/        Haystack JMT node compression (seg + RecSplit index)
    store/          Node stores (MDBX, Lazy, Pooled, Cached, Mem)
  lthash/           LtHash lattice state digest (BLAKE3 XOF, 2048-byte homomorphic)
  kv/               Key-value store interfaces (mdbx/, memdb/, remotedb/, layered/)
params/             Chain parameters, genesis configs (embedded JSON)
conf/               Node configuration (unified AICfg, all subsystem configs)
contracts/          Smart contracts
  deposit/            Tiered staking (token/, nftstake/, testnet/)
  bridge/             ZK bridge (N42Verifier.sol, N42Bridge.sol)
```

## System Requirements

- **Storage**: >= 200 GB (SSD or NVMe recommended; HDD not recommended)
- **Memory**: >= 16 GB RAM
- **CPU**: 64-bit architecture
- **Go Version**: >= 1.25 (current build/test environment: `go1.26.1`)

## Building from Source

### Linux and macOS

```sh
git clone https://github.com/n42blockchain/n42.git
cd n42
make n42
./build/bin/n42
```

Build the ZK guest program (requires no CGO):

```sh
make zkguest
# Output: build/bin/zkguest (linux/riscv64 ELF binary)
```

Build mobile SDK:

```sh
make android    # Output: build/mobile/android/evmsdk.aar
make ios        # Output: build/mobile/evmsdk.xcframework
```

### Windows

```powershell
go build -o build/bin/n42.exe ./cmd/n42
```

Or use Docker / WSL2 (recommended for development).

### Docker

```sh
make images   # Build Docker images containing N42 binaries
make up       # docker-compose up -d && docker-compose logs -f
make down     # docker-compose down && clean docker data
```

## Executables

| Command | Description |
|---------|-------------|
| **`n42`** | Main CLI client. Use `n42 --help` for options. |
| **`n42 migrate-jmt`** | Offline migration tool to build JMT state commitment. |
| **`n42 import/export`** | Block import/export (protobuf format). |
| **`n42 db`** | Database inspection (stats/list/get/inspect). |
| **`n42 state-dump`** | Dump account state to JSON. |
| **`zkguest`** | Standalone RISC-V64 EVM binary for ZK proving. |

## Network Ports

| Port  | Protocol | Purpose                      | Exposure            |
|-------|----------|------------------------------|---------------------|
| 61015 | UDP      | Discovery v5                 | Public              |
| 61016 | TCP      | libp2p Communication         | Public              |
| 20012 | TCP      | JSON RPC over HTTP           | Public              |
| 20013 | TCP      | JSON RPC over WebSocket      | Public              |
| 20014 | TCP      | Secure JSON RPC (JWT Auth)   | Authenticated       |
| 6060  | TCP      | Metrics & Profiling (pprof)  | Private             |
| 8553  | TCP      | MCP Server (AI agents)       | Private             |
| 8554  | TCP      | Message Stream (SSE)         | Private             |
| 9090  | TCP      | gRPC KV (RPCDaemon)          | Private             |

## Configuration

Key configuration options:

| Option | Type | Description |
|--------|------|-------------|
| `jmt_commitment` | `bool` | Enable JMT state commitment |
| `parallel_evm` | `bool` | Enable Block-STM parallel EVM execution |
| `prefetch` | `bool` | Enable state prefetching |
| `ancient_db` | `bool` | Enable ancient/freezer database |

ZK Prover (`ZKProverCfg`):

| Option | Type | Description |
|--------|------|-------------|
| `enabled` | `bool` | Enable ZK proof generation |
| `prover_addr` | `string` | gRPC address of prover cluster |
| `proof_type` | `string` | `"stark"`, `"snark"`, or `"sp1"` |
| `max_concurrent` | `int` | Maximum concurrent proof jobs |

AI Infrastructure (`AICfg`):

| Option | Type | Description |
|--------|------|-------------|
| `wallet.enabled` | `bool` | Enable AI agent wallets |
| `wallet.max_session_keys` | `int` | Max session keys per account (default: 16) |
| `wallet.paymaster_enabled` | `bool` | Enable gas sponsorship |
| `coord.enabled` | `bool` | Enable agent discovery and coordination |
| `governance.enabled` | `bool` | Enable training data governance |
| `governance.committee_quorum` | `int` | Minimum votes for review (default: 3) |
| `training.enabled` | `bool` | Enable ZK training verification |
| `attestation.enabled` | `bool` | Enable ZK inference attestation |
| `attestation.ttl_sec` | `int` | Attestation expiry (default: 86400) |
| `mev_optimizer.enabled` | `bool` | Enable AI block building |
| `mev_optimizer.fairness_mode` | `bool` | Enable sandwich detection (default: true) |

Glamsterdam Fork (`GlamsterdamTime`):

| Option | Type | Description |
|--------|------|-------------|
| `glamsterdamTime` | `timestamp` | Activation time for EIP-7904 gas repricing |

## Running a Node

```sh
# Mainnet full node
n42 --chain mainnet

# Testnet
n42 --chain testnet --http --http.api eth,net,web3

# Sync from bootnode
n42 --data.dir ./mainnet --chain mainnet \
    --p2p.tcp-port 10186 --p2p.udp-port 10185 \
    --p2p.min-sync-peers 1 \
    --p2p.bootstrap-node "enr:..." \
    --log.level info

# Development mode
n42 --chain private --p2p.no-discovery --dev.txgen

# Export chain history to EraE archive
n42 export-era --output history.era --start 0 --end 1000000

# Import from EraE archive
n42 import-era --input history.era
```

## Development

```sh
make build          # Compile all packages
make test           # Run all tests
make test-short     # Fast tests with -short flag
make lint           # Run golangci-lint
make check          # fmt + vet + lint
make race-core      # Race detection on core packages
make ci-full        # Full CI pipeline
make bench-smoke    # Quick benchmarks on core packages
```

### Test Suite

```sh
# AI infrastructure (150 tests, all race-safe)
go test ./internal/ai/... -race -count=1

# Consensus (HotStuff-2 7-node chaos tests)
go test ./internal/consensus/hotstuff/ -run TestChaos -v

# Mobile SDK (V1/V2 wire format, code cache)
go test ./cmd/evmsdk/ -v

# Distributed infrastructure (325 tests)
go test ./internal/distributed/... -race -count=1
```

## License

N42 is dual-licensed:

- **Core blockchain (node, consensus, EVM, P2P)**: [GNU General Public License v3.0](https://www.gnu.org/licenses/gpl-3.0.en.html) — ensures all derivative node implementations remain open source
- **Libraries, SDK, and tools (`lib/`, `cmd/evmsdk/`, `modules/rpc/`)**: [MIT License](https://opensource.org/licenses/MIT) — allows integration into proprietary mobile apps and third-party tooling

See [LICENSE](./LICENSE) and [LICENSE-MIT](./LICENSE-MIT) for details.

## Contributing

See [docs/developers/contribute.md](./docs/developers/contribute.md) for contribution guidelines and [docs/developers/codeofconduct.md](./docs/developers/codeofconduct.md) for our code of conduct.
