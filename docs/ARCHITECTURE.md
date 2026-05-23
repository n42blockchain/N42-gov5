# N42 Architecture

This document describes the internal architecture of N42, a high-performance
Ethereum-compatible blockchain node written in Go.

## Dual-Mode Architecture

N42 ships two binaries that share the same data layer (`modules/`, `lib/kv/`,
`internal/vm/`, `modules/state/`) but serve very different roles:

| | `cmd/n42` — N42 native chain | `cmd/eth-el` + `cmd/ethexec` — Ethereum EL |
|---|---|---|
| **Role** | Own L1, own consensus, own genesis, own validator set | Ethereum execution layer following the standard Engine API |
| **Consensus** | HotStuff-2 BFT (2-round, BLS12-381 aggregate, Rotor relay) — own pluggable APoS / APoA / HotStuff | Driven externally by a CL (Lighthouse / Prysm / Caplin) over `engine_*` JSON-RPC |
| **State commitment** | JMT (Blake3, 16-ary, ref-count GC) + LtHash digest in `Header.LtHashRoot` | Standard Ethereum HPH MPT — byte-compatible with mainnet `stateRoot` |
| **Block production** | `internal/miner` + Block-STM parallel EVM, ZISK fast-path proving | Receives `engine_newPayloadV{1..4}` from the CL and executes via `EngineStateAdapter` |
| **Mobile verification** | Yes — `cmd/evmsdk` SDK + lightweight BLS-attestation path | No (mainnet is the source of truth) |
| **Datadir layout** | `<datadir>/mdbx.dat` + standard freezer | `<datadir>/chaindata/mdbx.dat` + `<datadir>/chain/freezer` + optional snapshot dir (note the `chaindata/` subdir — see [docs/ethel/eth-el-datadir-layout.md](ethel/eth-el-datadir-layout.md)) |
| **Sync** | OtterSync (BitTorrent EraE), 5 modes: full / snap / checkpoint / backfill / staged | 3-mode snapshot distribution: minimal / full / archive + delta catch-up + external CL push payload |
| **Storage size** | ~50 GB hot + tiered cold | minimal ~80 GB / full ~250 GB / archive ~849 GB (state + history + receipts) |

Same address space, same EVM, same precompiles (modulo N42-only PQ + AI
precompiles), same transaction wire format. A program developed against
either runs against both.

### Why dual-mode

- **N42 native** is the high-throughput, instant-finality L1 with mobile-class
  validators. Real applications run here.
- **N42-as-EL** lets the same codebase serve as a drop-in Ethereum execution
  client. Goal: be the cheapest archive-node implementation by aggressive
  cold-data compression (RecSplit MPHF + Elias-Fano + zstd, ~10× smaller than
  geth/erigon archive) and 1-hour first-boot via snapshot distribution.

See [`docs/ethel/external-cl-runbook.md`](ethel/external-cl-runbook.md) for the
operator path to point Lighthouse / Prysm at `eth-el :20014`.

## Layer Diagram

