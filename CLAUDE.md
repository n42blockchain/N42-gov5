# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

N42 is a high-performance Ethereum-compatible blockchain node implementation in Go. Module: `github.com/n42blockchain/N42`, Go 1.25.0, using MDBX as the key-value database backend.

## Build & Development Commands

```bash
# Build
make n42              # Build main binary (with deps + version bump) → build/bin/n42
make build            # Compile all packages (no go mod tidy)
make clean            # Clean build artifacts

# Test
make test             # Run all tests
make test-short       # Fast tests with -short flag
make test-verbose     # Verbose test output
go test ./internal/vm/...  # Run tests for a single package

# Messaging subsystem tests (92 tests, all race-safe)
go test ./internal/distributed/messaging/...            # All messaging packages
go test -race ./internal/distributed/messaging/...      # With race detector
go test ./internal/distributed/messaging/ -run "TestRelay|TestProtocol"  # P2P relay
go test ./internal/distributed/messaging/crypto/ -v     # E2E encryption
go test ./internal/distributed/messaging/rln/ -v        # RLN anti-spam
go test ./internal/distributed/messaging/store/ -v      # Persistent storage
go test ./internal/distributed/messaging/group/ -v      # MLS group encryption
go test ./internal/distributed/messaging/stream/ -v     # SSE streaming
go test ./internal/distributed/messaging/identity/ -v   # DID identity

# AI infrastructure tests (150 tests across 10 packages, all race-safe)
go test ./internal/ai/wallet/ ./internal/ai/coord/ -v    # Agent wallet + discovery (34 tests)
go test ./internal/vm/ -run "TestAIInference" -v        # AI inference precompile (6 tests)
go test ./internal/distributed/compute/inference/ -v    # Inference cache + executor (26 tests)
go test ./internal/exex/extensions/ -v                  # AI data indexer (6 tests)
go test ./internal/zkprover/ -run "TestZKML|TestExecution" -v  # ZKML prover + trace (9 tests)
go test ./internal/zkverifier/ -run "TestZKML" -v       # ZKML verifier (5 tests)
go test ./internal/mev/ -run "TestAI|TestGas" -v        # AI block optimizer (13 tests)
go test ./internal/ai/governance/ -v                      # Data governance (19 tests)
go test ./internal/ai/training/ -v                        # ZK training verification (17 tests)
go test ./internal/ai/attestation/ -v                     # Inference attestation (15 tests)
go test -race ./internal/ai/wallet/ ./internal/ai/coord/ ./internal/vm/ ./internal/distributed/compute/inference/ ./internal/exex/extensions/ ./internal/zkprover/ ./internal/zkverifier/ ./internal/mev/ ./internal/ai/... -count=1  # Race check

# Code Quality
make lint             # golangci-lint (install: go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest)
make check            # fmt + vet + lint combined
make fmt              # gofmt
make vet              # go vet

# Race Detection
make race-core        # Race check on core packages (vm, state, sync)
make race             # Full race detection (slow, 30m timeout)

# Coverage & Benchmarks
make test-cover       # HTML coverage report → build/coverage/coverage.html
make bench-smoke      # Quick benchmarks on core packages
make bench            # Full benchmarks

# CI
make ci               # build + test + vet
make ci-full          # build + test + vet + lint + race-core
```

**Build tags**: `nosqlite,noboltdb` (always applied via GO_FLAGS)

**CGO required**: MDBX database backend needs CGO enabled.

## Architecture

### Layer Structure

