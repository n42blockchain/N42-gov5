# ETH/69 Protocol Implementation

This package implements the eth/69 protocol as specified in [EIP-7642](https://eips.ethereum.org/EIPS/eip-7642).

## Overview

The eth/69 protocol introduces several improvements over eth/68:

1. **History Expiry Awareness**: Nodes can now announce which historical blocks they have available
2. **Simpler Receipts**: Bloom filters removed from receipt messages to reduce bandwidth
3. **Block Range Tracking**: New `BlockRangeUpdate` message for announcing available block ranges

## Key Changes from eth/68

### Status Message (0x00)

**eth/68**:
```
[version, networkid, td, blockhash, genesis, forkid]
```

**eth/69**:
```
[version, networkid, genesis, forkid, earliestBlock, latestBlock, latestBlockHash]
```

Changes:
- **Removed**: Total difficulty (`td`) - no longer needed post-merge
- **Removed**: `blockhash` - replaced by `latestBlockHash`
- **Added**: `earliestBlock` - earliest available block number
- **Added**: `latestBlock` - latest block number
- **Added**: `latestBlockHash` - hash of latest block

### BlockRangeUpdate Message (0x11)

New message type introduced in eth/69:

```
[earliestBlock, latestBlock, latestBlockHash]
```

Sent when:
- Node's available block range changes
- New blocks are imported (max once per 32 blocks)
- Historical blocks are pruned
- After a chain reorganization

### Receipt Message (0x10)

**eth/68**: Includes bloom filter field in each receipt
**eth/69**: Bloom filter removed from network encoding

Benefits:
- ~530GB reduction in uncompressed sync data
- ~95GB reduction in compressed sync data
- Lower CPU usage for serving nodes

## Architecture

### Components

```
┌─────────────────────────────────────────────────────────────┐
│                      eth69 Package                           │
├─────────────────────────────────────────────────────────────┤
│                                                               │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐      │
│  │  protocol.go │  │  handler.go  │  │peer_tracker  │      │
│  │              │  │              │  │    .go       │      │
│  │ - Constants  │  │ - Message    │  │ - Block      │      │
│  │ - Types      │  │   handlers   │  │   range      │      │
│  │ - Validation │  │ - Status     │  │   tracking   │      │
│  └──────────────┘  └──────────────┘  └──────────────┘      │
│                                                               │
└─────────────────────────────────────────────────────────────┘
```

### Message Flow

```
Peer A                              Peer B
  │                                   │
  │ ─────── Status (0x00) ────────▶  │
  │                                   │
  │ ◀────── Status (0x00) ────────   │
  │                                   │
  │     Handshake Complete            │
  │                                   │
  │                                   │
  │    (New block imported)           │
  │                                   │
  │ ── BlockRangeUpdate (0x11) ───▶  │
  │                                   │
```

## Usage

### Initialize Handler

```go
import "github.com/n42blockchain/N42/internal/network/eth69"

// Create handler
handler := eth69.NewHandler(
    blockchain,      // BlockChainReader
    networkID,       // uint64 (1 for mainnet)
    earliestBlock,   // uint64 (0 for archive node)
    peerSender,      // PeerSender implementation
)
```

### Handle Status Message

```go
func handleStatus(peerID peer.ID, statusMsg *StatusPacket) error {
    return handler.HandleStatusMessage(peerID, statusMsg)
}
```

### Handle BlockRangeUpdate

```go
func handleBlockRangeUpdate(peerID peer.ID, update *BlockRangeUpdatePacket) error {
    return handler.HandleBlockRangeUpdate(peerID, update)
}
```

### On New Block

```go
func onNewBlockImported(block *types.Block) {
    // Handler will automatically send BlockRangeUpdate if needed
    handler.OnNewBlock(block)
}
```

### Query Peer Capabilities

```go
// Get peers that have a specific block
peers := handler.GetPeersWithBlock(blockNumber)

// Get peers that have a block range
peers := handler.GetPeersWithBlockRange(startBlock, endBlock)

// Get peer's block range
if range_, ok := handler.GetPeerRange(peerID); ok {
    fmt.Printf("Peer has blocks %d to %d\n",
        range_.EarliestBlock, range_.LatestBlock)
}
```

## Integration with N42

N42 uses libp2p instead of DevP2P, so the integration differs from geth:

1. **Protocol ID**: Uses libp2p protocol IDs instead of DevP2P capability negotiation
2. **Message Encoding**: Uses protobuf instead of RLP
3. **Transport**: Uses libp2p streams instead of RLPx connections

### Protobuf Integration

The protobuf definitions are in `api/protocol/sync_pb/sync_pb.proto`:

```protobuf
message Status {
  uint32 protocolVersion = 1;
  uint64 networkID = 2;
  types_pb.H256 genesisHash = 3;
  types_pb.H256 currentHeight = 4;
  uint64 earliestBlock = 5;
  uint64 latestBlock = 6;
  types_pb.H256 latestBlockHash = 7;
  bytes forkID = 8;
}

message BlockRangeUpdate {
  uint64 earliestBlock = 1;
  uint64 latestBlock = 2;
  types_pb.H256 latestBlockHash = 3;
}
```

## References

- **EIP-7642**: https://eips.ethereum.org/EIPS/eip-7642
- **Ethereum DevP2P**: https://github.com/ethereum/devp2p/blob/master/caps/eth.md
- **Geth Implementation**: https://github.com/ethereum/go-ethereum/tree/master/eth/protocols/eth
- **Erigon Implementation**: https://github.com/erigontech/erigon (PRs #15279, #17186, #17171)

## Testing

TODO: Add test coverage for:
- Status message validation
- BlockRangeUpdate message handling
- Peer range tracking
- Message frequency limiting
- Fork ID validation

## Future Improvements

1. **Fork ID**: Implement EIP-2124 fork identifier validation
2. **Receipt Encoding**: Optimize receipt encoding without bloom filters
3. **Metrics**: Add prometheus metrics for block range tracking
4. **History Expiry**: Integrate with pruning mechanism
5. **Peer Scoring**: Use block range info for peer selection

## License

Copyright 2022-2026 The N42 Authors. Licensed under LGPL-3.0.