```
cmd/n42/           -> Entry point, CLI (urfave/cli v2)
internal/           -> Core business logic (private, not importable)
  node/             -> Service orchestrator: creates & wires all components
  consensus/        -> Pluggable consensus engines (apoa/apos/hotstuff)
    hotstuff/       -> HotStuff-2 BFT (2-round, Rotor relay, DA verification)
  miner/            -> Block production and sealing
  txspool/          -> Transaction pool management
  vm/               -> EVM execution (interpreter, opcodes, gas)
  avm/              -> N42 AVM (alternative VM)
  api/              -> JSON-RPC backend implementation
  sync/             -> Chain synchronization (initial-sync, snap-sync, checkpoint)
  p2p/              -> libp2p networking with Kademlia DHT
  exex/             -> Execution Extensions (post-block plugin system)
  parallel/         -> Block-STM parallel EVM execution + dependency predictor
  deferred/         -> Deferred execution pipeline (3-stage + 5-stage deep pipeline)
  tile/             -> Tile architecture (SPSC ring buffer, CPU affinity, crash recovery)
  stateless/        -> Stateless block validation (witness-based, code cache)
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
  jmt/              -> Jellyfish Merkle Tree (Blake3, 16-ary, ref-counting GC)
    archive/        -> Haystack JMT node compression (seg + RecSplit index)
    store/          -> Node stores (MDBX, PooledDB, Lazy, Cached, Mem)
  lthash/           -> LtHash lattice state digest (BLAKE3 XOF 2048-byte homomorphic)
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

| Port  | Purpose                                                  |
|-------|----------------------------------------------------------|
| 61015 | P2P Discovery (UDP) — N42 native                         |
| 61016 | P2P Communication (TCP) — N42 native                     |
| 20012 | JSON-RPC HTTP                                            |
| 20013 | JSON-RPC WebSocket                                       |
| 20014 | Authenticated RPC (JWT) — Engine API (`cmd/eth-el`, `cmd/n42`) |
| 9000  | Caplin sentinel libp2p TCP + discv5 UDP (when wired)     |
| 5052  | External CL HTTP (Lighthouse / Prysm — external process) |
| 6060  | pprof metrics                                            |
| 8553  | MCP Server (AI agents)                                   |
| 8554  | Message Stream (SSE)                                     |
| 9090  | gRPC KV (RPCDaemon)                                      |

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
- Provides the storage/index foundation for historical JMT proof serving from compressed archives

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

## High-Performance Pipeline

N42 implements a multi-stage block processing pipeline with lock-free IPC:

```
Block N:   [Consensus]     Leader + Rotor relay
Block N-1: [Prefetch]      Async I/O + predicted slots
Block N-2: [Execution]     Block-STM parallel EVM
Block N-3: [Commitment]    JMT + LtHash state root
Block N-4: [Persistence]   MDBX write transaction
```

### Tile Architecture (`internal/tile/`)

Each pipeline stage runs as a **Tile** — a long-lived goroutine with:
- **SPSC ring buffer** (`RingBuffer`): Lamport queue with cache-line-padded
  head/tail pointers. Power-of-2 capacity with bitwise masking. ~50ns/op.
- **CPU affinity**: Optional core pinning via `runtime.LockOSThread()` +
  `unix.SchedSetaffinity()` (Linux only, no-op on other platforms).
- **Crash recovery**: Auto-restart on panic with configurable max restarts
  and cancellable restart delay.
- **TileManager**: Orchestrates tile creation, wiring, start/stop, and
  reorg recovery (`Reset()`).

### LtHash State Digest (`lib/lthash/`)

Homomorphic hash for O(k) incremental state verification:

```
newDigest = oldDigest ⊕ BLAKE3_XOF(newAccountData) ⊕ BLAKE3_XOF(oldAccountData)
```

- 2048-byte digest via BLAKE3 XOF mode (128-bit collision security)
- Runs alongside JMT — JMT provides Merkle proofs, LtHash provides fast
  full-validator verification
- 32-byte summary (`BLAKE3(digest[:])`) stored in `Header.LtHashRoot`
- Fork-gated via `LtHashTime` timestamp

### Mobile Verification Nodes (`cmd/evmsdk`)

N42's consensus design is built around **smartphones-as-validators**: a
phone with intermittent connectivity, ~4 GB RAM and no MDBX install can
verify the chain head without re-executing the EVM.

How it works:

1. **BLS-aggregated CommitQC per block** — the HotStuff-2 QC over a block
   is a single 48-byte BLS12-381 G2 aggregate signature of the validator
   committee (vs N×96 B for individual sigs). Verification = one pairing,
   ~1.5 ms on ARMv8.
2. **Header carries the proof** — `Header.Extra` packs the QC + 4 tree
   roots (JMT state, BMT, MPT, LtHash) + mobile BLS payload. ~114 B
   fixed overhead per block. See `memory project_header_extra_layout.md`.
3. **Witness streaming v1** — for blocks the mobile cares about, the
   server streams a length-prefixed Merkle witness; mobile checks
   inclusion against the header's tree root. No full state DB required
   (~10 GB code cache at most).
4. **Committee rotation + immediate exit** — APoS rotates the BLS
   committee per epoch with a short exit window so a compromised key
   has bounded blast radius.
5. **Mobile SDK** — `cmd/evmsdk` ships as `.aar` (Android) / `.xcframework`
   (iOS) via gomobile. The N42 mobile wallet (Flutter / `n42appv2`) drives
   it through `com.mobileSdk.Api` — **production pipeline, in use ~3 years**,
   do not refactor casually (see `memory feedback_evmsdk_is_verifier.md`).

Companion design notes:
- `docs/mobile/parallel-verification.md` — multi-node mobile verification
- BLS sizes: 48 B per aggregate, ~3 % body-overhead post-aggregation
  (`memory reference_signature_sizes.md`)

### Rotor Single-Hop Relay (`internal/consensus/hotstuff/rotor.go`)

Deterministic relay-based block propagation:
1. Leader computes relay assignments via `SHA256(view_number)` → Fisher-Yates shuffle
2. All validators independently compute the same assignments
3. Leader sends proposal directly to k relay nodes via libp2p streams
4. Each relay forwards to its assigned subset of validators
5. GossipSub broadcast as safety net for liveness

### Predictive Prefetching

Two-phase prefetch architecture:
1. **Static analysis**: Parse transaction access lists, sender/recipient accounts
2. **Learned prediction**: `PrefetchPredictor` tracks hot storage slots per contract
   from SLOAD execution patterns, predicts top-N slots for next block
3. **Async dispatch**: Request generators → bounded channel → I/O worker pool
4. **Periodic decay**: Every 100 blocks, reduces old access counts (factor 0.9)

## Ethereum EL/CL Compatibility

`cmd/eth-el` is N42 reconfigured as a standards-compliant Ethereum
execution client driven by an external CL (Lighthouse / Prysm) over the
Engine API. The same MDBX + EVM + state machine that runs the N42 native
chain serves Ethereum mainnet blocks.

### Components

```
cmd/eth-el/                    binary (build tag n42el adds embedded Caplin wire)
  main.go                      service registration order: bootstrap → catchup → engineAPI
  beacon_wire.go               Caplin wire (n42el tag); WARN log because Phase 6 stub