```
cmd/n42/          → Main entry point (urfave/cli v2), node lifecycle
internal/         → Core business logic (private packages)
  node/           → Node orchestration: creates and wires consensus, miner, txpool, RPC, P2P
  consensus/      → Consensus engines: apoa/ (PoA), apos/ (PoS)
  miner/          → Block production and validation
  txspool/        → Transaction pool management
  vm/             → EVM execution
  avm/            → N42 AVM (alternative VM)
  p2p/            → libp2p-based networking with Kademlia DHT
  sync/           → Chain synchronization (initial-sync, etc.)
  api/            → JSON-RPC API backend implementation
  tracers/        → Debug/trace (js/, native/liveTracer for real-time EVM events)
  distributed/    → Distributed infrastructure (modular, decoupled)
    coprocessor/  → Distributed compute coprocessor (tiered verification, provider marketplace)
      verification.go → Tiered verifier: ZK (default), Optimistic (bond+challenge), TEE (attestation)
      challenge.go    → Challenge manager: fraud proof disputes for optimistic verification
      provider.go     → Provider registry: stake, capabilities, reputation tracking
      marketplace.go  → Reverse-auction marketplace: bid, select (price/ETA/reputation)
      slashing.go     → Verify-or-Slash: economic penalties for misbehavior
    compute/      → Distributed compute engines
      wasm/       → WASM execution engine (wazero-compatible, fuel-based gas, host functions)
      batch/      → MapReduce batch compute (job splitting, scheduling, aggregation)
      inference/  → AI inference with opML verification (optimistic ML + fraud proofs)
        cache.go      → Verified inference result cache (LRU + TTL, precompile access)
        executor_wasm.go → WASM-based inference executor (fuel metering, model cache)
    messaging/    → Decentralized messaging platform (P2P relay, E2E encryption, groups)
      protocol.go   → Envelope wire format (sign/verify/encode/decode, Keccak256 IDs)
      relay.go      → GossipSub bridge (8-shard topics, ring-buffer dedup cache)
      peer_handler.go → Store query protocol (/n42/msg/store_query/1.0.0)
      service.go    → Core service (local store, rate limiter, relay integration)
      crypto/       → E2E encryption
        keys.go       → X25519 key pairs, wallet-derived keys (HKDF-SHA256), KeyBundle, KeyStore
        envelope.go   → Ephemeral ECDH + XChaCha20-Poly1305 seal/open
        session.go    → Bilateral session with chain key ratcheting (forward secrecy)
      rln/          → Rate-Limiting Nullifier anti-spam (Waku RLN v2 pattern)
        membership.go → Poseidon Merkle tree, identity commitments, Shamir share proofs
        verifier.go   → Proof verification, Shamir secret recovery (spam → slash)
        validator.go  → GossipSub validator (accept/reject/ignore), epoch management
      store/        → Persistent CAS-backed message storage
        persistent.go → CAS persistence with topic+timestamp indexing
        query.go      → Structured queries with cursor-based pagination
        sync.go       → Inter-node sync protocol (availability advertisement, gap detection)
      group/        → MLS-inspired (RFC 9420) group encryption
        session.go    → Group session (create/add/remove/encrypt/decrypt/commit)
        tree.go       → Binary ratchet tree (O(log n) TreeKEM path updates)
        keypackage.go → MLS KeyPackage creation and validation
      stream/       → Real-time message streaming
        server.go     → SSE server (/ws/messages, /health), topic subscriptions
      identity/     → W3C DID v1.1 decentralized identity
        did.go        → did:n42:<address> method, create/sign/verify
        resolver.go   → DID resolver with LRU cache
    storage/      → Multi-protocol storage (IPFS bridge, CAS↔CID, universal resolver)
      torrent/    → BitTorrent bridge (anacrolix/torrent, CAS↔infohash, magnet, seeder)
      ed2k/       → eDonkey2000 (MD4 hash, ed2k link parse/format, hash bridge)
    notify/       → Push notifications (contract events → wallet streams)
  ai/              → AI infrastructure (modular, decoupled)
    wallet/         → AI Agent wallet
      account.go      → Agent wallet (session keys, spend limits, contract allowlists)
      policy.go       → Spending policies (rate, cap, allowlist, composite AND/OR)
      paymaster.go    → Gas sponsorship (deposit pool, operation sponsoring)
      service.go      → Agent service orchestrator
    coord/          → AI Agent coordination
      registry.go     → Agent discovery registry (capabilities, stake, reputation)
      negotiation.go  → Task negotiation protocol (request, bid, accept, complete, dispute)
      reputation.go   → Agent reputation system (completion rate, response time, decay)
    governance/     → Training data governance (dataset provenance, ethics committee voting)
      types.go        → Dataset, Vote, ReviewResult, DatasetStatus, EthicsCategory
      registry.go     → DatasetRegistry (register, link to model, owner index)
      committee.go    → Committee (quorum/threshold voting, secp256k1 sig verification)
    training/       → ZK training verification (model authenticity, anti-tampering)
      types.go        → TrainingRecord, TrainingTrace, EpochTrace, TrainingProof
      prover.go       → TrainingProver (governance-gated, hash-chain ZK proof)
      verifier.go     → TrainingVerifier (structural + public input validation)
    attestation/    → ZK inference attestation (signed results, chain-of-custody)
      types.go        → InferenceAttestation, SignedAttestation, AttestationChain, SafetyLevel
      service.go      → AttestationService (create/sign/verify/chain, TTL, prune)
  deferred/       → Deferred execution pipeline (consensus-execution separation)
  mev/            → MEV-Boost relay integration
    ai_optimizer.go → AI block building optimizer (scoring, MEV detection, fairness guard)
    gas_predictor.go → Gas price prediction (EWMA, sliding window)
  mcp/            → MCP Server (AI agent data queries)
    data_tools.go   → AI data index tools (token transfers, address profiles, gas analytics)
    agent_tools.go  → Agent discovery tools (find agents, task delegation, reputation)
    agent_wallet_tools.go → Agent wallet tools (create wallet, balance, submit tx)
  zkprover/       → ZK proving (STARK/SNARK/SP1 three backends)
    zkml.go         → ZKML prover (circuit generation, inference proof generation)
    zkml_trace.go   → ML execution trace capture (layer-by-layer intermediate values)
  zkverifier/     → ZK proof verification
    zkml_verifier.go → ZKML proof verification (public input validation)
  metrics/        → 250+ Prometheus metrics
  exex/           → Execution Extensions (ExEx) framework
    extensions/ai_indexer.go → AI data indexer (token transfers, events, address profiles, gas)
    extensions/schema.go     → Index schema types (TokenTransfer, ContractEvent, AddressProfile, GasMetrics)
  bundler/        → ERC-4337 account abstraction bundler (+ agent session key validation)
  peerdas/        → PeerDAS data availability sampling (EIP-7594)
  mptbuild/       → reth-format MPT builder (3-pass: scan → ETL sort → HashBuilder → AppendDup)
  mpttrie/        → MPT reader (Walk, sibling collection, BranchNodeCompact decode, unified-env Open)
  mptproof/       → eth_getProof generator (latest + state-as-of, EIP-1186 wire format)
    generator.go       → Generator entry; LatestAccountProof / LatestStorageProofs / LatestProof; SetLeafSource / UnifiedEnv
    source.go          → LeafSource interface + HashedKeyScanner; RethLeafSource (PlainState fallback) + MapLeafSource
    reth_hashed.go     → RethHashedLeafSource — reads reth HashedAccounts (29.7 GB) + HashedStorages DupSort (127.7 GB);
                         implements HashedKeyScanner for native cursor prefix scans; **production fast path**
    wire_full.go       → FullAccountProofBytes / FullStorageProofBytes (rebuilds inline siblings via SubtreeNodeBytes)
    wire_expand.go     → D.1.5 target-subtree expansion (extension/branch nodes between deepest branch and leaf)
    wire_verify.go     → VerifyStandardProof — independent EIP-1186 oracle
    verify_subtree.go  → Walk + subtree-rebuild self-verify; dispatch on HashedKeyScanner for cursor fast path
    historical.go      → HistoricalLeafSource overlay (historicalstate + base); HistoricalProof bundle
  historicalstate/ → state-as-of reader (combines snapshot + MPHF+fp history)
  history/         → MPHF+fp coldstore for per-block changes (used by historicalstate)
modules/          → Data layer
  state/          → State management (IntraBlockState, snapshot, witness)
    commitment/   → Pluggable state-root engines: MPT / JMT / BMT / Verkle / LtHash
  rawdb/          → Raw database operations (MDBX backend, freezer, log index)
  rpc/            → JSON-RPC transport (HTTP, WebSocket, IPC)
  ethdb/          → Database interface abstraction
lib/              → Shared libraries
  kv/             → Key-value store (mdbx/, memdb/, remotedb/, remotedbserver/, layered/)
  commitment/     → Erigon HexPatriciaHashed (HPH) port: Keccak / 16-ary grid, ETH-compatible stateRoot
                     + ConcurrentMPTRootComputer (per-worker RoTx, etl.Collector), Warmuper, recording context
  jmt/            → Jellyfish Merkle Tree (Blake3, sparse 16-ary, ref-counting GC, cold/hot layered store)
  bmt/            → Binary Merkle Tree (Blake3, 2-ary content-addressed, 65B internal node, smallest proof ~427B)
  verkle/         → Verkle tree (go-verkle, Bandersnatch IPA / Banderwagon, 256-ary, 64B commitment key)
  lthash/         → Lattice Hash (Blake3 XOF, 2048B homomorphic digest, O(changes) root update, treeless)
  state/          → HistoryV3 aggregator (per-block changeset + inverted index)
common/           → Shared types and utilities
  types/          → Address, Hash, core blockchain types
  block/          → Block/Header/Body interfaces
  transaction/    → Transaction types (Legacy, AccessList, DynamicFee, Blob, SetCode)
  crypto/         → Cryptographic functions (bls/, stark/, dilithium/, falcon/)
params/           → Chain parameters (config, blob_schedule, chainspecs/)
conf/             → Node configuration (all subsystem configs, ai_config)
accounts/         → Account management (keystore/, abi/, external/)
contracts/        → Smart contracts (deposit contract with tiered staking)
cmd/rpcdaemon/    → Standalone RPC daemon (gRPC remote KV)
cmd/clef/         → External signer (IPC + rules + audit log)
cmd/zkguest/      → ZK guest program (RISC-V64 target)
```

