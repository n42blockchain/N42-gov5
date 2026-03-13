# N42 Blockchain

[![Go](https://img.shields.io/badge/go-1.21%2B-blue.svg)](https://golang.org)
[![GitHub Workflow Status](https://img.shields.io/github/actions/workflow/status/n42blockchain/n42/ci.yml?branch=main)](https://github.com/n42blockchain/n42/actions)
[![GitHub License](https://img.shields.io/github/license/n42blockchain/n42)](https://github.com/n42blockchain/n42/blob/main/LICENSE)
[![GitHub Issues](https://img.shields.io/github/issues/n42blockchain/n42)](https://github.com/n42blockchain/n42/issues)
[![GitHub Pull Requests](https://img.shields.io/github/issues-pr/n42blockchain/n42)](https://github.com/n42blockchain/n42/pulls)
[![GitHub Stars](https://img.shields.io/github/stars/n42blockchain/n42)](https://github.com/n42blockchain/n42/stargazers)
[![GitHub Forks](https://img.shields.io/github/forks/n42blockchain/n42)](https://github.com/n42blockchain/n42/network/members)

## Introduction

N42 establishes a secure, efficient, and globally connected digital ecosystem, giving developers unparalleled freedom and seamless interoperability for building applications. As a high-performance public blockchain, N42 is built using Go, leveraging its superior concurrency, scalability, and deployment simplicity to deliver a resilient and highly efficient infrastructure.

Featuring a modular, sharded architecture, N42 delivers high transaction throughput and advanced data processing — key elements for building globally connected digital infrastructure. Its permissionless framework enables effortless integration and efficient data exchange across a wide range of applications, paving the way for the next generation of decentralized internet services.

**Disclaimer:** This software is currently a tech preview. We will do our best to keep it stable and avoid breaking changes, but we make no guarantees.

## Key Features

### Zero-Knowledge Proof System

- **ZISK zkVM Fast-Path Proving**: Block producers generate ZK proofs after execution; validators verify proofs in milliseconds without re-executing the EVM, drastically reducing hardware requirements
- **RISC-V64 Guest Program**: Standalone EVM execution binary (`cmd/zkguest`) compiled to RISC-V64 for zkVM proving, with pure Go EVM core (no CGO)
- **gRPC Prover Integration**: External ZISK prover cluster connection via gRPC, with configurable concurrency, timeouts, and proof types (STARK/SNARK)
- **Soft Verification Mode**: Blocks with proofs are verified via ZK; blocks without proofs fall back to normal EVM execution (configurable to enforce proofs)
- **Block Witness Infrastructure**: TracingReader-based witness capture during block production, binary encoding, LRU caching, and P2P witness request protocol

### Consensus & Execution

- **High-Performance EVM**: Block-STM parallel transaction execution with multi-version state
- **Pluggable Consensus**: Authority PoA (`apoa`) and Authority PoS (`apos`) engines
- **EOF (EVM Object Format)**: Full support including EOFCREATE, RETURNCONTRACT, and sub-containers
- **Blob Transactions (EIP-4844)**: Native support for blob-carrying transactions
- **MEV Infrastructure**: Bundle pool and priority-based transaction ordering for block builders
- **State Prefetching**: Predictive state loading for sender/recipient/access-list entries

### State & Storage

- **Jellyfish Merkle Tree (JMT)**: Blake3-hashed state commitment with Merkle proofs, replacing legacy incremental Keccak
- **MDBX Storage**: Memory-mapped B+ tree database with layered caching
- **Ancient/Freezer DB**: Automatic archival of historical data beyond a configurable threshold
- **Flat Snapshot Acceleration**: In-memory snapshot tree with diff layers for fast state reads

### Networking & Sync

- **Snap Sync**: Fast state synchronization with parallel downloading and verification
- **P2P Networking**: libp2p-based with Kademlia DHT, peer scoring, and rate limiting
- **ExEx Extensions**: Execution Extension framework for pluggable post-block processing

### API & Tooling

- **Comprehensive JSON-RPC**: Full Ethereum JSON-RPC including `eth_getProof`, `eth_createAccessList`, `debug_*`, and ZK proof endpoints (`zk_getBlockZKProof`, `zk_verifyBlockZKProof`, `zk_getProofStatus`)
- **Witness RPC**: `eth_getBlockWitness` for retrieving block execution witnesses
- **MCP Server**: Model Context Protocol server for AI-assisted blockchain interaction
- **Chain Import/Export**: Length-prefixed protobuf format for offline block data transfer
- **Database Inspector**: CLI tools for database stats, key inspection, and state dumps

## Architecture

```
cmd/
  n42/              Main entry point and CLI commands
  zkguest/          RISC-V64 ZK guest program (standalone EVM for zkVM)
internal/
  consensus/        Pluggable consensus engines (apoa/, apos/)
  miner/            Block production with MEV bundle support and witness capture
  txspool/          Transaction pool with persistence
  vm/               EVM execution engine with EOF support
  avm/              N42 AVM (alternative VM)
  sync/             Chain synchronization (snap sync, initial sync, witness P2P)
  api/              JSON-RPC backend (eth, debug, zk, witness endpoints)
  zkprover/         ZK prover service (gRPC client, input builder, guest program)
  zkverifier/       ZK proof verifier (STARK/SNARK verification)
  mcp/              Model Context Protocol server
modules/
  state/            State management with JMT commitment and witness generation
  rawdb/            Raw database operations (MDBX + freezer)
lib/
  jmt/              Jellyfish Merkle Tree implementation (Blake3, 16-ary)
  kv/               Key-value store interfaces (mdbx/, memdb/)
conf/               Node configuration (P2P, RPC, consensus, ZK prover settings)
```

## System Requirements

- **Storage**: >= 200 GB (SSD or NVMe recommended; HDD not recommended)
- **Memory**: >= 16 GB RAM
- **CPU**: 64-bit architecture
- **Go Version**: [>= 1.21](https://golang.org/doc/install)

Current build/test environment: `go1.26.1`

## Building from Source

### Linux and macOS

To build N42 from source, you must have the latest version of Go installed.

- Installation instructions: [Go installation page](https://golang.org/doc/install)
- Download Go: [Go download page](https://golang.org/dl/)

Clone the repository and compile:

```sh
git clone https://github.com/n42blockchain/n42.git
cd n42
make n42
./build/bin/n42
```

Build the ZK guest program (requires no CGO):

```sh
make zkguest
# Output: build/bin/zkguest (linux/riscv64 ELF binary)
```

### Windows

Windows users may run N42 in three ways:

- **Native binaries**: Build using [Chocolatey](https://chocolatey.org/)
- **Docker**: See [docker-compose.yml](./docker-compose.yml)
- **WSL2 (Windows Subsystem for Linux)**:
    - Install and build as on Linux
    - Ensure storage is on Linux filesystem for best performance

### Docker Container

Docker allows easy building and running without installing dependencies on the host OS.

See: [docker-compose.yml](./docker-compose.yml), [Dockerfile](./Dockerfile)

Convenient Docker commands:

```sh
make images # Build Docker images containing N42 binaries
make up     # docker-compose up -d && docker-compose logs -f
make down   # docker-compose down && clean docker data
make start  # docker-compose start && docker-compose logs -f
make stop   # docker-compose stop
```

## Executables

N42 includes the following executables:

| Command | Description |
|---------|-------------|
| **`n42`** | Main CLI client, provides JSON RPC endpoints over HTTP transports. Use `n42 --help` for options. |
| **`n42 migrate-jmt`** | Offline migration tool to build JMT state commitment from existing database. |
| **`n42 import`** | Import blocks from a file. |
| **`n42 export`** | Export blocks to a file. |
| **`n42 db`** | Database inspection and maintenance commands. |
| **`n42 state-dump`** | Dump account state to JSON. |
| **`zkguest`** | Standalone RISC-V64 EVM binary for ZK proof generation inside zkVM. |

## Network Ports

| Port  | Protocol | Purpose                      | Exposure            |
|-------|----------|------------------------------|---------------------|
| 61015 | UDP      | Discovery v5                 | Public              |
| 61016 | TCP      | libp2p Communication         | Public              |
| 20012 | TCP      | JSON RPC over HTTP           | Public              |
| 20013 | TCP      | JSON RPC over Websocket      | Public              |
| 20014 | TCP      | Secure JSON RPC (JWT Auth)   | Authenticated       |
| 4000  | TCP      | Blockchain Explorer          | Public              |
| 6060  | TCP      | Metrics & Profiling (pprof)  | Private             |

## Configuration

Key configuration options in `NodeConfig`:

| Option | Type | Description |
|--------|------|-------------|
| `jmt_commitment` | `bool` | Enable JMT state commitment (requires `migrate-jmt` first) |
| `parallel_evm` | `bool` | Enable Block-STM parallel EVM execution |
| `prefetch` | `bool` | Enable state prefetching for transaction processing |
| `ancient_db` | `bool` | Enable ancient/freezer database for historical data |
| `ancient_freeze_threshold` | `uint64` | Block threshold for freezing data to ancient DB |

ZK Prover configuration (`ZKProverCfg`):

| Option | Type | Description |
|--------|------|-------------|
| `enabled` | `bool` | Enable ZK proof generation |
| `prover_addr` | `string` | gRPC address of external ZISK prover cluster |
| `prover_timeout` | `int` | Proof generation timeout in seconds (default: 600) |
| `proof_type` | `string` | Proof type: `"stark"` or `"snark"` |
| `max_concurrent` | `int` | Maximum concurrent proof generation jobs |
| `guest_binary` | `string` | Path to RISC-V64 ELF guest binary |

## Development

```sh
make build          # Compile all packages
make test           # Run all tests
make test-short     # Fast tests with -short flag
make lint           # Run golangci-lint
make check          # fmt + vet + lint
make race-core      # Race detection on core packages
make ci-full        # Full CI pipeline
make bench-smoke    # Quick benchmarks on core packages
```

## License

N42 is licensed under the [GNU General Public License v3.0](https://www.gnu.org/licenses/gpl-3.0.en.html).
