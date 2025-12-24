# Developers

N42 is composed of several packages that can be used in standalone projects. If you are interested in using one or more of the packages, you can get an overview of them in the developer docs.

## Architecture

N42 follows a modular architecture with the following main components:

- **Consensus**: APoS/APoA consensus implementation
- **EVM**: Ethereum Virtual Machine execution
- **Storage**: MDBX-based persistent storage
- **Network**: P2P networking with libp2p
- **RPC**: JSON-RPC API server

## Key Packages

| Package | Description |
|---------|-------------|
| `cmd/n42` | Main binary and CLI |
| `common` | Core blockchain primitives |
| `internal/consensus` | Consensus mechanisms |
| `internal/vm` | EVM execution |
| `modules/rawdb` | Database layer |
| `internal/p2p` | P2P networking |
| `internal/api` | RPC server |

## Getting Started

```go
// Example: Using N42 packages
import (
    "github.com/n42blockchain/N42/common/types"
    "github.com/n42blockchain/N42/common/block"
)

// Example: Create a new block header
header := &block.Header{
    Number:     uint256.NewInt(12345),
    ParentHash: types.Hash{...},
}
```

## Resources

- [API Documentation](https://docs.n42.io)
- [GitHub Repository](https://github.com/n42blockchain/N42)
- [Contributing Guide](./contribute.md)
