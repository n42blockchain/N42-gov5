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
    messaging/    → Decentralized messaging relay (publish/subscribe + RLN rate limit)
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
