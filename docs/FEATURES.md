# N42 Feature Reference

Complete feature list organized by subsystem. Each feature includes description, configuration, and activation mechanism.

---

## Consensus

### HotStuff-2 BFT
Two-round optimistic BFT consensus with instant finality. Uses BLS12-381 aggregate signatures for compact quorum certificates. Adaptive pacemaker handles view timeouts with exponential backoff. Dynamic validator reconfiguration via epoch transitions. 7-node chaos testing validated.

- **Config**: `consensus: hotstuff` in chain spec
- **Activation**: Genesis configuration

### Rotor Single-Hop Relay
Deterministic relay-based block propagation that reduces consensus latency compared to multi-layer GossipSub trees. The leader sends proposals directly to a small set of stake-weighted relay nodes (default 3), who each forward to their assigned subset of validators. Relay selection uses SHA256(view_number) so all validators agree on assignments without communication. Includes validator-to-peerID registry for direct libp2p stream messaging. Falls back to gossip broadcast on direct send failure for liveness.

- **Config**: Enabled by default when HotStuff is active
- **Relay count**: 3 (configurable)
- **Files**: `internal/consensus/hotstuff/rotor.go`

### On-Chain Randomness Beacon (0x0302)
VRF-style randomness derived from CommitQC BLS aggregate signatures. Since the aggregate requires 2f+1 signers, no single validator can predict the output (threshold VUF). Smart contracts access per-block randomness via precompile at address `0x0302` with selectors for `getRandom`, `getRandomInRange`, and `getRandomWithSeed`.

- **Config**: `randomnessTime` timestamp in chain spec
- **Activation**: Fork-gated via `IsRandomness(time)`
- **Gas**: 10,000 base + input-dependent

### Baby Raptr DA Verification
Data availability commitment in HotStuff proposals. Each proposal includes a `TxRootHash` — the transaction root of the proposed block. When the block is imported after execution, the actual transaction root is compared against the committed value. Mismatches return a `DAVerificationError`. Bounded by `MaxImportedBlocks` (64) to prevent memory growth.

- **Files**: `internal/consensus/hotstuff/proposal.go`

### Authority PoS (APoS)
Epoch-based validator rotation with tiered staking (token, NFT, testnet). Checkpoint snapshots every 3000 blocks. Post-quantum STARK verification path.

### Authority PoA (APoA)
Lightweight proof-of-authority for private networks and development environments.

### Mobile Verification
Lightweight mobile nodes verify block state via BLS-signed attestations without re-executing EVM. Enables smartphones as consensus participants.

---

## Execution

### Block-STM Parallel EVM
Optimistic parallel transaction execution with multi-version state (MVS). Wave-based validation detects read-write conflicts and re-executes. 3.9x speedup on independent workloads. Falls back to sequential for ≤4 transactions.

### Dependency Prediction
Pre-execution transaction reordering based on contract address + method selector grouping. Clusters likely-conflicting transactions together for better Block-STM wave efficiency. Learns from historical execution patterns.

- **Files**: `internal/parallel/predictor.go`

### Predicted Executor
Wraps the Block-STM executor with prediction-based transaction reordering. Falls back to identity order for small batches (≤4 txs) where reordering overhead exceeds benefit.

- **Files**: `internal/parallel/predicted_executor.go`

### EIP-7904 Glamsterdam Gas Repricing
Simple transfers drop from 21,000 to 4,500 gas (-78.6%). Data costs reduced 75% (16/4 → 4/1 gas/byte). Contract creation reduced 75% (32,000 → 8,000 gas). Activated via timestamp fork.

- **Config**: `glamsterdamTime` timestamp in chain spec
- **Activation**: Fork-gated via `IsGlamsterdam(time)`

### Deferred Execution Pipeline
Consensus-execution separation allowing the consensus layer to agree on transaction ordering while execution proceeds asynchronously. 3-stage pipeline (Consensus → Execution → Commit) with configurable queue depth and worker count.

- **Config**: `deferred_exec: true` in node config
- **Queue size**: 64 (default)

---

## High-Performance Pipeline

### Deep Pipeline (5-Stage)
Superscalar block processing where 5 stages operate on different blocks simultaneously:

| Stage | Block | Function |
|-------|-------|----------|
| Consensus | N | Ordering agreed |
| Prefetch | N-1 | Async state warming |
| Execute | N-2 | EVM processing |
| Commit | N-3 | JMT + LtHash state root |
| Persist | N-4 | MDBX write transaction |