### Key Patterns

- **Node** (`internal/node/node.go`) is the central orchestrator — it creates DB, consensus engine, miner, txpool, P2P, RPC, MCP, ZK prover, deferred executor, gRPC KV server, distributed services, then manages lifecycle (Start/Stop).
- **Consensus is pluggable**: `apoa` (PoA), `apos` (PoS), and `hotstuff` (HotStuff-2 BFT) implement the `consensus.Engine` interface.
- **Database**: MDBX (memory-mapped B+ tree) via `lib/kv/mdbx/`; `lib/kv/memdb/` for testing; `lib/kv/remotedb/` for RPCDaemon.
- **State management**: `modules/state/` handles state trie with changeset tracking; `modules/state/commitment/` exposes a pluggable `RootComputer` interface backed by five engines (see `docs/bench_state_report.md` for 1M-block cross-tree benchmarks). **Production defaults**: the n42 native custom chain (`--chain private`) commits with **QMDB**; eth-el commits with the **Ethereum MPT**. **JMT is deprecated** for the custom chain and eth-el (code retained, no longer a default; named legacy chains keep their JMT preset pending migration).
  - **MPT (HPH)** — `lib/commitment/` Erigon HexPatriciaHashed port, Keccak / 16-ary, **ETH stateRoot byte-compatible** (production — eth-el). `ConcurrentMPTRootComputer` parallelizes via per-worker RoTx + `bulk_resume` checkpoint.
  - **QMDB** — twig-forest / binary, Blake3. **Production commitment for the n42 native chain** (live hotstuff block production); `--chain private` bootstraps on it.
  - **JMT** — `lib/jmt/` Blake3 / 16-ary sparse, ref-counting GC, highest write throughput (~3.06M blk/s @ 1M bench). **Deprecated as a default** (see above); code kept for legacy chains + cross-check.
  - **BMT** — `lib/bmt/` Blake3 / binary, content-addressed, **smallest proof** (~427B). Phase 1 validated against 11.7M EVM-replay blocks.
  - **Verkle** — `lib/verkle/` go-verkle (Bandersnatch IPA / Banderwagon), 256-ary, **smallest persistent state** (~4.8 MB full history) but ~40× slower writes than JMT — experimental, suited to verify-heavy / L2 use.
  - **LtHash** — `lib/lthash/` Blake3 XOF homomorphic digest, O(changes) update, no tree → no proof. Experimental, runs in parallel with JMT for cross-check.
