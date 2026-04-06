# N42/ETH-EL Shared Infrastructure Guide

## Architecture Overview

N42 uses a **single binary, dual-mode** architecture. One `n42` executable supports two execution profiles:

| Mode | Profile | Use Case |
|------|---------|----------|
| **N42** | `n42` | Self-sovereign chain: HotStuff-2 BFT, mobile consensus, AI agents, ZK bridge |
| **ETH EL** | `eth-el` | Standard Ethereum Execution Layer: Engine API, Hive/EEST compatible |

The mode is selected at startup via `--chain` or `--profile` flags. The same shared infrastructure (EVM, MDBX, RPC transport, crypto) serves both modes.

---

## Quick Start

```bash
# N42 mode (default)
./n42 --chain mainnet_v2 --mine --etherbase 0x...

# N42 dev mode (single node, no peers)
./n42 --dev

# ETH EL dev mode (Engine API, no peers)
./n42 --ethdev

# ETH EL with external CL (e.g., Lighthouse/Prysm)
./n42 --chain eth-sepolia --authrpc --authrpc.jwtsecret jwt.hex
```

---

## Profile System

### Files
- `params/profile.go` — Profile descriptors, capability checks
- `params/network_preset.go` — Chain-to-profile binding
- `params/networkname/network_name.go` — Chain name constants

### Available Chains

| Chain | Profile | ChainID | Consensus |
|-------|---------|---------|-----------|
| `mainnet` / `n42-mainnet` | n42 | 94 | APoS |
| `testnet` / `n42-testnet` | n42 | 1142 | APoS |
| `mainnet_v2` | n42 | 94 | APoS (all forks from genesis) |
| `eth-mainnet` | eth-el | 1 | EtHash (requires init) |
| `eth-sepolia` / `eth-testnet` | eth-el | 11155111 | EtHash (requires init) |
| `private` | either | configurable | Faker/Clique |

### Profile Capabilities

| Capability | N42 | ETH EL |
|------------|:---:|:------:|
| EVM execution | yes | yes |
| Engine API (v1-v4) | yes | yes |
| JSON-RPC (eth, web3, net) | yes | yes |
| HotStuff-2 / APoS consensus | yes | no |
| AI agent runtime | yes | no |
| Cross-chain bridge | yes | no |
| Distributed compute | yes | no |
| MCP server | yes | no |
| ZK proof API | yes | no |
| Hive/EEST tests | limited | full |

### Checking Profile in Code
```go
profile := params.ResolveExecutionProfile(chainName, profileStr)
if profile.IsN42() { /* N42-only logic */ }
if profile.IsEthereumEL() { /* ETH-only logic */ }
if profile.SupportsAIRuntime() { /* gates AI services */ }
```

---

## Module Ownership

### Shared (both profiles use)
```
cmd/n42/              CLI entry point
common/block/         Block/Header (21-field ETH Pectra standard)
common/transaction/   TX types + RLP encoding
common/types/         Address, Hash, core types
common/hash/          Keccak256, DeriveSha
internal/vm/          EVM (standard precompiles)
internal/api/         JSON-RPC + Engine API
internal/state_processor.go  Block execution
modules/rawdb/        Raw DB accessors
modules/state/        IntraBlockState
lib/kv/               MDBX key-value store
lib/rlp/              RLP encoding
params/               Chain config, profile, forks
conf/                 Node configuration
crypto/               secp256k1, BLS, keccak
```

### N42 Only (do NOT modify for ETH EL work)
```
internal/consensus/hotstuff/   HotStuff-2 BFT engine
internal/consensus/apos/       APoS consensus engine
internal/ai/                   AI agent infrastructure
internal/bridge/               ZK cross-chain bridge
internal/distributed/          Distributed compute/messaging
internal/replay/               Chain replay engine
internal/mcp/                  MCP server
internal/mev/                  MEV optimization
internal/zkprover/             ZK proving
internal/zkverifier/           ZK verification
internal/bundler/              ERC-4337 bundler
internal/peerdas/              PeerDAS sampling
lib/bmt/                       Binary Merkle Tree (Blake3)
lib/jmt/                       Jellyfish Merkle Tree
lib/commitment/                HexPatriciaHashed (Erigon HPH)
lib/trie/                      CalcTrieRoot (erigon2.7)
modules/rawdb/consensus_evidence.go  ConsensusEvidence table
contracts/                     N42 smart contracts
```

