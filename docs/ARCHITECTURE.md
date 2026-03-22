# N42 Architecture

This document describes the internal architecture of N42, a high-performance
Ethereum-compatible blockchain node written in Go.

## Layer Diagram

```
cmd/n42/           -> Entry point, CLI (urfave/cli v2)
internal/           -> Core business logic (private, not importable)
  node/             -> Service orchestrator: creates & wires all components
  consensus/        -> Pluggable consensus engines (apoa/apos)
  miner/            -> Block production and sealing
  txspool/          -> Transaction pool management
  vm/               -> EVM execution (interpreter, opcodes, gas)
  avm/              -> N42 AVM (alternative VM)
  api/              -> JSON-RPC backend implementation
  sync/             -> Chain synchronization (initial-sync, snap-sync, checkpoint)
  p2p/              -> libp2p networking with Kademlia DHT
  exex/             -> Execution Extensions (post-block plugin system)
  parallel/         -> Block-STM parallel EVM execution
  tracing/          -> OpenTelemetry distributed tracing
  tracers/          -> Debug/trace (js/ via goja, native/ Go tracers)
  metrics/          -> Runtime, chain, and txpool metrics
  snapshot/         -> Block snapshot management
modules/            -> Data layer (importable by external packages)
  state/            -> State trie management with changeset tracking
  rawdb/            -> Raw database operations (MDBX backend)
  rawdb/era/        -> EraE standardized history archive (reader/writer/index)
  rawdb/freezer/    -> Ancient/freezer cold storage for old blocks
  rpc/              -> JSON-RPC transport (HTTP, WebSocket, IPC)
  ethdb/            -> Database interface abstraction
  event/            -> Event subscription system
  changeset/        -> State changeset tracking
lib/                -> Shared libraries (importable)
  kv/               -> Key-value store interfaces
  kv/mdbx/          -> MDBX backend implementation
  kv/memdb/         -> In-memory backend (testing)
  kv/layered/       -> Layered DB (state + history split)
  kv/remotedb/      -> Remote DB client
  types/            -> Core type definitions
  common/           -> Utility packages
common/             -> Public types (importable)
  block/            -> Block, Header, Receipt, Log types
  transaction/      -> Transaction types
  types/            -> Address, Hash
  crypto/           -> Cryptographic functions
  rlp/              -> RLP encoding/decoding
params/             -> Chain parameters, genesis configs (embedded JSON)
conf/               -> Configuration structs
accounts/           -> Account management (keystore/)
contracts/          -> Smart contracts (deposit contract with tiered staking)
sdk/                -> Public SDK re-exports for library consumers
```

## Key Interfaces

| Interface | Package | Purpose | Implementations |
|---|---|---|---|
| `consensus.Engine` | `internal/consensus` | Block validation, sealing, difficulty | `apoa.Apoa` (PoA), `apos.APos` (PoS), `apos.FakeAPos` (test) |
| `common.IBlockChain` | `common` | Blockchain state and insertion | `internal.BlockChain` |
| `common.ITxsPool` | `common` | Transaction pool | `internal/txspool.TxsPool` |
| `kv.RwDB` | `lib/kv` | Read-write database | `mdbx.MdbxKV`, `memdb.MemoryDB`, `layered.LayeredDB` |
| `kv.Tx` / `kv.RwTx` | `lib/kv` | Database transactions | MDBX tx, memory tx, layered tx |
| `exex.Extension` | `internal/exex` | Execution extension plugin | `extensions.LogExtension` |
| `freezer.FreezerAPI` | `modules/rawdb/freezer` | Ancient/cold block storage | `freezer.Freezer` |
| `common.IStateDB` | `common` | EVM state database | `state.IntraBlockState` |
| `common.Service` | `common` | Stoppable service | Various services |
| `state.StateReader` | `modules/state` | State reading | `PlainStateReader`, `CachedStateReader` |
| `state.WriterWithChangeSets` | `modules/state` | State writing with history | `PlainStateWriter`, `NoopWriter` |
| `Processor` | `internal` | Block transaction processing | `StateProcessor` |
| `Validator` | `internal` | Block/state validation | `BlockValidator` |
| `p2p.P2P` | `internal/p2p` | Peer-to-peer networking | `p2p.Service` |
| `consensus.ChainHeaderReader` | `internal/consensus` | Chain header access for consensus | `internal.BlockChain` |

## Service Wiring Flow

`internal/node/node.go` (`NewNode`) creates and connects all services in this order:

```
1. OpenDatabase         -> kv.RwDB (MDBX or LayeredDB or memdb)
2. Read/write genesis   -> genesisBlock, chainConfig
3. p2p.NewService       -> P2P networking layer
4. consensus engine     -> apoa.New / apos.New / apos.NewFaker (based on ChainConfig.Consensus)
5. NewBlockChain        -> IBlockChain (wires DB, engine, P2P, config)
   5a. SetParallelEVM   -> enable Block-STM if configured
   5b. SetPrefetch      -> enable state prefetching if configured
   5c. SetFreezer       -> attach ancient DB + start background freeze goroutine
   5d. SetExExManager   -> attach Execution Extensions manager
6. deposit.NewDeposit   -> deposit contract (PoS only)
7. txspool.NewTxsPool   -> transaction pool (depends on blockchain + deposit)
8. initialsync.NewService -> initial block sync
9. snapsync.NewService    -> snap sync (state download)
10. checkpoint.NewService -> checkpoint sync
11. sync.NewService       -> sync orchestrator (P2P + blockchain + initial-sync)
12. miner.NewMiner        -> block producer (blockchain + engine + txpool)
13. api.NewAPI            -> JSON-RPC backend (blockchain + DB + engine + txpool)
```

`Start()` activates services in order:
1. Blockchain background loops
2. ExEx manager
3. Miner (if mining enabled, authorizes consensus engine)
4. JSON-RPC (HTTP, WebSocket, authenticated)
5. P2P networking
6. Sync service
7. Metrics
8. Deposit contract
9. Checkpoint sync -> Snap sync -> Initial sync (async pipeline)
10. Snapshot manager (if enabled)
11. Pruner (if enabled)

`Close()` tears down in reverse dependency order (consumers first, infrastructure last).

## Extension Points

### Adding a New Consensus Engine

1. Implement `consensus.Engine` interface in `internal/consensus/myengine/`.
2. Add a new `params.ConsensusType` constant in `params/`.
3. Add a case to the `switch cfg.ChainCfg.Consensus` block in `node.go:NewNode`.
4. Supply chain config in `params/chainspecs/`.

### Adding a New Database Backend

1. Implement `kv.RwDB` interface (see `lib/kv/kv_interface.go`).
2. Wire it into `node.OpenDatabase` or `openLayeredDatabase`.
3. The `kv.Tx` / `kv.RwTx` interfaces must support cursor-based iteration and bucket operations.

### Adding an Execution Extension (ExEx)

1. Implement `exex.Extension` interface (Name, Start, OnNotification, Stop).
2. Register it in `node.go` via `exexMgr.Register(&myExtension{})`.
3. The extension receives `ExExNotification` after each block commit/revert containing the committed chain and reverted chain.

### Adding a New Freezer Backend

1. Implement `freezer.FreezerAPI` interface in `modules/rawdb/freezer/`.
2. Pass it to `BlockChain.SetFreezer` instead of the default `freezer.Freezer`.

## Dependency Rules

The layer structure enforces these import rules:

```
cmd/         -> can import everything
internal/    -> can import common/, lib/, modules/, params/, conf/
               CANNOT be imported by modules/, lib/, common/
modules/     -> can import common/, lib/, params/
               CANNOT be imported by lib/, common/
lib/         -> can import only standard library and its own sub-packages
               CANNOT be imported by common/
common/      -> can import only standard library, proto, and common/ sub-packages
params/      -> can import only standard library and common/
conf/        -> can import params/, common/
sdk/         -> can import common/, lib/, modules/, params/ (public re-exports)
```

Key constraints:
- `internal/` packages are Go-private: only code within the module can import them.
- `common/block`, `common/transaction`, `common/types` are the shared type vocabulary.
- `lib/kv` defines database interfaces; backends live in `lib/kv/mdbx`, `lib/kv/memdb`, etc.
- `modules/state` and `modules/rawdb` are the data access layer, importable by `internal/` and `sdk/`.

## Database Architecture

N42 uses MDBX (memory-mapped B+ tree) as its primary key-value store:

- **Hot DB**: Current state, recent blocks, indexes (MDBX, `lib/kv/mdbx/`)
- **Layered DB** (optional): Splits hot DB into state DB + history DB (`lib/kv/layered/`)
- **Freezer/Ancient DB** (optional): Immutable flat files for old block data (`modules/rawdb/freezer/`)
- **In-memory DB**: For testing (`lib/kv/memdb/`)

State management uses incremental Keccak hashing (not Merkle Patricia Trie). Account data is protobuf-encoded.

## Default Ports

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

## AI-Native Infrastructure