internal/ethel/                eth-mode service framework
  bootstrap/                   one-shot leaves-journal → PlainState init
  catchup/                     forward replay from a stored progress marker
  engineapi/                   auth-RPC HTTP listener + JWT (HS256, Engine API §3.1)
  snapshotprestart/            pre-start snapshot status + AutoFetch + delta catch-up
  fetch/                       HTTP + Torrent + WebRTC fetchers (shared by bootstrap/catchup)
internal/api/                  REUSED from N42 native — the Engine API impls
  engine_api_v1.go             engine_newPayloadV1/V2 + forkchoiceUpdatedV1/V2 (real)
  engine_api_blob.go           engine_newPayloadV3 + forkchoiceUpdatedV3 (real, Cancun blobs)
  engine_api_v4.go             engine_newPayloadV4 + forkchoiceUpdatedV4 (real, Pectra)
  engine_state_adapter.go      REAL EVM execute + state root verify + MDBX commit
internal/cl/                   Caplin (own beacon node) — Phase 6 stub; sentinel/stage loop
                               NOT wired. Use external CL until Phase 7+
cmd/ethexec                    bring-up tool: witness/leaves freezer replay, rebuild-state
cmd/n42-eth-snapshot           snapshot client: status, fetch, catchup (delta), follow
cmd/n42-eth-manifest           publisher manifest tool
cmd/n42-eth-publish            mirror/publisher pipeline
cmd/reth-snapshot-export       MDBX → RecSplit MPHF + Elias-Fano + zstd snapshot writer
```

### Engine API path (external CL → eth-el)

```
[Lighthouse / Prysm beacon node]
     │ engine_newPayloadV3 (+ withdrawals, blobs, requests)
     │ engine_forkchoiceUpdatedV3
     │ (HS256-JWT auth, port 20014)
     ▼
[internal/ethel/engineapi/service.go]
     │ JWT verify → dispatch to EngineAPIv4 / EngineAPIBlob / EngineAPIV1
     ▼
