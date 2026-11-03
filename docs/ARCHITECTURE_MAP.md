# N42 Architecture

Moved out of CLAUDE.md on 2026-10-08 to keep the per-turn context small. Layer map and key patterns of the node.

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
cmd/n42-datc/     → thin wrapper around internal/datc (build/verify/proof/bench/reframe/derive-ns/verify-ns ...)
internal/datc/    → DATC: EIP-1186 proofs at ANY height (the eth-el archive-plus tier). Builder, offline derivation,
                     reader library (archive.go: OpenArchive/Prove, served by eth-el via --publicrpc.datc).
                     Read docs/ethel/datc-archive-plus.md first. Correctness harness:
                     go test -tags "nosqlite,noboltdb" ./internal/datc/ -run TestE2E
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

