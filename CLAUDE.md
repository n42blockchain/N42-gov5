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
cmd/
  n42/              → Main entry point (urfave/cli v2), node lifecycle
  rpcdaemon/        → Standalone RPC daemon (connects to core node via gRPC)
  clef/             → External signer service (IPC + rules engine + audit log)
  evmsdk/           → Mobile SDK for iOS/Android (gomobile)
  zkguest/          → ZK guest program (RISC-V64 target for zkVM proving)
internal/           → Core business logic (private packages)
  node/             → Node orchestration: creates and wires all subsystems (Start/Stop lifecycle)
  consensus/        → Consensus engines:
    apoa/           → Authority PoA (Clique-style voting)
    apos/           → Authority PoS (deposit-based + reward tiers)
    hotstuff/       → HotStuff-2 BFT (2-round finality, BLS12-381, validator reconfiguration)
    misc/           → Fork-specific consensus helpers (EIP-1559, EIP-4844)
  miner/            → Block production (worker loop, transaction ordering, witness generation)
  txspool/          → Transaction pool management
    encrypted/      → Encrypted mempool (threshold AES-256-GCM, anti-MEV)
  vm/               → EVM execution
    eips_pectra.go  → Pectra EIPs: EIP-7702 (delegation), EIP-2537 (BLS), EIP-6110 (deposits)
    eips_osaka.go   → Osaka EIPs: EOF (EVM Object Format), DATACOPY, RETURNDATALOAD
    eips_fusaka.go  → Fusaka EIPs: EIP-7883 (MODEXP gas), EIP-7825 (tx gas limit)
    pq_contracts.go → Post-quantum precompiles (Falcon, Dilithium2/3, SQIsign) — isolated via PQPrecompilesTime
    precompiles/    → Precompile registry with metrics
  avm/              → N42 AVM (alternative VM)
  p2p/              → libp2p-based networking with Kademlia DHT, discv4/v5
  sync/             → Chain synchronization
    initialsync/    → Initial full/snap sync
    snapsync/       → Snapshot-based sync
    checkpoint/     → Checkpoint sync (trusted hash)
    staged/         → Staged sync pipeline (7 stages: Headers→Bodies→Senders→Execution→HashState→Commitment→Finish)
  api/              → JSON-RPC API backend
    engine_api_v1.go      → Engine API v1 (Paris)
    engine_api_blob.go    → Engine API v3 (Cancun/blobs)
    engine_api_v4.go      → Engine API v4 (Pectra/requests)
    engine_payload_*.go   → Payload validation (structural + stateful)
    engine_overlay.go     → Overlay state for Engine API blocks
    otterscan_api.go      → Otterscan block explorer API (ots_* namespace)
    graphql/              → GraphQL API (EIP-1767)
    filters/              → Log filtering with roaring bitmap indices
    blockscout.go         → Blockscout compatibility + eth_getProof (JMT Merkle proofs)
    hotstuff_reconfig_api.go → Validator reconfiguration RPC (admin_proposeAddValidator)
  tracers/          → Debug/trace
    native/         → Go tracers: callTracer, 4byteTracer, prestateTracer, flatCallTracer, liveTracer
    js/             → JavaScript tracers via goja
  deferred/         → Deferred (async) execution pipeline (Monad/Aptos-style consensus-execution separation)
  mev/              → MEV-Boost relay integration (builder API, block auction)
  mcp/              → MCP Server (Model Context Protocol for AI agents)
  zkprover/         → ZK proving service
    guest/          → Guest program (full EVM execution in zkVM context)
    sp1_client.go   → SP1 zkVM prover backend (simulation + network modes)
  zkverifier/       → ZK proof verification (STARK, SNARK, SP1)
  metrics/          → 182+ Prometheus metrics (EVM, chain, reorg, fee market, tx lifecycle, Engine API, RPC, JMT)
  exex/             → Execution Extensions (ExEx) framework
  bundler/          → ERC-4337 account abstraction bundler
  peerdas/          → PeerDAS data availability sampling (EIP-7594)
  snapshot/         → Block snapshot compression for P2P
  download/         → Block download management
  forkchoice.go     → Fork choice rule (with randomized tie-breaking)
  parallel_processor.go → Block-STM parallel EVM execution (MVS + wave validation)
modules/            → Data layer
  state/            → State management
    intra_block_state.go → Per-block mutable state (accounts, storage, logs, journal)
    snapshot/       → Snapshot acceleration layer (DiffLayer tree + DiskLayer + MDBX persistence)
    witness/        → Stateless block witness (generation, verification, encoding)
    commitment/     → JMT state commitment (Blake3-based Merkle proofs)
  rawdb/            → Raw database operations
    freezer/        → Ancient/cold data storage (5-table freezer)
    log_index*.go   → Roaring bitmap log index (LogTopicIndex, LogAddressIndex)
  rpc/              → JSON-RPC transport (HTTP, WebSocket, IPC)
    jsonrpc/        → JSON-RPC 2.0 server implementation
  ethdb/            → Database interface abstraction
  changeset/        → Per-block state changesets (AccountChangeSet, StorageChangeSet)
  event/            → Event subscription system