[internal/api/engine_state_adapter.go]
     │ BeginRw → ApplyTransaction (real EVM)
     │ ProcessExecutionBlockStart/End → withdrawals → requests hash
     │ VerifyStateRoot (HPH, byte-compatible mainnet)
     │ WriteChangeSets → WriteHistory → tx.Commit
     ▼
[chaindata/mdbx.dat + chain/freezer]
```

All `engine_*` methods are real implementations (NOT eladapter stubs).
External CL at `--execution-endpoint=http://127.0.0.1:20014` works
today; see [`docs/ethel/external-cl-runbook.md`](ethel/external-cl-runbook.md).

### Bring-up modes

| Mode | What you have | Setup time | Catch-up to tip |
|---|---|---|---|
| **minimal** | chaindata only (~80 GB) | snapshot fetch ~20 min | yes via Engine API |
| **full** | chaindata + recent freezer (~250 GB) | snapshot fetch ~1 h | yes + last-week reorg lookback |
| **archive** | chaindata + full freezer (~849 GB) | snapshot fetch ~3-4 h | yes + full historical queries |
| **rebuild** | witness freezer only | replay + rebuild ~hours-day | offline → then live |

`bootstrap/service.go` detects `HasPopulatedState` and short-circuits when
the snapshot has already populated `Account`. Then catchup picks up from
`SyncStageProgress/ethel-last-block` (authoritative, per-batch marker).

### Catch-up strategy selection (G4)

`internal/ethel/snapshotprestart/strategy.go` picks one of:

| Strategy | When | Action |
|---|---|---|
| `none` | gap == 0 | up-to-date, return |
| `delta` | gap inside publisher delta retention | apply chained deltas from mirror (cheap) |
| `libp2p` | gap > delta window, peers available | backfill blocks from libp2p |
| `fetch` | gap > everything | error with operator hint — `n42-eth-snapshot fetch` explicitly |

First-boot empty datadir + `--snapshot.auto-fetch` → runs `snapshot.Fetch`
against publisher's latest before delta catch-up (default off; multi-GB
download must be opt-in).

## Data Compression & Fast Sync

The eth-EL mode targets a **~10× smaller archive footprint** than reference
clients via cold-data RecSplit perfect hashing + Elias-Fano offsets + zstd
value compression. Same approach lets a new node reach tip in ~1 hour of
fetch + ~1 hour of catch-up instead of geth's days.

### Cold-data file layout

```
<datadir>/
  chaindata/mdbx.dat         hot state — recent N blocks, PlainState, account/storage
  chain/freezer/             cold append-only flat files (per-month rotation)
    headerc.cidx + .NNNN.cdat     column-stripped headers (~9 bytes/header)
    bodies.cidx + .NNNN.cdat      tx data (compact + zstd batched)
    receipts.cidx + .NNNN.cdat
    senders.cidx + .NNNN.cdat     sender recovery cache (one-shot, 38 B/tx)
    codes.cidx + .NNNN.cdat       contract bytecode dedup'd by keccak(value)
    acctcs.cidx + .NNNN.cdat      Erigon V2 account changeset per block
    storcs.cidx + .NNNN.cdat      Erigon V2 storage changeset per block (DupSort)
    witness.cidx + .NNNN.cdat     stream/length-prefixed v1 witnesses (replay)
  snapshot/                  RecSplit-indexed cold-state snapshots (per-mode)
    accounts.{0-H}.{idx,ef,val.zst,codedict}
    storage.{0-H}.{idx,ef,val.zst}
    accounts.{k*1M}-{(k+1)*1M-1}.* (H.3b-full TODO: per-segment files)
  manifest-{minimal,full,archive}.json    blake2b256 + size per file (CAS)
```

Authority on table semantics: [`docs/ethel/freezer-tables.md`](ethel/freezer-tables.md).

### RecSplit + Elias-Fano + zstd

For ~24 GB accounts / ~126 GB storage post-import, the snapshot tier uses:

- **RecSplit MPHF** (~1.8 bit/key): minimal perfect hash maps every key to
  a dense ordinal in [0, N)