### ETH EL Development Areas (for ETH EL team)
```
internal/api/engine_api_*.go   Engine API (already done, extend as needed)
internal/api/engine_mpt.go     MPT state adapter (stub, needs completion)
internal/api/engine_overlay.go State overlay for payload execution
common/transaction/ethereum_rlp.go  Standard ETH RLP (done)
conf/genesis_hive.go           Hive test integration (done)
params/eth_chain_config.go     ETH chain definitions (done)
# TODO by ETH EL team:
internal/sync/devp2p/          devp2p RLPx protocol (not started)
internal/sync/snap/            snap/1 sync protocol (not started)
internal/sync/eth68/           eth/68 wire protocol (not started)
```

---

## Decoupling Principles

### 1. Header Structure is Frozen
`common/block/header.go` has 21 ETH Pectra standard fields. **Do not add N42-specific fields.**
N42 extensions (QC, tree roots, mobile BLS) go in MDBX tables:
- `ConsensusEvidence` — APoS/HotStuff seal and signer data
- `HotStuffState` — Consensus persistence
- `BMTNode`, `JMTNode` — Tree commitment nodes

### 2. Header.Hash() is ETH Standard
`Hash() = keccak256(rlp(fields))` — same as go-ethereum. Do not change the algorithm.

### 3. Extra Field
`Header.Extra` = 32 bytes vanity (ETH standard). No consensus data in Extra.

### 4. Precompile Address Ranges
| Range | Owner |
|-------|-------|
| `0x01-0x0A` | ETH standard (ecrecover, sha256, etc.) |
| `0x0B-0x13` | ETH future (point evaluation, etc.) |
| `0x14-0x17` | N42 post-quantum (PQ isolation) |
| `0x0300-0x0302` | N42 extensions (CAS, randomness, AI inference) |

### 5. Consensus Interface Extension
Do NOT add N42-specific methods to shared interfaces (`consensus.Engine`, `consensus.ChainHeaderReader`).
Instead, use type assertion:
```go
if n42Reader, ok := chain.(consensus.N42ChainHeaderReader); ok {
    // N42-specific logic
}
```

### 6. State Commitment
| Mode | State Root | Implementation |
|------|-----------|----------------|
| N42 | MPT (HPH incremental) | `lib/commitment/hex_patricia_hashed.go` |
| ETH EL | MPT (standard) | `internal/api/engine_mpt.go` (stub) |

Both produce standard Ethereum MPT state roots. The implementations differ in incremental optimization but the output is identical.

---

## Testing Conventions

| Mode | Test Framework | Command |
|------|---------------|---------|
| N42 | `go test` + replay verification | `go test ./internal/consensus/hotstuff/... -v` |
| ETH EL | Hive / EEST | `./scripts/run_eest_local.sh` |
| Shared | `go test` | `go test ./internal/vm/... ./common/... ./params/...` |

### Replay Verification (N42)
```bash
# Full chain replay (11.9M blocks, ~1h)
./n42 replay-v2 --source <old-chain> --target <new-dir> --chain mainnet_v2 --tree mpt --batch 100000

# Cross-verify MPT roots
./n42 rebuild-mpt --datadir <dir>
./n42 rebuild-trie --datadir <dir>
```

---

## Branch Strategy

- `main` — Shared branch, both teams commit
- `feat/n42-*` — N42 feature branches (HotStuff, AI, bridge, etc.)
- `feat/eth-el-*` — ETH EL feature branches (devp2p, snap sync, etc.)
- `codex/*` — Colleague's ETH EL work branches (cherry-pick, don't merge wholesale)

### Merge Rules
1. Changes to shared modules require review from both teams
2. N42-only changes (hotstuff/, ai/, bridge/) can merge directly
3. ETH-only changes (devp2p/, snap/) can merge directly
4. Profile system changes (`params/profile.go`) require both teams

---

## Build

```bash
# Single binary for both modes
make n42              # → build/bin/n42

# Tests
make test             # All tests
make test-short       # Fast tests
go test ./params/...  # Profile system tests

# Build tags (always applied)
# nosqlite,noboltdb — MDBX only
```

CGO is required (MDBX backend).

---

## Key Contacts & References

| Topic | Resource |
|-------|----------|
| Architecture design | `docs/engineering/ETH_EL_FEASIBILITY_AND_ARCHITECTURE.md` |
| Phase roadmap | `docs/engineering/ETH_EL_FOUNDATION_CHECKLIST.md` |
| Module ownership | `docs/engineering/ETH_EL_PROFILE_BOUNDARY_INVENTORY.md` |
| Codex review | `docs/CODEX_REVIEW.md` |
| N42 testnet guide | `docs/LOCAL_TESTNET.md` |
| Header format | `common/block/header.go` (21-field ETH Pectra) |
| Profile system | `params/profile.go` + `params/network_preset.go` |