N42 includes a complete AI safety and agent infrastructure at the L1 level:

```
internal/
  ai/                    AI infrastructure (modular, decoupled)
    wallet/              AI agent wallets (session keys, spending policies, paymaster)
      account.go           Session keys, spending policies
      service.go           Agent service orchestrator
    coord/               AI agent coordination
      registry.go          Capability-based agent discovery
      negotiation.go       Task delegation protocol
      reputation.go        Weighted reputation system
    governance/          Training data governance (ethics committee voting)
    training/            ZK training verification (model provenance)
    attestation/         ZK inference attestation (signed results, chain-of-custody)
  vm/
    contracts_ai_inference.go  AI inference precompile (0x0301)
  mev/
    ai_optimizer.go      AI transaction ordering
    gas_predictor.go     EWMA gas prediction
  zkprover/
    zkml.go              ZKML proof generation
    zkml_trace.go        Execution trace capture
  exex/extensions/
    ai_indexer.go        AI data pipeline (token/event/gas indexing)
  mcp/
    data_tools.go        AI data query tools
    agent_tools.go       Agent discovery tools
```

### Interface Decoupling

The AI subsystems communicate through interfaces, not concrete types:

- `training.DatasetGovernance` ← implemented by `governance.Committee`
- `attestation.TrainingVerification` ← implemented by `training.TrainingProver`
- `attestation.ZKProofProvider` ← wraps `zkprover.ZKMLProver`
- `miner.AIOptimizer` ← implemented by `mev.AIBlockOptimizer`
- `vm.InferenceBackend` ← wraps `inference.InferenceService`

Each feature can be enabled/disabled independently via `conf.AICfg`.

## Fork Schedule

N42 supports all Ethereum execution forks through Glamsterdam:

| Fork | Activation | Key Features |
|------|-----------|-------------|
| London | Block 0 | EIP-1559 base fee |
| Shanghai | Block 11907216 | Withdrawals |
| Cancun | Timestamp | EIP-4844 blobs |
| Pectra | Timestamp | EIP-7702 SetCode, BLS, 9 EIPs |
| Osaka | Timestamp | EOF opcodes |
| Fusaka | Timestamp | PeerDAS, BPO, gas metering |
| Glamsterdam | Timestamp | EIP-7904 gas repricing (-78.6% transfers) |

N42-specific extensions (independent activation):
- PQ Precompiles (Falcon, Dilithium, SQIsign)
- CAS Precompile (0x0300)
- AI Inference Precompile (0x0301)

## Data Tiering & Sync

### Storage Tiering

Operators can split data across storage tiers via `storage_tier` config:

| Tier | Content | Size | Recommended |
|------|---------|------|------------|
| Hot | Chaindata MDBX, tmp | ~50 GB | NVMe |
| Warm | Domain snapshots, accessors | ~300 GB | NVMe/SSD |
| Cold | History, indices, freezer, EraE | ~800 GB | HDD |

### OtterSync (BitTorrent Chain Sync)

`internal/sync/torrentsync/` enables initial sync via BitTorrent:

1. Seed node exports frozen blocks → EraE segments (8192 blocks/file)
2. Manifest lists available segments with SHA256 hashes
3. New node downloads segments via BitTorrent/HTTP webseed
4. Importer writes blocks to DB with hash chain verification
5. Regular P2P sync continues from the import tip

### JMT Archive Compression

`lib/jmt/archive/` compresses historical JMT nodes for long-term storage:

- Nodes stored as hash+data word pairs in `lib/seg/` compressed segments
- `lib/recsplit/` RecSplit index provides O(1) lookup by node hash
- Enables `eth_getProof` for historical blocks from compressed archives

### JMT Performance

The Jellyfish Merkle Tree uses a two-level caching strategy:

1. **Parsed Node Cache** (`tree.go`): 65536-entry LRU of decoded `*Node` objects,
   persisted across block validations (not reset per payload)
2. **CachedStore** (`store/cached_store.go`): Read-through LRU byte cache wrapping
   `LazyDBStore`, eliminating per-Get MDBX transaction overhead

Together these reduce MDBX reads by 50-80% during steady-state operation.

### Stateless Validation

`internal/stateless/` enables block verification without full state:

- `Validator`: verifies blocks using JMT Merkle proof witnesses
- `CodeCache`: LRU cache for contract bytecodes (~10GB for mainnet)
- Witness infrastructure: `modules/state/witness/` provides `BlockWitness`,
  `WitnessStateReader`, proof generation and verification
- P2P witness protocol: `internal/sync/rpc_witness.go`
