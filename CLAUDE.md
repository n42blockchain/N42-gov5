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
  deferred/       → Deferred execution pipeline (consensus-execution separation)
  mev/            → MEV-Boost relay integration
  mcp/            → MCP Server (AI agent data queries)
  zkprover/       → ZK proving (STARK/SNARK/SP1 three backends)
  zkverifier/     → ZK proof verification
  metrics/        → 250+ Prometheus metrics
  exex/           → Execution Extensions (ExEx) framework
  bundler/        → ERC-4337 account abstraction bundler
  peerdas/        → PeerDAS data availability sampling (EIP-7594)
modules/          → Data layer
  state/          → State management (IntraBlockState, snapshot, witness, JMT commitment)
  rawdb/          → Raw database operations (MDBX backend, freezer, log index)
  rpc/            → JSON-RPC transport (HTTP, WebSocket, IPC)
  ethdb/          → Database interface abstraction
lib/              → Shared libraries
  kv/             → Key-value store (mdbx/, memdb/, remotedb/, remotedbserver/, layered/)
  jmt/            → Jellyfish Merkle Tree (Blake3, sparse cache, ref-counting GC)
  state/          → HistoryV3 aggregator (per-block changeset + inverted index)
common/           → Shared types and utilities
  types/          → Address, Hash, core blockchain types
  block/          → Block/Header/Body interfaces
  transaction/    → Transaction types (Legacy, AccessList, DynamicFee, Blob, SetCode)
  crypto/         → Cryptographic functions (bls/, stark/, dilithium/, falcon/)
params/           → Chain parameters (config, blob_schedule, chainspecs/)
conf/             → Node configuration (all subsystem configs)
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
- **State management**: `modules/state/` handles state trie with changeset tracking; `modules/state/commitment/` provides JMT Blake3 state commitment with ref-counting GC for online pruning.
- **P2P**: Built on `go-libp2p` with custom protocols for block/transaction/blob/witness propagation.
- **PQ isolation**: Post-quantum precompiles (0x14-0x17) are NOT in standard fork maps; activated only via `ChainConfig.PQPrecompilesTime`.
- **Distributed compute platform**: `internal/distributed/` provides a full distributed compute stack:
  - **Tiered verification** (Brevis coChain pattern): ZK proof (default) → Optimistic with bond+challenge window → TEE attestation. Tasks route through `TieredVerifier` based on `VerificationTier`.
  - **Provider network** (EigenLayer AVS + Akash model): providers register with stake+capabilities, claim tasks or bid in reverse-auction marketplace, get rewarded/slashed via Verify-or-Slash economic model.
  - **WASM engine**: sandboxed execution with fuel-based gas metering, host functions (CAS load/store, keccak256, logging), compilation cache. Runtime interface wraps wazero.
  - **Batch compute**: MapReduce over CAS data — job splits into map tasks, parallel execution, ordered reduce with panic recovery.
  - **AI inference** (ORA opML): model registry, optimistic ML verification with fraud proof challenges.
  - **State machine enforcement**: `validTransition()` in task.go enforces legal status transitions; atomic `TransitionToProving`/`TransitionToChallenged` prevent TOCTOU races.

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
- `GenerateProof`: for each message, produces a Shamir share `y = secret + hash * x mod p` (BN254 scalar field). Same identity sending 2 messages in same epoch → 2 shares → Shamir recovery of identity secret → slash.
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
- **RLN Poseidon hash**: Currently a Keccak256-based approximation with domain separation (`n42-rln-poseidon-v1`). Swap for a real BN254 Poseidon when a vetted Go implementation is available.