lib/                → Shared libraries
  kv/               → Key-value store interfaces
    mdbx/           → MDBX backend (memory-mapped B+ tree)
    memdb/          → In-memory backend (testing)
    remotedb/       → Remote gRPC KV client (for RPCDaemon)
    remotedbserver/ → gRPC KV server (exposes MDBX over network)
    layered/        → ShardedCache + LayeredDB
    temporal/       → Time-travel DB (TemporalTx for historical state queries)
    membatch/       → Batched mutation layer
  jmt/              → Jellyfish Merkle Tree (16-ary sparse trie, Blake3 hashing)
    store/          → Node stores: MDBXStore, LazyDBStore, MemStore
  state/            → HistoryV3 aggregator (per-block changeset + inverted index)
  metrics/          → Base Prometheus metrics framework
  gointerfaces/     → gRPC proto-generated interfaces (remote KV, sentry, ETH backend)
  log/v3/           → Legacy log15-style logger
common/             → Shared types and utilities
  types/            → Address, Hash, and core blockchain types
  block/            → Block/Header/Body interfaces
  transaction/      → Transaction types (Legacy, AccessList, DynamicFee, Blob, SetCode)
  crypto/           → Cryptographic functions
    bls/            → BLS12-381 signatures (blst backend)
    stark/          → STARK proof aggregation
    dilithium/      → Post-quantum Dilithium signatures
    falcon/         → Post-quantum Falcon-512 signatures
  rlp/              → RLP encoding/decoding
  hash/             → Hashing utilities (Keccak, DeriveSha)
params/             → Chain parameters
  config.go         → ChainConfig (all fork fields + PQPrecompilesTime)
  config_rules.go   → Fork activation rules (isForked with safe Cmp)
  blob_schedule.go  → BPO blob gas schedule
  chainspecs/       → Embedded mainnet.json / testnet.json
conf/               → Node configuration
  config.go         → Master Config struct
  node_config.go    → NodeConfig (JMTCommitment, PrivateAPIAddr, Prefetch, AncientDB)
  deferred_config.go → DeferredExecConfig (Enabled, QueueSize, Workers)
  mcp_config.go     → MCPCfg (Host, Port, AllowedTools)
  zkprover_config.go → ZKProverCfg (ProofType: stark/snark/sp1, ProverAddr)
  mev_config.go     → MEVBoostCfg
  encrypted_pool_config.go → EncryptedPoolCfg
  graphql_config.go → GraphQLCfg
  logger_config.go  → LoggerConfig (JSONFormat, Level, rotation)
  genesis_hive.go   → Hive test environment variable mapping
accounts/           → Account management (keystore/, abi/, external/)
contracts/          → Smart contracts (deposit/AMT, deposit/FUJI, deposit/NFT)
turbo/              → Performance optimization layers (rpchelper, etc.)
scripts/            → Operational scripts
  run_archive_smoke.sh → Archive node validation (8 RPC checks)
  run_soak_24h.sh      → 24h resource boundary soak test (goroutine/heap/RSS red-lines)
  run_eest_shards.sh   → EEST compatibility test runner
```

### Key Patterns

- **Node** (`internal/node/node.go`) is the central orchestrator — it creates DB, consensus engine, miner, txpool, P2P, RPC stack, MCP server, ZK prover, deferred executor, gRPC KV server, then manages lifecycle (Start/Stop).
- **Consensus is pluggable**: `apoa` (Authority PoA), `apos` (Authority PoS), and `hotstuff` (HotStuff-2 BFT) implement the `consensus.Engine` interface.
- **Database**: MDBX (memory-mapped B+ tree) via `lib/kv/mdbx/`; `lib/kv/memdb/` for testing; `lib/kv/remotedb/` for RPCDaemon gRPC access.
- **State management**: `modules/state/` handles state trie with changeset tracking; `modules/state/commitment/` provides JMT Blake3 state commitment; archive mode is the default.
- **P2P**: Built on `go-libp2p` with custom protocols for block/transaction/blob/witness propagation.
- **Chain specs**: `params/chainspecs/mainnet.json` and `testnet.json` are embedded at compile time.
- **PQ isolation**: Post-quantum precompiles (0x14-0x17) are NOT in standard fork maps; activated only via `ChainConfig.PQPrecompilesTime` independent switch.
- **ZK proving**: `internal/zkprover/` supports STARK, SNARK, and SP1 proof backends via `ProverClient` interface; `cmd/zkguest/` is the RISC-V64 guest program.

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
| 9090  | gRPC KV (RPCDaemon, when configured) |

## Code Style & Linting

- **Linter config**: `.golangci.yml` — gosec, govet, staticcheck, errcheck, gofmt, goimports, prealloc enabled
- **Import ordering**: `goimports` with local prefix `github.com/n42blockchain/N42`
- **Generated files** (`*_gen.go`, `*.pb.go`) are excluded from linting
- **Test files** are excluded from gosec and errcheck

## Important Constraints

- **holiman/uint256**: Do NOT upgrade this dependency — it breaks `MainnetGenesisHash` calculation.
- **Build version**: Auto-incremented on every `make n42` / `make build` via `scripts/bump_version.sh`. Version stored in `VERSION` file.
- **Mobile builds**: `cmd/evmsdk/` provides iOS/Android SDK via gomobile (`make ios`, `make android`).
- **PQ precompiles**: Must NEVER appear in standard fork precompile maps (Prague/Pectra/Osaka/Fusaka). Only activated via `PQPrecompilesTime`.
- **JMT Tree**: NOT thread-safe. Callers must ensure single-goroutine access (same as IntraBlockState).
- **isForked()**: Uses `big.Int.Cmp()` (not `Uint64()`) to prevent silent truncation of fork activation values.