- **Elias-Fano** monotonic sequence: ordinal → byte offset in `.val`
- **Per-entry XXH64-lo32 fingerprint** (4 B): rejects phantom-key lookups
  (since MPHF returns *some* ordinal for unknown keys); on-chain commitment
  still gates tampering
- **zstd-compressed values**: post-build sweep produces `.val.zst`
- **Codedict** (accounts only): 32 B `codeHash` → 3 B dictionary id; 2.22 M
  unique hashes cover 73.9 M contracts → saves 29 B / contract

Net effect (validated): account snapshot ~80 % smaller than raw MDBX
PlainAccountState rows; storage ~70 % smaller.

### Erigon V2 changeset encoding

`acctcs` / `storcs` use Erigon V2 codec (NOT RLP / SSZ / protobuf) for
per-block changesets. Critical for two-way state reconstruction:
- forward: changeset + previous state → next state (`RebuildState`)
- backward: changeset + next state → previous state (`Reorg`)

Decision rationale: `memory feedback_erigon_v2_codec.md`.

### Snapshot distribution & deltas

| Subsystem | Path | Status |
|---|---|---|
| 3-mode publisher | `n42-eth-publish` → mirror at `<root>/<network>/{height}/{mode}/` | ✓ |
| Manifest format | blake2b256 + size per file, glob selector for per-mode files | ✓ ([selector glob covers segmented files](ethel/n42-eth-snapshot-segments.md)) |
| Client status | `n42-eth-snapshot status --datadir --source --mode` | ✓ |
| Client fetch | `n42-eth-snapshot fetch ...` — atomic tmp+rename, parallel workers, blake2b verify | ✓ |
| Delta apply | `n42-eth-snapshot catchup ...` — apply chained deltas to current manifest_id | ✓ |
| eth-el AutoFetch | `--snapshot.auto-fetch` — first-boot empty datadir runs initial fetch + delta | ✓ |
| Segmented snapshot writer | H.3b-mini: `reth-snapshot-export --end-block H` spec-named files | ✓ |
| Per-1M-block segments | H.3b-full: real per-segment writer using reth `AccountChangeSets` | TODO (1-2 wk) |
| Delta payload size | per-week delta with H.3b-full ≈ ~2 GB / 0.24 % of archive | targeted |

Design docs:
- [`docs/ethel/n42-eth-client-distribution.md`](ethel/n42-eth-client-distribution.md) — 3-mode spec
- [`docs/ethel/n42-eth-delta-updates.md`](ethel/n42-eth-delta-updates.md) — Phase H delta design
- [`docs/ethel/n42-eth-snapshot-segments.md`](ethel/n42-eth-snapshot-segments.md) — H.3 segmentation
- [`docs/ethel/client-server-sync.md`](ethel/client-server-sync.md) — end-to-end flow

### Sync timeline (mainnet, full mode)

```
T+0       operator runs:  eth-el --datadir D:/n42-eth --bootstrap.enabled=false
                          --snapshot.source=https://mirror.example.com/mainnet
                          --snapshot.mode=full --snapshot.auto-fetch
                          --engine.enabled --engine.jwt=D:/jwt.hex
T+0-3 min publisher releases.json read; latest height + manifest_id resolved
T+5-60 min snapshot.Fetch downloads ~250 GB minus what's already on disk;
          blake2b verified per file, atomic rename to final path
T+60-65   snapshot.Status re-check; delta catchup if mirror rolled forward
          during fetch (typically a few hundred MB)
T+65 min  bootstrap.Service sees HasPopulatedState=true → skip RebuildState
T+65 min  catchup.Service reads SyncStageProgress → at snapshot tip
T+65 min  engineapi service binds :20014, waits for CL push
T+x min   Lighthouse running in parallel completes checkpoint sync
          and starts pushing engine_newPayloadV3 for blocks since snapshot
T+x+1h    catch-up to mainnet tip (~3-5 s EVM per block × N blocks)
T+x+1h+   12-second slot live loop
```

Reality check 2026-05-23 (D:/N42-eth1177 with 25.1 M blocks already at
hand): `cmd/eth-el` opens chaindata, finds `SyncStageProgress=25,101,866`,
binds Engine API in seconds. Caplin Phase 6 stub is correctly NOT relied on.
