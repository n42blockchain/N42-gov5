// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

// Package eth69 implements the eth/69 protocol as specified in EIP-7642.
//
// The eth/69 protocol introduces history expiry awareness and simpler receipts.
// Key changes from eth/68:
//   - Status message includes available block range (earliestBlock, latestBlock)
//   - New BlockRangeUpdate message (0x11) for announcing block range changes
//   - Receipts message removes bloom filter to reduce bandwidth
//   - Total difficulty removed from Status (post-merge)
//
// References:
//   - EIP-7642: https://eips.ethereum.org/EIPS/eip-7642
//   - Geth implementation: https://github.com/ethereum/go-ethereum/tree/master/eth/protocols/eth
//   - Erigon implementation: https://github.com/erigontech/erigon
package eth69

import (
	"fmt"

	"github.com/n42blockchain/N42/common/types"
)

// Protocol version constants
const (
	ETH68 = 68
	ETH69 = 69

	// Current protocol version
	ProtocolVersion = ETH69

	// ProtocolName is the official short name of the `eth` protocol used during devp2p capability negotiation.
	// For N42, we use a custom protocol identifier over libp2p instead of devp2p.
	ProtocolName = "eth"
)

// ProtocolVersions lists all supported eth protocol versions.
var ProtocolVersions = []uint{ETH69, ETH68}

// protocolLengths defines the number of message codes for each protocol version.
var protocolLengths = map[uint]uint64{
	ETH68: 17, // eth/68 has message codes 0x00-0x10
	ETH69: 18, // eth/69 adds BlockRangeUpdateMsg (0x11)
}

// Message codes as defined in the eth/69 specification.
// These match the Ethereum DevP2P eth protocol message identifiers.
const (
	StatusMsg                     = 0x00 // Status exchange during handshake
	NewBlockHashesMsg             = 0x01 // Announce new block hashes
	TransactionsMsg               = 0x02 // Broadcast transactions
	GetBlockHeadersMsg            = 0x03 // Request block headers
	BlockHeadersMsg               = 0x04 // Deliver block headers
	GetBlockBodiesMsg             = 0x05 // Request block bodies
	BlockBodiesMsg                = 0x06 // Deliver block bodies
	NewBlockMsg                   = 0x07 // Announce new block
	NewPooledTransactionHashesMsg = 0x08 // Announce pooled transaction hashes
	GetPooledTransactionsMsg      = 0x09 // Request pooled transactions
	PooledTransactionsMsg         = 0x0a // Deliver pooled transactions
	GetReceiptsMsg                = 0x0f // Request receipts
	ReceiptsMsg                   = 0x10 // Deliver receipts
	BlockRangeUpdateMsg           = 0x11 // Announce block range update (eth/69 only)
)

// StatusPacket represents the eth/69 status message format.
// This message is sent during the handshake to exchange chain information.
//
// The structure differs from eth/68 by:
//   - Removing total difficulty (TD)
//   - Adding earliestBlock, latestBlock, latestBlockHash for history expiry support
type StatusPacket struct {
	ProtocolVersion uint32      // Protocol version number (should be ETH69)
	NetworkID       uint64      // Network identifier (1 = mainnet, etc.)
	Genesis         types.Hash  // Genesis block hash for fork verification
	ForkID          []byte      // EIP-2124 fork identifier
	EarliestBlock   uint64      // Earliest available block number
	LatestBlock     uint64      // Latest block number (replaces head)
	LatestBlockHash types.Hash  // Hash of the latest block
}

// BlockRangeUpdatePacket announces changes in the node's available block range.
// This message is new in eth/69 and allows peers to track which historical
// blocks are available from each peer.
//
// The message should be sent:
//   - Initially during handshake (via Status message)
//   - When the block range changes (e.g., new blocks, pruning old blocks)
//   - Limited to once per 32-block epoch to prevent spam
type BlockRangeUpdatePacket struct {
	EarliestBlock   uint64     // Earliest available block number
	LatestBlock     uint64     // Latest block number
	LatestBlockHash types.Hash // Hash of the latest block
}

// String returns a human-readable representation of StatusPacket.
func (s *StatusPacket) String() string {
	return fmt.Sprintf("Status{protocol=%d, network=%d, genesis=%s, earliest=%d, latest=%d, latestHash=%s}",
		s.ProtocolVersion, s.NetworkID, s.Genesis.Hex()[:10], s.EarliestBlock, s.LatestBlock, s.LatestBlockHash.Hex()[:10])
}

// String returns a human-readable representation of BlockRangeUpdatePacket.
func (b *BlockRangeUpdatePacket) String() string {
	return fmt.Sprintf("BlockRangeUpdate{earliest=%d, latest=%d, latestHash=%s}",
		b.EarliestBlock, b.LatestBlock, b.LatestBlockHash.Hex()[:10])
}

// ValidateBlockRange checks if the block range is valid.
func (b *BlockRangeUpdatePacket) ValidateBlockRange() error {
	if b.EarliestBlock > b.LatestBlock {
		return fmt.Errorf("invalid block range: earliest(%d) > latest(%d)", b.EarliestBlock, b.LatestBlock)
	}
	return nil
}

// ValidateBlockRange checks if the block range in StatusPacket is valid.
func (s *StatusPacket) ValidateBlockRange() error {
	if s.EarliestBlock > s.LatestBlock {
		return fmt.Errorf("invalid block range: earliest(%d) > latest(%d)", s.EarliestBlock, s.LatestBlock)
	}
	return nil
}

// GetProtocolLength returns the number of message codes for a given protocol version.
func GetProtocolLength(version uint) (uint64, error) {
	if length, ok := protocolLengths[version]; ok {
		return length, nil
	}
	return 0, fmt.Errorf("unsupported protocol version: %d", version)
}

// IsProtocolVersionSupported checks if a protocol version is supported.
func IsProtocolVersionSupported(version uint) bool {
	for _, v := range ProtocolVersions {
		if v == version {
			return true
		}
	}
	return false
}
