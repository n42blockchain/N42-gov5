# N42 Public Blockchain — Architecture & Feature Plan

> Version: 2026-04-03
> Module: `github.com/n42blockchain/N42` | Go 1.25+ | MDBX Backend
> Positioning: High-performance EVM-compatible L1 with post-quantum security, AI-native infrastructure, and full distributed stack

---

## Table of Contents

- [1. Architecture Overview](#1-architecture-overview)
- [2. Node Lifecycle](#2-node-lifecycle)
- [3. Distributed Ledger (State & Storage)](#3-distributed-ledger)
- [4. Distributed Storage](#4-distributed-storage)
- [5. Distributed Compute](#5-distributed-compute)
- [6. Distributed Communication](#6-distributed-communication)
- [7. Consensus & Block Production](#7-consensus--block-production)
- [8. Execution Layer & EVM](#8-execution-layer--evm)
- [9. Sync Modes](#9-sync-modes)
- [10. Cross-Chain Bridge](#10-cross-chain-bridge)
- [11. Post-Quantum Security](#11-post-quantum-security)
- [12. ENS & Identity](#12-ens--identity)
- [13. AI-Native Infrastructure](#13-ai-native-infrastructure)
- [14. RPC & Services](#14-rpc--services)
- [15. CLI Commands & Flags](#15-cli-commands--flags)
- [16. Default Ports](#16-default-ports)
- [17. Module Dependency Map](#17-module-dependency-map)

---

## 1. Architecture Overview

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                              cmd/n42 (CLI)                                  │
├─────────────────────────────────────────────────────────────────────────────┤
│                      internal/node (Orchestrator)                           │
│   Creates & wires all subsystems → manages lifecycle (Start / Stop)         │
├────────┬────────┬────────┬────────┬────────┬────────┬────────┬─────────────┤
│Consens.│ Miner  │ TxPool │  Sync  │  RPC   │  P2P   │  ExEx  │  AI Runtime │
│ APoA   │ Block  │ Blob   │ Staged │ HTTP   │libp2p  │Manager │  Wallet     │
│ APoS   │ Build  │ Bundle │ Snap   │ WS     │GossipSub│ Hook  │  Coord      │
│HotStuff│ MEV    │ 4337   │ Chkpt  │ IPC    │Kademlia│ AI Idx │  Governance │
│  BFT   │ Boost  │ Crypto │ Backfl │ gRPC   │ Rotor  │ Log    │  Attestation│
├────────┴────────┴────────┴────────┴────────┴────────┴────────┴─────────────┤
│                          internal/ (Core Logic)                             │
│  vm/        state_processor   parallel/   deferred/   mev/   zkprover/     │
│  avm/       state_proof       prefetcher  bridge/     mcp/   zkverifier/   │
│  tracers/   api/              bundler/    peerdas/    metrics/ exex/        │
├─────────────────────────────────────────────────────────────────────────────┤
│                   internal/distributed/ (Distributed Stack)                 │
│  coprocessor/  messaging/  storage/  compute/  notify/                      │
│  (tiered ZK)   (E2E enc)   (IPFS+BT) (WASM+ML) (push)                     │
├─────────────────────────────────────────────────────────────────────────────┤
│                       modules/ (Data Layer)                                 │
│  state/           rawdb/          rpc/          ethdb/                      │
│  IntraBlockState  MDBX accessors  HTTP/WS/IPC   DB interface               │
│  snapshot/        freezer/        transport      layered/                   │
│  commitment/      log_index                                                 │
├─────────────────────────────────────────────────────────────────────────────┤
│                         lib/ (Shared Libraries)                             │
│  kv/mdbx      jmt/       bmt/       verkle/     state/                     │
│  kv/memdb     jmt/store  bmt/store  verkle/store (HistoryV3)               │
│  kv/remotedb  (Blake3)   (Blake3)   (IPA)       aggregator                 │
├─────────────────────────────────────────────────────────────────────────────┤
│                    common/ & params/ (Types & Config)                       │
│  types/  block/  transaction/  crypto/ (bls/stark/dilithium/falcon)         │
│  params/version  params/chainspecs  conf/ (all subsystem configs)           │
└─────────────────────────────────────────────────────────────────────────────┘
```

### Dual-Mode Architecture

N42 supports two execution profiles via `--profile`:

| Mode | Flag | Consensus | State Tree | Hash | Block Header |
|------|------|-----------|------------|------|-------------|
| **N42 Mode** | `--chain mainnet` | HotStuff-2 BFT | JMT/BMT Blake3 | Blake3 | Extended (4 roots + QC + BLS) |
| **ETH EL Mode** | `--ethdev` | APoA/APoS (Beacon) | MPT Keccak256 | Keccak256 | Standard ETH |

Shared infrastructure: MDBX, EVM, P2P, TxPool, RPC, Freezer, Sync.

---

## 2. Node Lifecycle

### Startup Sequence (`internal/node/node.go`)

```
1.  Database (MDBX)           ─ Open ChainDB, open/create tables
2.  Genesis                   ─ Init or validate genesis block
3.  Blockchain Service        ─ Chain reader, consensus engine
4.  ExEx Manager              ─ Execution extension hooks
5.  Miner                     ─ Block production (if --mine)
6.  HotStuff Service          ─ BFT consensus (if enabled)
7.  JSON-RPC Registration     ─ eth/debug/trace/engine/ots/admin APIs
8.  RPC Servers               ─ HTTP / WS / IPC / Auth-RPC
9.  P2P Networking            ─ libp2p + Kademlia DHT + GossipSub
10. Sync Pipeline             ─ Checkpoint → Snap → Staged → Full
11. Snapshot Acceleration     ─ DiffLayer tree + DiskLayer + MDBX
12. Pruner                    ─ History pruning (if enabled)
13. History Expirer           ─ EIP-4444 (if enabled)
14. PeerDAS                   ─ Data availability sampling
15. Freezer                   ─ Cold storage background engine
16. OtterSync                 ─ BitTorrent segment sync (if enabled)
17. MCP Server                ─ AI agent protocol (port 8553)
18. gRPC KV Server            ─ Remote DB for RPCDaemon (port 9090)
19. Deferred Execution        ─ Async pipeline (if enabled)
20. AI Runtime                ─ Governance + Training + Attestation + Wallet
21. Distributed Services      ─ Coprocessor + Messaging + Storage + Notify
22. Bridge Runtime            ─ ZK cross-chain bridge (if enabled)
23. Metrics                   ─ Prometheus exporter
```

### Shutdown Sequence (reverse order)

All services implement `Stop()` or `Close()`. Node catches SIGINT/SIGTERM, drains in reverse startup order with 30s timeout per service.

---

## 3. Distributed Ledger

### 3.1 State Commitment Trees

N42 supports 4 state commitment schemes. Benchmark data from 11.9M mainnet blocks:

| Tree | Fan-out | Hash | Full History (11.7M) | Throughput | Proof Size | Use Case |
|------|---------|------|---------------------|------------|------------|----------|
| **JMT(16)** | 16 | Blake3 | ~130 GB (est.) | 673 blk/s @4.5M | 2,258 B | N42 default, PQ-safe |
| **MPT(16)** | 16 | Keccak256 | 127.5 GB | 6,230 blk/s | 1,660 B | ETH EL compatibility |
| **BMT(2)** | 2 | Blake3 | ~80 GB (est.) | 249 blk/s @5M | 752 B | Small proof, light client |
| **Verkle(256)** | 256 | IPA/Banderwagon | **1.19 GB** | 5,640 blk/s | 577 B | Storage-optimal, not PQ |

### 3.2 Storage Architecture

```
Hot Path (MDBX):
  Account          ─ address → account (flat, O(1))
  Storage          ─ address+slot → value (flat, O(1))
  Code             ─ codeHash → bytecode
  JMTNode          ─ blake3(node) → data (content-addressed)
  BMTNode          ─ blake3(node) → data
  VerkleNode       ─ commitment(64B) → serialized node
  SnapshotAccount  ─ address → snapshot data
  SnapshotStorage  ─ address+slot → snapshot data

Warm Path (Freezer / Ancient DB):
  Headers          ─ block# → RLP header (.cidx/.cdat)
  Bodies           ─ block# → RLP body
  Receipts         ─ block# → RLP receipts
  Hashes           ─ block# → hash (fixed-size .ridx/.rdat)
  Difficulty       ─ block# → TD
  Senders          ─ block# → recovered senders
  AccountChanges   ─ block# → changeset
  StorageChanges   ─ block# → changeset

Cold Path (EraE Archives / BitTorrent):
  Monthly segment files (.era.e)
  Random-access indexed
  BitTorrent distribution via OtterSync
```

### 3.3 Snapshot Acceleration Layer

```
modules/state/snapshot/
  DiffLayer tree    ─ In-memory diffs per block (branching for reorgs)
  DiskLayer         ─ MDBX-persisted base state
  ShardedCache      ─ Concurrent read cache
  Journal           ─ Crash recovery (diff replay)
  Generator         ─ Background snapshot builder (crash-resume marker)
  4 MDBX tables     ─ SnapshotAccount / SnapshotStorage / SnapshotMeta / SnapshotJournal
```

### 3.4 State Pruning & GC

| Mechanism | Location | Description |
|-----------|----------|-------------|
| JMT Ref-Count GC | `lib/jmt/gc.go` | Online pruning, 96% dead node recovery |
| History Expiry | `internal/node/history_expiry.go` | EIP-4444, configurable retention |
| Snapshot Pruning | `internal/node/pruner.go` | Snapshot-aware boundary |
| Freezer Compaction | `modules/rawdb/freezer/` | Ancient data compaction |

---

## 4. Distributed Storage

### 4.1 Multi-Protocol Storage (`internal/distributed/storage/`)

| Protocol | Module | Description |
|----------|--------|-------------|
| **CAS (Content-Addressable)** | `storage/` | Blake3-keyed store, universal resolver |
| **IPFS Bridge** | `storage/ipfs/` | Pin/Unpin/Get/Stat via HTTP API, CID↔CAS |
| **BitTorrent Bridge** | `storage/torrent/` | anacrolix/torrent, CAS↔infohash, magnet links |
| **eDonkey2000** | `storage/ed2k/` | MD4 hash, ed2k link parse/format |

### 4.2 EraE Archive Format

```
cmd/n42 era export --datadir <path> --output <dir>   # Export monthly .era.e segments
cmd/n42 era import --datadir <path> --input <dir>    # Import from .era.e segments
```

- Random-access indexed (RecSplit + Elias-Fano)
- BitTorrent distributable via OtterSync manifest

### 4.3 Freezer (Cold Storage)

```yaml
# conf.yaml
ancient_db: true               # Enable freezer (default: false)
freezer:
  data_dir: "/data/ancient"    # Freezer directory
```

7 frozen tables: Headers, Bodies, Receipts, Hashes, Difficulty, Senders, AccountChanges, StorageChanges.

---

## 5. Distributed Compute

### 5.1 Coprocessor (`internal/distributed/coprocessor/`)

Tiered off-chain verification (Brevis coChain pattern):

```
Task Submission → TieredVerifier routes by VerificationTier:
  ├─ ZK Tier (default)    ─ STARK/SNARK/SP1 proof generation + verification
  ├─ Optimistic Tier      ─ Bond + challenge window + fraud proof disputes
  └─ TEE Tier             ─ Hardware attestation verification
```

| Component | File | Description |
|-----------|------|-------------|
| TieredVerifier | `verification.go` | Route tasks by tier, verify results |
| ChallengeManager | `challenge.go` | Fraud proof disputes for optimistic tier |
| ProviderRegistry | `provider.go` | Stake, capabilities, reputation tracking |
| Marketplace | `marketplace.go` | Reverse-auction bidding (price/ETA/reputation) |
| Slashing | `slashing.go` | Verify-or-slash economic penalties |

### 5.2 WASM Engine (`internal/distributed/compute/wasm/`)

- Wazero-compatible sandboxed execution
- Fuel-based gas metering
- Host functions: CAS load/store, keccak256, logging
- Compilation cache

### 5.3 Batch Compute (`internal/distributed/compute/batch/`)

MapReduce over CAS data: job splitting → parallel map → ordered reduce with panic recovery.

### 5.4 AI Inference Engine (`internal/distributed/compute/inference/`)

- ORA opML: Model registry + optimistic ML verification + fraud proof challenges
- ResultCache: LRU + TTL, precompile access (`0x0301`)
- WASM executor: Fuel metering, model cache

---

## 6. Distributed Communication

### 6.1 Messaging Platform (`internal/distributed/messaging/`)

6-layer decentralized communications stack:

```
Layer 6: Stream API & DID Identity
  └─ SSE server (port 8554) + W3C DID v1.1 (did:n42:<address>)
Layer 5: MLS Group Encryption
  └─ RFC 9420 inspired, O(log n) ratchet tree, XChaCha20-Poly1305
Layer 4: Persistent Storage
  └─ CAS-backed, topic+timestamp indexing, cursor pagination, inter-node sync
Layer 3: RLN Anti-Spam
  └─ Poseidon Merkle tree, Shamir secret recovery, GossipSub validator
Layer 2: E2E Encryption
  └─ X25519 ECDH + XChaCha20-Poly1305, session ratcheting, forward secrecy
Layer 1: P2P Message Relay
  └─ GossipSub bridge, 8-shard topics, ring-buffer dedup (65536 entries)
```

### 6.2 Push Notifications (`internal/distributed/notify/`)

Contract events → wallet push streams. Channel subscription, filter matching, per-address history.

### 6.3 P2P Network (`internal/p2p/`)

| Feature | Implementation |
|---------|---------------|
| Stack | libp2p + Kademlia DHT |
| Block Gossip | GossipSub with scoring |
| TX Announce | eth/68 equivalent |
| Blob Sidecar | Independent gossip topic + RPC |
| Witness | `/n42/witness/1.0.0` stream protocol |
| Message Relay | 8-shard GossipSub (`/n42/msg/shard/0..7`) |
| Store Query | `/n42/msg/store_query/1.0.0` |
| Rotor Relay | SHA256 deterministic relay selection, direct stream + gossip fallback |

---

## 7. Consensus & Block Production

### 7.1 Consensus Engines

| Engine | Module | Type | Finality |
|--------|--------|------|----------|
| **HotStuff-2 BFT** | `internal/consensus/hotstuff/` | 2-round BFT + BLS12-381 | Instant (single slot) |
| **APoS** | `internal/consensus/apos/` | Proof of Stake + STARK | ~1 epoch |
| **APoA** | `internal/consensus/apoa/` | Proof of Authority | Instant |

### 7.2 HotStuff-2 Details

- **2-round protocol**: Prepare + Commit (pipeline overlapping)
- **BLS12-381 aggregate signatures**: QuorumCertificate with signer bitmap
- **Adaptive Pacemaker**: Jolteon-style exponential backoff + p95 latency
- **Dynamic reconfiguration**: commit-then-activate (quorum overlap safety)
- **MDBX persistence**: Crash recovery via diff layer journal
- **Mobile BLS**: Phone validators contribute aggregate signatures

### 7.3 MEV & Block Building

| Component | Module | Description |
|-----------|--------|-------------|
| MEV-Boost Relay | `internal/mev/` | Multi-relay concurrent bidding |
| AI Optimizer | `mev/ai_optimizer.go` | Score by effective tip x gas efficiency |
| Fairness Guard | `mev/ai_optimizer.go` | Sandwich attack detection |
| Gas Predictor | `mev/gas_predictor.go` | EWMA-based (alpha=0.3, window=32) |
| Bundle Pool | `internal/miner/` | BundlePool + expiry eviction |
| Encrypted Mempool | `internal/txspool/` | Threshold encryption AES-256-GCM |

### 7.4 Deferred Execution (`internal/deferred/`)

Consensus-execution separation (Monad/Aptos pattern):

```
Queue Stage  →  Execute Stage  →  Commit Stage
(order txs)     (async workers)    (persist state)

stateRoot(N) is included in block N+1
```

Configurable via `conf.DeferredCfg.Enabled`.

---

## 8. Execution Layer & EVM

### 8.1 EIP Support

**Cancun**: TLOAD/TSTORE (1153), Blobs (4844), MCOPY (5656), BLOBBASEFEE (7516), PUSH0 (3855)
**Pectra**: 7702 (delegation), 2537 (BLS 9 precompiles), 7212 (P-256), 6110 (deposits), 7251 (MaxEB), 7002 (withdrawals), 7623 (calldata cost), 2935 (historical hashes), 7685 (execution requests)
**Fusaka**: PeerDAS (7594), BPO 1-5, 7825 (tx gas limit)
**Glamsterdam**: EOF (3540/3670/4200/4750/5450), EIP-7904 (transfer gas 4500)

### 8.2 Parallel Execution

**Block-STM** (`internal/parallel/`): Wave executor + MVS + DAG access list.

Benchmarks (32-core): 374 Ggas/s, 661K TPS, 153ns/call. Independent TX: 3.9x speedup.

### 8.3 State Prefetch

`internal/prefetcher.go`: ShardedCache preloads sender/recipient/access-list accounts before execution.

### 8.4 Precompiled Contracts

| Address | Contract | Notes |
|---------|----------|-------|
| 0x01-0x0a | Standard ETH | ecrecover, sha256, ripemd, identity, modexp, bn256, blake2f |
| 0x0b | BLS G1Add | Pectra EIP-2537 (9 BLS precompiles) |
| 0x14-0x17 | PQ Precompiles | Dilithium/Falcon/STARK verify, isolated via PQPrecompilesTime |
| 0x0100 | P-256 Verify | EIP-7212 secp256r1 |
| 0x0301 | AI Inference | Request/result/model (EIP-configurable) |

### 8.5 ERC-4337 Account Abstraction

`internal/bundler/`: UserOp mempool, validator, bundle builder, 4 RPC endpoints. Agent session key validation for AI wallets.

---

## 9. Sync Modes

### 9.1 Available Modes

| Mode | Description | Use Case |
|------|-------------|----------|
| **Full Sync** | Execute all blocks from genesis | Archive nodes |
| **Snap Sync** | Download state snapshot + backfill history | Default for new nodes |
| **Checkpoint Sync** | Start from trusted hash, skip historical verification | Fast node join |
| **Backfill Sync** | Background history fill (checkpoint → genesis) | Post-checkpoint history |
| **Staged Sync** | 7-stage pipeline (Headers→Bodies→Senders→Execution→Hashing→Trie→Finish) | Production sync |

### 9.2 BitTorrent Sync (OtterSync)

```bash
n42 --torrent-sync.enabled --torrent-sync.dir /data/torrents
```

Downloads immutable EraE segment files via BitTorrent. Manifest-based integrity verification.

### 9.3 Freezer Download Flow

```
New Node Start:
  1. Checkpoint Sync (trusted hash)     ─ Minutes
  2. Snap Sync (state trie download)     ─ Hours
  3. OtterSync (frozen segments via BT)  ─ Background, hours
  4. Backfill (fill gaps genesis→chkpt)  ─ Background, hours
  5. Live Sync (follow chain head)       ─ Continuous
```

---

## 10. Cross-Chain Bridge

### 10.1 ZK-Native Bridge (`internal/bridge/`)

Trust chain: HotStuff-2 BLS → SP1 ZK Proof → JMT State Proof → ETH on-chain verification.

| Component | File | Description |
|-----------|------|-------------|
| HeaderProver | `header_prover.go` | BLS consensus → SP1 ZK proof |
| StateProver | `state_prover.go` | JMT Merkle inclusion proof |
| Relayer | `relayer.go` | Event monitoring → evidence chain → submit |
| Router | `router.go` | Cross-chain message lifecycle |
| N42Verifier.sol | `contracts/bridge/` | ETH on-chain ZK verification |
| N42Bridge.sol | `contracts/bridge/` | Asset lock/release |

**Status**: Phase 1 complete (N42→ETH). Phase 2 (ETH→N42) planned.

### 10.2 EIP-3668 CCIP-Read

Supported for off-chain data resolution.

---

## 11. Post-Quantum Security

### 11.1 PQ Cryptography Stack

| Component | Algorithm | Security Level | Status |
|-----------|-----------|---------------|--------|
| State Root | JMT Blake3-256 | 128-bit post-quantum (Grover) | Production |
| Consensus Proof | PQ-STARK | Hash-based, no curve dependency | Production |
| Signature (future) | Dilithium-3 | NIST PQC Level 3 | Precompile (0x14) |
| Signature (future) | Falcon-1024 | NIST PQC Level 5 | Precompile (0x15) |
| STARK Verify | Poseidon/Blake3 | Hash-based | Precompile (0x16-0x17) |

### 11.2 PQ Precompile Isolation

Activated via `ChainConfig.PQPrecompilesTime` — standard fork maps are PQ-free, EEST tests unaffected.

### 11.3 Industry Context

- Ethereum Foundation PQ Team established 2026-01 ($2M research grants)
- Vitalik marks 2028 as quantum computing critical window
- N42 is the **only mainstream client with integrated PQ cryptography**

---

## 12. ENS & Identity

### 12.1 DID Identity (`internal/distributed/messaging/identity/`)

- **W3C DID v1.1**: `did:n42:<address>` method
- Create from wallet key, sign/verify against document verification methods
- LRU-cached resolver (1024 entries, 1h TTL)
- Register/Resolve/Update/Deactivate lifecycle

### 12.2 EVM-Level ENS

N42 is fully EVM-compatible — standard ENS contracts deploy and operate natively. `eth_call` + `eth_getStorageAt` support all ENS resolution patterns.

---

## 13. AI-Native Infrastructure

### 13.1 Agent Wallet (`internal/ai/wallet/`)

| Component | Description |
|-----------|-------------|
| Account | Deterministic address from owner key + DID via Keccak256 |
| SessionKey | Time-limited, contract allowlists, method selectors, spend/gas caps (max 16/account) |
| SpendingPolicy | Rate (sliding window), Cap (per-tx + daily), Allowlist, Composite (AND/OR) |
| Paymaster | Deposit pool per owner, gas sponsorship with tagged data |

### 13.2 Agent Coordination (`internal/ai/coord/`)

| Component | Description |
|-----------|-------------|
| AgentRegistry | Register with capabilities + stake, discover by capability (sorted by reputation) |
| TaskNegotiation | Request → bid → accept → complete/dispute, escrow on acceptance |
| ReputationSystem | Weighted: completion 40%, disputes 30%, response time 20%, stake 10% |

### 13.3 AI Safety Stack

| Layer | Module | Description |
|-------|--------|-------------|
| Data Governance | `ai/governance/` | Dataset registry, ethics committee voting (quorum/threshold) |
| Training Verification | `ai/training/` | ZK proofs of training from approved datasets |
| Inference Attestation | `ai/attestation/` | Signed attestations, chain-of-custody, safety levels |
| ZKML | `zkprover/zkml.go` | Circuit generation, inference proof (96B public inputs) |

### 13.4 AI Block Building (`internal/mev/`)

- AIBlockOptimizer: Score transactions by effective tip x gas efficiency
- FairnessGuard: Sandwich attack pattern detection
- GasPredictor: EWMA-based (alpha=0.3, window=32 blocks)

### 13.5 MCP Server (`internal/mcp/`)

Model Context Protocol server (port 8553): 8 blockchain data tools + 4 resources for AI agent queries.

### 13.6 AI Inference Precompile

Address `0x0301`: requestInference, getResult, getModel, listModels. Gas: 10000 base + 100/byte.

---

## 14. RPC & Services

### 14.1 API Namespaces

| Namespace | Transport | Description |
|-----------|-----------|-------------|
| `eth_*` | HTTP/WS/IPC | Standard Ethereum (complete) |
| `debug_*` | HTTP/WS/IPC | Tracing, state dump, profiling |
| `trace_*` | HTTP/WS/IPC | Parity-compatible tracing |
| `engine_*` | Auth-RPC (JWT) | Engine API v1-v4 |
| `txpool_*` | HTTP/WS/IPC | Transaction pool inspection |
| `admin_*` | HTTP/WS/IPC | Node admin, peer management |
| `net_*` | HTTP/WS/IPC | Network info |
| `web3_*` | HTTP/WS/IPC | Client version, SHA3 |
| `ots_*` | HTTP/WS/IPC | Otterscan (10 methods) |
| `eth_subscribe` | WS | Real-time subscriptions |
| `graphql` | HTTP | EIP-1767 GraphQL API |

### 14.2 Key RPC Methods

| Method | Notes |
|--------|-------|
| `eth_getProof` | JMT-based Merkle proof (account + storage) |
| `eth_simulateV1` | Multi-block simulation |
| `eth_createAccessList` | Iterative AccessListTracer |
| `eth_getBlockReceipts` | Full block receipts |
| `eth_getStorageValues` | EIP-7834 batch storage |
| `debug_traceCall` | EVM trace with state overrides |
| `debug_traceTransaction` | Transaction execution trace |
| `ots_searchTransactions*` | Address transaction history |
| `engine_newPayloadV4` | Pectra+ payload validation |
| `engine_getBlobsV1` | Blob retrieval |

### 14.3 RPCDaemon (Independent Deployment)

```bash
# Core node (serves gRPC KV)
n42 --grpc.addr 0.0.0.0 --grpc.port 9090

# RPCDaemon (separate process, scales horizontally)
rpcdaemon --private.api.addr <core-node>:9090 --http.port 8545
```

### 14.4 Specialized Services

| Service | Port | Description |
|---------|------|-------------|
| MCP Server | 8553 | AI agent data queries |
| SSE Stream | 8554 | Real-time message streaming |
| gRPC KV | 9090 | Remote DB for RPCDaemon |
| Clef Signer | IPC | External signer + rules + audit log |

---

## 15. CLI Commands & Flags

### 15.1 Commands

```bash
n42                          # Start node (default)
n42 init                     # Initialize data directory
n42 account list|new|update|import  # Account management
n42 wallet import            # Wallet import
n42 db stats|list|get|inspect       # Database inspection
n42 export txs|balance|dbState|dbCopy  # Data export
n42 state                    # State inspection
n42 era export|import        # EraE archive management
n42 bench-state              # Benchmark state trees (--only jmt|mpt|bmt|verkle)
n42 migrate-jmt              # Migrate to JMT state commitment
n42 migrate-tiers            # Migrate storage tiers
n42 rebuild-mpt              # Rebuild MPT trie
n42 rebuild-trie             # Rebuild trie
n42 replay                   # Block replay
n42 replay-v2                # Block replay v2 (with journal + witness)
n42 jmt-compact              # Compact JMT database
n42 fix-genesis              # Fix genesis configuration
```

### 15.2 Quick Start Examples

```bash
# N42 mainnet node with full features
n42 --chain mainnet --data.dir /data/n42 \
    --mine --engine.etherbase 0x... \
    --http --http.api eth,web3,net,debug,txpool,trace \
    --ws --authrpc --authrpc.jwtsecret /path/jwt.hex \
    --metrics --pprof

# ETH EL development node
n42 --ethdev --data.dir /data/ethdev \
    --http --http.api eth,debug,trace \
    --ws

# Archive node with RPCDaemon
n42 --chain mainnet --data.dir /data/archive \
    --grpc.addr 0.0.0.0 --grpc.port 9090

# Light sync (checkpoint + snap)
n42 --chain mainnet --syncmode fast \
    --torrent-sync.enabled
```

### 15.3 Key Flags Reference

**Network & Chain**
```
--chain <name>              Chain: mainnet|testnet|private|eth-mainnet (default: mainnet)
--profile <mode>            Execution profile: n42|eth (default: n42)
--data.dir <path>           Data directory (default: ./n42data)
--config, -c <file>         Config file (YAML)
```

**P2P**
```
--p2p.tcp-port <int>        TCP port (default: 61016)
--p2p.udp-port <int>        UDP port (default: 61015)
--p2p.max-peers <int>       Max peers (default: 5)
--p2p.bootstrap-node <addr> Bootstrap nodes (repeatable)
--p2p.no-discovery          Disable peer discovery
```

**RPC**
```
--http                      Enable HTTP RPC
--http.addr <ip>            Listen address (default: 127.0.0.1)
--http.port <port>          HTTP port (default: 8545)
--http.api <modules>        API modules (default: eth,web3,net)
--ws                        Enable WebSocket
--ws.port <port>            WS port (default: 8546)
--authrpc                   Enable Engine API (JWT auth)
--authrpc.port <port>       Auth port (default: 8551)
--authrpc.jwtsecret <file>  JWT secret file
```

**Mining & Consensus**
```
--mine                      Enable block production
--engine.etherbase <addr>   Mining reward address
```

**Sync**
```
--syncmode <mode>           full|fast|light (default: full)
--torrent-sync.enabled      Enable OtterSync BitTorrent
```

**Observability**
```
--metrics                   Enable Prometheus (port 6061)
--pprof                     Enable pprof (port 6060)
--log.level <level>         trace|debug|info|warn|error (default: info)
--log.json                  JSON structured logs (default: true)
```

---

## 16. Default Ports

| Port | Protocol | Service |
|------|----------|---------|
| 61015 | UDP | P2P Discovery (Kademlia DHT) |
| 61016 | TCP | P2P Communication (libp2p) |
| 8545 | TCP | JSON-RPC HTTP |
| 8546 | TCP | JSON-RPC WebSocket |
| 8551 | TCP | Auth-RPC (Engine API, JWT) |
| 6060 | TCP | pprof Profiling |
| 6061 | TCP | Prometheus Metrics |
| 8553 | TCP | MCP Server (AI Agents) |
| 8554 | TCP | Message Stream (SSE) |
| 9090 | TCP | gRPC KV (RPCDaemon) |

---

## 17. Module Dependency Map

```
                    ┌──────────┐
                    │ cmd/n42  │
                    └────┬─────┘
                         │
                ┌────────┴────────┐
                │ internal/node   │
                └────────┬────────┘
         ┌───────┬───────┼───────┬───────┬───────┐
         ▼       ▼       ▼       ▼       ▼       ▼
     consensus  miner   sync    api    p2p    distributed/
     ┌───┐     ┌───┐   ┌───┐  ┌───┐  ┌───┐   ┌──────────┐
     │HS2│     │MEV│   │Stg│  │RPC│  │DHT│   │coprocessor│
     │APoS│    │Bld│   │Snp│  │WS │  │GSub│  │messaging  │
     │APoA│    │Bnd│   │Ckp│  │IPC│  │Rot│   │storage    │
     └─┬─┘    └─┬─┘   └─┬─┘  └─┬─┘  └─┬─┘   │compute   │
       │        │        │      │      │       │notify    │
       └────────┴────────┴──────┴──────┘       └──────────┘
                         │
              ┌──────────┴──────────┐
              │  modules/state      │
              │  IntraBlockState    │
              │  snapshot/          │
              │  commitment/ (JMT/BMT/MPT)
              └──────────┬──────────┘
                         │
              ┌──────────┴──────────┐
              │  lib/               │
              │  kv/mdbx  jmt/ bmt/ │
              │  verkle/  state/    │
              └──────────┬──────────┘
                         │
              ┌──────────┴──────────┐
              │  common/  params/   │
              │  types/ crypto/     │
              │  transaction/       │
              └─────────────────────┘
```

### Key Design Principles

1. **Node is the orchestrator** — creates, wires, and manages all subsystem lifecycles
2. **Consensus is pluggable** — APoA, APoS, HotStuff-2 implement `consensus.Engine` interface
3. **State commitment is pluggable** — JMT, BMT, MPT, Verkle via `commitment/` abstraction
4. **Distributed services are decoupled** — each can be enabled/disabled independently
5. **Dual-mode execution** — N42 extended or ETH EL standard, selected at startup
6. **Content-addressed storage** — JMT/BMT/Verkle nodes keyed by hash, natural dedup and history
7. **ExEx hooks** — post-execution extensions for indexers, bridges, AI without core changes

---

*Generated from N42-gov5 source code audit. See `docs/GAP_ANALYSIS.md` for competitive analysis.*