- **P2P**: Built on `go-libp2p` with custom protocols for block/transaction/blob/witness propagation.
- **PQ isolation**: Post-quantum precompiles (0x14-0x17) are NOT in standard fork maps; activated only via `ChainConfig.PQPrecompilesTime`.
- **Distributed compute platform**: `internal/distributed/` provides a full distributed compute stack:
  - **Tiered verification** (Brevis coChain pattern): ZK proof (default) → Optimistic with bond+challenge window → TEE attestation. Tasks route through `TieredVerifier` based on `VerificationTier`.
  - **Provider network** (EigenLayer AVS + Akash model): providers register with stake+capabilities, claim tasks or bid in reverse-auction marketplace, get rewarded/slashed via Verify-or-Slash economic model.
  - **WASM engine**: sandboxed execution with fuel-based gas metering, host functions (CAS load/store, keccak256, logging), compilation cache. Runtime interface wraps wazero.
  - **Batch compute**: MapReduce over CAS data — job splits into map tasks, parallel execution, ordered reduce with panic recovery.
  - **AI inference** (ORA opML): model registry, optimistic ML verification with fraud proof challenges. ResultCache for precompile access. WASM executor for deterministic model execution.
  - **State machine enforcement**: `validTransition()` in task.go enforces legal status transitions; atomic `TransitionToProving`/`TransitionToChallenged` prevent TOCTOU races.

