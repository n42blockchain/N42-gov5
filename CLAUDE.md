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

See `docs/ARCHITECTURE_MAP.md` (layer map, key patterns, state-root engines), `docs/AI_INFRASTRUCTURE.md` (agent wallet, coordination, inference precompile, ZKML, AI safety) and `docs/MESSAGING_PLATFORM.md` (relay, E2E, RLN, store, MLS groups, stream/DID). Read them only when working in those areas.

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
- **Build version**: Every `make n42` / `make build` increments the build component exactly once before compiling, updates both `VERSION` and `params/version.go`, then enforces equality with `version-check`. `make release` delegates to that same build path and must not bump a second time. A version-changing build therefore leaves those two files modified for the corresponding build commit.
- **Mobile builds**: `cmd/evmsdk/` provides iOS/Android SDK via gomobile (`make ios`, `make android`).
- **Messaging crypto**: Uses pure Go `golang.org/x/crypto` (curve25519, chacha20poly1305, hkdf, sha3). No CGO dependency. Signing uses `common/crypto` (secp256k1 via libsecp256k1 CGO).
- **RLN Poseidon hash**: Real Poseidon over BN254 via the gnark-crypto Poseidon2 permutation (width-2 compression, length-prefixed 31-byte chunk absorption, domain `n42-rln-poseidon2-v1`). **RLN is NOT production-wired** and must not be until a vetted BN254 ZK circuit lands: without ZK there is no secure nullifier choice — secret-derived is unverifiable (spammer bypass), commitment-derived (used here, verifier-recomputable) is forgeable (any third party can craft a passing proof with arbitrary in-range ShareY and censor a victim by pre-registering their nullifier). Share y-coordinates stay unverifiable without ZK — recovered secrets are only slashable when they reproduce the member's commitment. No non-test code calls the verifier today.
- **protobuf is deprecated for internal encoding**: use compact bitmask codecs (`common/block/header_compact.go` `MarshalCompact`, `receipt_compact.go`) or the Erigon V2 account/storage codec instead. proto is retained ONLY where a cross-process / cross-language boundary makes it unavoidable (P2P wire, gRPC KV for RPCDaemon, cross-language SDK contracts). Do not add proto to new internal persistence.
- **Storage keys carry NO incarnation**: `HashedStorage`/`TrieOfStorage` keys are `addrHash(32)+slotHash(32)` / `addrHash(32)+path`. The incarnation was removed in an earlier cleanup; any 40-byte `addrHash+inc` prefix left in a tool is a leftover bug (DATC had one until format v2) — delete it, never re-add. Plain-state keys are `addr(20)+slot(32)` (52 B) and the StorageHistory index seeks with exactly that prefix. Not to be confused with the Block-STM `incarnation` in `modules/state/block_stm_scheduler.go`, `mvhashmap.go`, `mv_view.go`, `parallel_executor.go` and `internal/parallel/`: that is the per-transaction re-execution counter and stays.
- **New consensus header field → update ALL codecs**: any field added to `common/block/header.go` that participates in the block hash MUST be handled in every codec — RLP (`rlpHash`), the proto/trailer `Marshal`/`parseTrailer`, AND the compact storage codec `MarshalCompact`/`unmarshalCompact` (the default for `WriteHeader`). Missing the compact codec silently drops the field on the storage round-trip, so the stored header's hash diverges from the consensus head hash and block import fails with `unknown ancestor`. Regression: `TestCompactHeaderRoundTrip`'s `full` header must include every optional field and assert the round-trip hash is unchanged.
