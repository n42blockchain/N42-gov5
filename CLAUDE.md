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
  tracers/        → Debug/trace (js/ for JS tracers via goja, native/ for Go tracers)
modules/          → Data layer
  state/          → State trie management
  rawdb/          → Raw database operations (MDBX backend)
  rpc/            → JSON-RPC transport (HTTP, WebSocket, IPC)
  ethdb/          → Database interface abstraction
lib/              → Shared libraries
  kv/             → Key-value store interfaces (kv/mdbx/, kv/memdb/)
  types/          → Core type definitions
  common/         → Utility packages
common/           → Shared types and utilities
  types/          → Address, Hash, and core blockchain types
  block/          → Block interfaces and types
  transaction/    → Transaction types
  crypto/         → Cryptographic functions
  rlp/            → RLP encoding/decoding
params/           → Chain parameters, genesis configs (mainnet.json/testnet.json embedded via //go:embed)
conf/             → Node configuration structs (RPC, P2P, consensus settings)
accounts/         → Account management (keystore/, abi/)
contracts/        → Smart contracts (deposit/AMT, deposit/FUJI, deposit/NFT)
turbo/            → Performance optimization layers (rpchelper, etc.)
```

### Key Patterns

- **Node** (`internal/node/node.go`) is the central orchestrator — it creates DB, consensus engine, miner, txpool, P2P, and RPC stack, then manages lifecycle (Start/Stop).
- **Consensus is pluggable**: `apoa` (Authority PoA) and `apos` (Authority PoS) implement the `consensus.Engine` interface.
- **Database**: MDBX (memory-mapped B+ tree) via `lib/kv/mdbx/`; `lib/kv/memdb/` for testing.
- **State management**: `modules/state/` handles state trie with changeset tracking in `modules/changeset/`.
- **P2P**: Built on `go-libp2p` with custom protocols for block/transaction propagation.
- **Chain specs**: `params/chainspecs/mainnet.json` and `testnet.json` are embedded at compile time.

### Default Ports

| Port  | Purpose                    |
|-------|----------------------------|
| 61015 | P2P Discovery (UDP)        |
| 61016 | P2P Communication (TCP)    |
| 20012 | JSON-RPC HTTP              |
| 20013 | JSON-RPC WebSocket         |
| 20014 | Authenticated RPC (JWT)    |
| 6060  | pprof metrics              |

## Code Style & Linting

- **Linter config**: `.golangci.yml` — gosec, govet, staticcheck, errcheck, gofmt, goimports, prealloc enabled
- **Import ordering**: `goimports` with local prefix `github.com/n42blockchain/N42`
- **Generated files** (`*_gen.go`, `*.pb.go`) are excluded from linting
- **Test files** are excluded from gosec and errcheck

## Important Constraints

- **holiman/uint256**: Do NOT upgrade this dependency — it breaks `MainnetGenesisHash` calculation.
- **Build version**: Auto-incremented on every `make n42` / `make build` via `scripts/bump_version.sh`. Version stored in `VERSION` file.
- **Mobile builds**: `cmd/evmsdk/` provides iOS/Android SDK via gomobile (`make ios`, `make android`).