### AI Agent Infrastructure

`internal/ai/wallet/` and `internal/ai/coord/` provide a complete AI Agent platform with 3 subsystems:

**Agent Wallet** (`ai/wallet/account.go`, `policy.go`, `paymaster.go`, `service.go`):
- `Account`: deterministic address derivation from owner key + DID via Keccak256.
- `SessionKey`: time-limited keys with contract allowlists, method selectors, spend limits, gas caps. Max 16 per account.
- `SpendingPolicy` interface with `RatePolicy` (sliding window), `CapPolicy` (per-tx + daily), `AllowlistPolicy`, `CompositePolicy` (AND/OR).
- `PaymasterService`: deposit pool per owner, gas sponsorship with tagged paymaster data.
- Bundler integration: `AgentSessionValidator` interface in `bundler/validator.go` for session key validation in UserOps.

**Agent Discovery** (`ai/coord/registry.go`, `negotiation.go`, `reputation.go`):
- `AgentRegistry`: register agents with capabilities + stake, discover by capability (sorted by reputation).
- `TaskNegotiation`: full lifecycle — request → bid → accept → complete/dispute. Escrow on acceptance.
- `ReputationSystem`: weighted score (completion 40%, disputes 30%, response time 20%, stake 10%), decay factor.
- Messaging integration: `AgentDiscoveryTopic` and `AgentNegotiateTopic` in messaging service.

**AI Inference Precompile** (`vm/contracts_ai_inference.go`):
- Precompiled contract at `0x0301`, activated via `ChainConfig.AIInferenceTime`.
- Selectors: `0x00` requestInference, `0x01` getResult, `0x02` getModel, `0x03` listModels.
- Gas: base 10000 + 100/byte for requests; 2600 for queries; 5000 for listing.
- `InferenceBackend` interface decouples precompile from service implementation.

**ZKML Verification** (`zkprover/zkml.go`, `zkprover/zkml_trace.go`, `zkverifier/zkml_verifier.go`):
- `ZKMLProver`: generates circuits from model structure, produces proofs with 96-byte public inputs (modelHash + inputHash + outputHash).
- `ExecutionTrace`: captures per-layer intermediate values for ZK witness generation.
- `ZKMLVerifier`: validates proof structure and public input consistency.
- Coprocessor integration: `ZKMLVerifierAdapter` connects to `TieredVerifier` via `SetZKMLVerifier()`.

**AI Block Building** (`mev/ai_optimizer.go`, `mev/gas_predictor.go`):
- `AIBlockOptimizer`: scores transactions by effective tip × gas efficiency, stable-sorts preserving nonce order.
- `FairnessGuard`: detects sandwich attack patterns when fairness mode enabled.
- `DetectMEV`: identifies arbitrage (3+ same-contract txs) and liquidation (high-value) patterns.
- `GasPredictor`: EWMA-based prediction (alpha=0.3, window=32 blocks) for base fee and gas usage.
- Miner integration: `AIOptimizer` interface in `miner.go`, injected via `SetAIOptimizer()`.

**Configuration** (`conf/ai_config.go` — `AICfg`):

| Field | Default | Description |
|-------|---------|-------------|
| `AICfg.Wallet.Enabled` | `false` | Master switch for agent wallet service |
| `AICfg.Wallet.MaxSessionKeys` | `16` | Max session keys per account |
| `AICfg.Wallet.PaymasterEnabled` | `false` | Enable gas sponsorship |
| `AICfg.Coord.RegistryEnabled` | `false` | Enable agent discovery |
| `AICfg.Coord.MinAgentStake` | `0.1 ETH` | Minimum stake for registration |
| `AICfg.Coord.NegotiationTimeoutSec` | `300` | Task negotiation timeout |
| `AICfg.MEV.Enabled` | `false` | Enable AI block optimization |
| `AICfg.MEV.FairnessMode` | `true` | Enable sandwich detection |
| `AICfg.MEV.FallbackOnError` | `true` | Fall back to standard ordering |

### AI Safety Infrastructure

`internal/ai/governance/`, `internal/ai/training/`, and `internal/ai/attestation/` provide three modular, decoupled AI safety subsystems:

**Feature 1 — Training Data Governance** (`ai/governance/`):
- `DatasetRegistry`: registers training datasets with content hash (via CAS), owner, metadata. Links datasets to trained models for provenance.
- `Committee`: human ethics review committee with quorum/threshold voting. Members cast votes signed with secp256k1 (signature over `Keccak256(datasetID || decision || category)`). Voter address recovered via `crypto.SigToPub`.
- Categories: Fairness, Privacy, ContentSafety, Transparency. Dataset must pass all required categories.
- Lifecycle: `DatasetPending` → `DatasetUnderReview` (on first vote) → `DatasetApproved`/`DatasetRejected` (on finalize).

**Feature 2 — ZK Training Verification** (`ai/training/`):
- `TrainingProver`: generates ZK proofs that a model was trained from approved datasets with specific config/weights.
- `DatasetGovernance` interface: decouples from governance package. Checks `IsApproved(datasetID)` before allowing training registration.
- `TrainingTrace`: epoch-level checkpoints (weights before/after, loss, gradients) for ZK witness generation.
- `TrainingProof`: 160-byte public inputs = `modelHash(32) || initWeightsHash(32) || finalWeightsHash(32) || configHash(32) || datasetRootHash(32)`.
- `TrainingVerifier`: structural validation + public input consistency (mirrors `ZKMLVerifier` pattern).

**Feature 3 — ZK Inference Attestation** (`ai/attestation/`):
- `AttestationService`: creates signed attestations binding inference results to their full provenance chain (model, training, data governance).
- `SignedAttestation`: operator signs canonical attestation bytes with secp256k1. Signature verified via `crypto.SigToPub`.
- `AttestationChain`: validates multi-hop pipelines (e.g., perception → planning → control for autonomous driving). Enforces `output[i] == input[i+1]`.
- `SafetyLevel`: Standard (text/images), HighValue (financial/medical), Critical (autonomous driving, robotics). Critical level requires verified training record.
- TTL-based expiry with `Prune()` for memory management.

**Interface Decoupling:**
```
governance.Committee implements training.DatasetGovernance
training.TrainingProver → used by attestation.TrainingVerification
zkprover.ZKMLProver → used by attestation.ZKProofProvider
```

**Configuration** (`conf/ai_config.go` — `AICfg`):

| Field | Default | Description |
|-------|---------|-------------|
| `AICfg.Governance.Enabled` | `false` | Enable data governance |
| `AICfg.Governance.MaxDatasets` | `10000` | Max registered datasets |
| `AICfg.Governance.CommitteeQuorum` | `3` | Min votes for valid decision |
| `AICfg.Governance.CommitteeThreshold` | `0.67` | Approval ratio for pass |
| `AICfg.Training.Enabled` | `false` | Enable training ZK verification |
| `AICfg.Attestation.Enabled` | `false` | Enable inference attestation |
| `AICfg.Attestation.TTLSec` | `86400` | Attestation expiry (24h) |

### Messaging Platform

`internal/distributed/messaging/` provides a full decentralized communications stack, built in 6 layers:

**Layer 1 — P2P Message Relay** (`protocol.go`, `relay.go`, `peer_handler.go`):
- `Envelope` wire format: version + sender (compressed secp256k1 pubkey) + topic + payload + timestamp + nonce + signature.
- `Relay` bridges the local `Service` ↔ libp2p GossipSub. Messages are sharded across 8 topics (`/n42/msg/shard/0..7`) for load balancing (configurable via `MessageShards`).
- Ring-buffer dedup cache (default 65536 entries) prevents message re-processing.
- `PeerHandler` implements the `/n42/msg/store_query/1.0.0` stream protocol for peer-to-peer historical message queries.
- P2P topics registered in `internal/p2p/topics.go`; scoring params in `gossip_scoring_params.go` (TopicWeight 0.1, non-critical); subscription filter in `pubsub_filter.go` allows `/n42/msg/` prefix.

**Layer 2 — E2E Encryption** (`crypto/`):
- `MessagingKeyPair`: X25519 keys. `DeriveFromWallet()` deterministically derives from secp256k1 wallet key via HKDF-SHA256.
- `SealEnvelope`/`OpenEnvelope`: ephemeral X25519 ECDH → HKDF → XChaCha20-Poly1305 (24-byte nonce, immune to nonce reuse).
- `Session`: bilateral encrypted channel with chain key ratcheting. Each message derives a unique key from `chainKey + counter`, then ratchets the chain key forward. Provides forward secrecy. Export/import for session persistence.
- Crypto stack: pure Go via `golang.org/x/crypto` (curve25519, chacha20poly1305, hkdf). No CGO dependency.