Channel-based backpressure with configurable depth (default 4). Halts pipeline on execution/commit error to prevent state corruption. Reorg recovery via `Reset()`.

- **Config**: `deep_pipeline: true` in deferred exec config
- **Files**: `internal/deferred/deep_pipeline.go`

### Tile Architecture
Lock-free pipeline stage communication via SPSC (Single Producer Single Consumer) ring buffers. Each tile is a long-lived goroutine with:

- **Lock-free ring buffer**: Lamport queue with cache-line-padded head/tail pointers (64-byte isolation). Power-of-2 capacity with bitwise masking. ~50ns/op vs ~65ns for Go channels.
- **CPU core pinning**: Optional via `runtime.LockOSThread()` + Linux `SchedSetaffinity()`. No-op on non-Linux.
- **Crash recovery**: Auto-restart on panic (configurable max restarts, default 3). Cancellable restart delay.
- **TileManager**: Orchestrates 5-tile pipeline (Net → Consensus → Execute → Commit → Persist).

- **Config**: `tile_enabled: true`, `tile_ring_size: 1024`, `tile_*_core: -1`
- **Files**: `internal/tile/` (7 files)

### Async I/O Prefetcher
Decouples state prefetch request generation from I/O execution. Request-generation goroutines parse transactions and submit `prefetchRequest` structs to a bounded channel. A separate I/O worker pool (NumCPU/2, min 2) drains the channel and performs actual MDBX reads, populating the shared CachedStateReader cache before EVM execution begins.

- **Files**: `internal/prefetcher.go`

### Predictive Slot Prefetching
SLOAD access recording via `SlotAccessRecorder` interface feeds `PrefetchPredictor`, which tracks the most frequently accessed storage slots per contract. Before each block, the top-N (default 8) predicted slots are prefetched alongside standard access-list and sender/recipient reads. Periodic decay (every 100 blocks, factor 0.9) adapts to workload changes. Memory bounded by `maxContracts` (8192) and `maxSlots` (64 per contract).

- **Files**: `internal/prefetch_predictor.go`, `internal/vm/instructions.go`

---

## State & Storage

### Jellyfish Merkle Tree (JMT)
Blake3-hashed 16-ary trie with three node types (Internal, Leaf, Extension). In-memory LRU node cache (131,072 entries). Reference-counting GC for online pruning. Batch update with sorted entries for path locality.

### LtHash Lattice State Digest
Homomorphic hash function where `LtHash(A ∪ B) = LtHash(A) ⊕ LtHash(B)`. Enables O(k) incremental state digest computation per block (k = changed accounts/slots), compared to O(k × depth) for Merkle tree traversal.

- **Digest**: 2048 bytes (16,384 bits) via BLAKE3 XOF mode
- **Security**: 128-bit collision resistance per output block
- **Summary**: BLAKE3(digest[:]) → 32 bytes stored in `Header.LtHashRoot`
- **Domain separation**: Tag `'A'` (0x41) for accounts, `'S'` (0x53) for storage
- **Performance**: Add 2.1μs, Update 4.3μs, 100-element batch 0.4ms
- **Config**: `ltHashTime` timestamp in chain spec
- **Files**: `lib/lthash/`, `modules/state/commitment/lthash_*.go`

### PooledDBStore
Long-lived MDBX read-only transaction for JMT node lookups, replacing the per-Get() transaction overhead of LazyDBStore. Auto-refreshes after block commits via `RefreshTx()`. Combined with the 128K-entry decoded node cache, eliminates the need for the separate CachedStore byte-level cache.

- **Files**: `lib/jmt/store/pooled_db_store.go`

### JMT Batch Flush
`BatchNodeStore` interface extends `NodeStore` with `PutBatch(entries map[Hash][]byte)` for efficient bulk writes. `Tree.FlushTo()` detects `BatchNodeStore` and uses the fast path. `SnapshotDirty()` captures dirty nodes for the deep pipeline's commitment stage without flushing. `ClearDirty()` discards unflushed mutations.

### Haystack JMT Archive
Compressed historical JMT nodes stored in seg files with RecSplit O(1) perfect hash index. Enables `eth_getProof` queries on archived state without keeping all nodes in the live MDBX database.

- **Files**: `lib/jmt/archive/`

### Storage Tiering
Configurable NVMe/HDD split for operator cost reduction (~60%):

