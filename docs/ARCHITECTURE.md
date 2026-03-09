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
contracts/          -> Smart contracts (deposit/AMT, deposit/FUJI, deposit/NFT)
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