**Layer 3 — RLN Anti-Spam** (`rln/`):
- `MembershipTree`: Poseidon Merkle tree (depth 20 by default, ~1M members). Precomputed empty hashes for O(1) lookup.
- `GenerateProof`: for each message, produces a Shamir share `y = secret + slope * x mod p` (BN254 scalar field, `slope = Poseidon(secret, epoch)`, `x = Poseidon(epoch, messageHash)`). Same identity sending 2 messages in same epoch → 2 shares on one line → Shamir recovery of identity secret → slash.
- `NullifierRegistry`: tracks seen nullifiers per epoch. Detects duplicate nullifiers as spam.
- `GossipSubValidator`: returns Accept/Reject/Ignore. Rejects future epochs, ignores stale epochs, rejects invalid proofs, rejects spam (with secret recovery).

**Layer 4 — Persistent Storage** (`store/`):
- `PersistentStore`: CAS-backed message persistence via `CASBackend` interface. In-memory index sorted by topic + timestamp.
- `QueryEngine`: structured queries with filters (topic, time range, sender) and cursor-based pagination. Max 1000 results per query.
- `SyncProtocol`: advertise local availability ranges, compute missing ranges vs peer, export entries for sync.

**Layer 5 — MLS Group Encryption** (`group/`):
- `GroupSession`: manages group state (epoch, ratchet tree, members). Encrypt/Decrypt uses XChaCha20-Poly1305 with AAD = groupID + epoch.
- `RatchetTree`: binary tree with O(log n) `UpdatePath` for forward secrecy after member changes. `SecretTree` derives per-sender secrets.
- `KeyPackage`: MLS cipher suite `MLS_128_HPKEX25519_CHACHA20POLY1305_SHA256_Ed25519` (0x0003), 30-day lifetime.
- `Welcome` + `Commit` protocol for add/remove/update operations.

**Layer 6 — Stream API & DID Identity** (`stream/`, `identity/`):
- `StreamServer`: SSE (Server-Sent Events) server on configurable port (default 8554). Endpoints: `/ws/messages?topic=X` (subscribe), `/health`. Clients receive JSON notifications. Pre-populated topic map before client registration eliminates race conditions.
- `DIDDocument`: W3C DID v1.1 with `did:n42:<address>` method. `CreateDID()` from wallet key, `VerifyDIDSignature()` against document verification methods.
- `DIDResolver`: LRU-cached resolver (default 1024 entries, 1h TTL). Register/Resolve/Update/Deactivate lifecycle.

**Configuration** (`conf/messaging_config.go` — `MessagingCfg`):

| Field | Default | Description |
|-------|---------|-------------|
| `Enabled` | `false` | Master switch for messaging service |
| `P2PRelayEnabled` | `false` | Enable GossipSub relay bridge |
| `MessageShards` | `8` | Number of GossipSub shard topics |
| `MaxEnvelopeSize` | `262144` | Max envelope size in bytes (256 KiB) |
| `DeduplicateCacheSize` | `65536` | Dedup ring buffer size |
| `MaxMessageSize` | `65536` | Max payload size (64 KiB) |
| `StoreCapacity` | `10000` | In-memory LRU store capacity |
| `StoreTTLSec` | `3600` | In-memory message TTL (1h) |
| `StoreQueryEnabled` | `false` | Enable peer store query protocol |
| `EncryptionEnabled` | `false` | Enable E2E encryption |
| `KeyRotationIntervalSec` | `86400` | Key rotation interval (24h) |
| `RLNEnabled` | `false` | Enable RLN anti-spam |
| `RLNRateLimit` | `10` | Local rate limit (msgs/min) |
| `RLNEpochSec` | `10` | RLN epoch duration |
| `RLNMessageLimit` | `1` | Messages per epoch per identity |
| `RLNMerkleDepth` | `20` | Membership tree depth (~1M) |
| `PersistenceEnabled` | `false` | Enable CAS persistence |
| `PersistenceMaxAgeSec` | `86400` | Persistence max age (24h) |
| `PersistenceMaxSizeMB` | `256` | Persistence max size |
| `GroupsEnabled` | `false` | Enable MLS group sessions |
| `MaxGroupSize` | `256` | Max members per group |
| `MaxGroupsPerNode` | `100` | Max groups per node |
| `StreamServerEnabled` | `false` | Enable SSE streaming server |
| `StreamServerPort` | `8554` | SSE server port |
| `DIDEnabled` | `false` | Enable DID identity |