| Tier | Data | Size | Medium |
|------|------|------|--------|
| Hot | Chaindata, tmp | ~50GB | NVMe |
| Warm | Snapshots, domain | ~300GB | NVMe/SSD |
| Cold | History, indices, freezer | ~800GB | HDD |

- **Config**: `storage_tier` section in node config
- **CLI**: `n42 migrate-tiers` for offline migration

### Stateless Validation Mode
Verify blocks using only JMT Merkle proof witnesses (~10GB disk), no full state DB required. LRU code cache (4096 entries) reduces witness size for long-lived contracts. Foundation for lightweight mobile and edge validators.

- **Config**: `stateless_enabled: true` in node config
- **Files**: `internal/stateless/`

---

## Networking & Sync

### OtterSync (BitTorrent Sync)
Export/import chain data as EraE segment files (8192 blocks/file) via BitTorrent. Shifts ~98% of initial sync from CPU to network bandwidth. Manifest with SHA256 hashes and binary search for segment lookup.

### EraE History Format
Standardized binary archive for blocks + receipts with random access via block number index. OOM-safe boundary checks on untrusted data.

### 5 Sync Modes
Full, Snap, Checkpoint, Backfill, and Staged (7-stage pipeline with forward/unwind/prune).

### Decentralized Messaging
6-layer P2P messaging platform: relay (GossipSub sharding), E2E encryption (XChaCha20-Poly1305), RLN anti-spam, persistent CAS storage, MLS group encryption, DID identity.

---

## Zero-Knowledge Proofs

### ZISK zkVM Fast-Path Proving
Block producers generate ZK proofs after execution. Validators verify proofs in milliseconds without re-executing the EVM.

### ZKML Verification
Zero-knowledge proofs of ML inference correctness. Circuit generation from model structure, execution trace capture, proof generation and verification.

### ZK Training Verification
Cryptographic proofs binding trained models to approved datasets and training processes. Governance-gated: datasets must pass ethics committee review before training proof generation.

### ZK Inference Attestation
Signed attestations for inference results with chain-of-custody validation. Multi-hop pipeline support for autonomous driving and robotics safety.

---

## AI-Native Infrastructure

### AI Agent Wallets
L1-native agent accounts with session keys (time-limited, contract-allowlisted, spend-capped), composable spending policies (rate/cap/allowlist), and gas sponsorship via paymaster.

### AI Inference Precompile (0x0301)
Smart contracts call AI models on-chain. Gas-metered with tiered verification (ZK/Optimistic/TEE).

### AI Data Governance
On-chain training data provenance with human ethics committee voting. Datasets must pass fairness, privacy, content safety, and transparency review.

### AI Block Building
AI-optimized transaction ordering with MEV detection, sandwich attack protection (fairness guard), and EWMA-based gas prediction.

### Agent Discovery
P2P agent registry with capability-based discovery, task negotiation protocol, and weighted reputation system.

---

## Security

### Post-Quantum Cryptography
Falcon, Dilithium2/3, SQIsign precompiles (isolated activation via `PQPrecompilesTime`).

### Encrypted Mempool
Transaction privacy before block inclusion.

---

## Configuration Reference

### Fork Timestamps

| Fork | Config Key | Description |
|------|-----------|-------------|
| Glamsterdam | `glamsterdamTime` | EIP-7904 gas repricing |
| Randomness | `randomnessTime` | On-chain randomness precompile |
| AI Inference | `aiInferenceTime` | AI inference precompile |
| Content Store | `contentStoreTime` | CAS precompile |
| LtHash | `ltHashTime` | Lattice state digest |
| PQ Precompiles | `pqPrecompilesTime` | Post-quantum cryptography |

### Node Configuration

| Config | Default | Description |
|--------|---------|-------------|
| `jmt_commitment` | `false` | Enable JMT state commitment |
| `parallel_evm` | `false` | Enable Block-STM parallel execution |
| `prefetch` | `false` | Enable state prefetching |
| `stateless_enabled` | `false` | Enable stateless validation mode |
| `tile_enabled` | `false` | Enable tile-based pipeline |
| `tile_ring_size` | `1024` | SPSC ring buffer capacity |
| `deep_pipeline` | `false` | Enable 5-stage deep pipeline |
| `deferred_exec` | `false` | Enable deferred execution |
| `ancient_db` | `false` | Enable ancient/freezer database |