**Node integration** (`internal/node/node.go`):
- `messagingService` field (line 162), created at line 1008-1012 when `MessagingCfg.Enabled` is true.
- Relay is injected via `Service.SetRelay()` after P2P service is available.
- Stopped in the "Distributed services" shutdown phase (line 1337-1338).

**P2P integration** (`internal/p2p/`):
- `topics.go`: `GossipMessagePrefix = "message/shard/"`, `GossipMessageFormat = "/n42/msg/shard/%d"`, `StoreQueryProtocol`.
- `gossip_scoring_params.go`: `messagingTopicParams()` — lightweight scoring (TopicWeight 0.1), injected via `topicScoreParams()` switch.
- `pubsub_filter.go`: `CanSubscribe()` allows topics with `/n42/msg/` prefix.
- `P2PPublisher` interface in `relay.go` matches `p2p.Service` methods (`PublishToTopic`, `SubscribeToTopic`).

### Default Ports

| Port  | Purpose                    |
|-------|----------------------------|
| 61015 | P2P Discovery (UDP)        |
| 61016 | P2P Communication (TCP)    |
| 20012 | JSON-RPC HTTP              |
| 20013 | JSON-RPC WebSocket         |
| 20014 | Authenticated RPC (JWT)    |
| 6060  | pprof metrics              |
| 8553  | MCP Server (AI agents)     |
| 8554  | Message Stream (SSE)       |
| 9090  | gRPC KV (RPCDaemon)        |

## Code Style & Linting

- **Linter config**: `.golangci.yml` — gosec, govet, staticcheck, errcheck, gofmt, goimports, prealloc enabled
- **Import ordering**: `goimports` with local prefix `github.com/n42blockchain/N42`
- **Generated files** (`*_gen.go`, `*.pb.go`) are excluded from linting
- **Test files** are excluded from gosec and errcheck

## Important Constraints

- **holiman/uint256**: Do NOT upgrade this dependency — it breaks `MainnetGenesisHash` calculation.
- **Build version**: Auto-incremented on every `make n42` / `make build` via `scripts/bump_version.sh`. Version stored in `VERSION` file.
- **Mobile builds**: `cmd/evmsdk/` provides iOS/Android SDK via gomobile (`make ios`, `make android`).
- **Messaging crypto**: Uses pure Go `golang.org/x/crypto` (curve25519, chacha20poly1305, hkdf, sha3). No CGO dependency. Signing uses `common/crypto` (secp256k1 via libsecp256k1 CGO).
- **RLN Poseidon hash**: Real Poseidon over BN254 via the gnark-crypto Poseidon2 permutation (width-2 compression, length-prefixed 31-byte chunk absorption, domain `n42-rln-poseidon2-v1`). **RLN is NOT production-wired** and must not be until a vetted BN254 ZK circuit lands: without ZK there is no secure nullifier choice — secret-derived is unverifiable (spammer bypass), commitment-derived (used here, verifier-recomputable) is forgeable (any third party can craft a passing proof with arbitrary in-range ShareY and censor a victim by pre-registering their nullifier). Share y-coordinates stay unverifiable without ZK — recovered secrets are only slashable when they reproduce the member's commitment. No non-test code calls the verifier today.
- **protobuf is deprecated for internal encoding**: use compact bitmask codecs (`common/block/header_compact.go` `MarshalCompact`, `receipt_compact.go`) or the Erigon V2 account/storage codec instead. proto is retained ONLY where a cross-process / cross-language boundary makes it unavoidable (P2P wire, gRPC KV for RPCDaemon, cross-language SDK contracts). Do not add proto to new internal persistence.
- **New consensus header field → update ALL codecs**: any field added to `common/block/header.go` that participates in the block hash MUST be handled in every codec — RLP (`rlpHash`), the proto/trailer `Marshal`/`parseTrailer`, AND the compact storage codec `MarshalCompact`/`unmarshalCompact` (the default for `WriteHeader`). Missing the compact codec silently drops the field on the storage round-trip, so the stored header's hash diverges from the consensus head hash and block import fails with `unknown ancestor`. Regression: `TestCompactHeaderRoundTrip`'s `full` header must include every optional field and assert the round-trip hash is unchanged.
