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

package eth69

import (
	"context"
	"fmt"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/log"
)

// BlockChainReader defines the minimal interface required to access blockchain data.
type BlockChainReader interface {
	// CurrentBlock retrieves the current head block of the canonical chain.
	CurrentBlock() *block.Block

	// GetBlockByNumber retrieves a block by number.
	GetBlockByNumber(number uint64) *block.Block

	// GenesisBlock retrieves the genesis block.
	GenesisBlock() *block.Block

	// Config retrieves the blockchain's chain configuration.
	// Config() *params.ChainConfig
}

// PeerSender defines the interface for sending messages to peers.
type PeerSender interface {
	// SendBlockRangeUpdate sends a BlockRangeUpdate message to a peer.
	SendBlockRangeUpdate(ctx context.Context, peerID peer.ID, update *BlockRangeUpdatePacket) error

	// BroadcastBlockRangeUpdate broadcasts a BlockRangeUpdate to all connected peers.
	BroadcastBlockRangeUpdate(ctx context.Context, update *BlockRangeUpdatePacket) error
}

// Handler manages eth/69 protocol message handling.
type Handler struct {
	chain         BlockChainReader
	peerTracker   *PeerRangeTracker
	peerSender    PeerSender
	networkID     uint64
	genesisHash   types.Hash
	earliestBlock uint64 // Earliest block available (0 for archive nodes)
}

// NewHandler creates a new eth/69 protocol handler.
func NewHandler(chain BlockChainReader, networkID uint64, earliestBlock uint64, peerSender PeerSender) *Handler {
	var genesisHash types.Hash
	if genesis := chain.GenesisBlock(); genesis != nil {
		genesisHash = genesis.Hash()
	}

	return &Handler{
		chain:         chain,
		peerTracker:   NewPeerRangeTracker(),
		peerSender:    peerSender,
		networkID:     networkID,
		genesisHash:   genesisHash,
		earliestBlock: earliestBlock,
	}
}

// MakeStatusPacket creates a Status message for handshake or status updates.
func (h *Handler) MakeStatusPacket() *StatusPacket {
	currentBlock := h.chain.CurrentBlock()
	if currentBlock == nil {
		log.Warn("unable to create status packet: no current block")
		return nil
	}

	latestBlock := currentBlock.Number64().Uint64()
	h.peerTracker.UpdateLocalRange(h.earliestBlock, latestBlock, currentBlock.Hash())

	return &StatusPacket{
		ProtocolVersion: ProtocolVersion,
		NetworkID:       h.networkID,
		Genesis:         h.genesisHash,
		ForkID:          h.makeForkID(),
		EarliestBlock:   h.earliestBlock,
		LatestBlock:     latestBlock,
		LatestBlockHash: currentBlock.Hash(),
	}
}

// MakeBlockRangeUpdatePacket creates a BlockRangeUpdate message.
func (h *Handler) MakeBlockRangeUpdatePacket() *BlockRangeUpdatePacket {
	currentBlock := h.chain.CurrentBlock()
	if currentBlock == nil {
		return nil
	}

	return &BlockRangeUpdatePacket{
		EarliestBlock:   h.earliestBlock,
		LatestBlock:     currentBlock.Number64().Uint64(),
		LatestBlockHash: currentBlock.Hash(),
	}
}

// HandleStatusMessage processes an incoming Status message from a peer.
func (h *Handler) HandleStatusMessage(peerID peer.ID, status *StatusPacket) error {
	if status == nil {
		return ErrInvalidStatus
	}
	if !IsProtocolVersionSupported(uint(status.ProtocolVersion)) {
		return fmt.Errorf("unsupported protocol version: %d", status.ProtocolVersion)
	}
	if status.NetworkID != h.networkID {
		return fmt.Errorf("network ID mismatch: got %d, want %d", status.NetworkID, h.networkID)
	}
	if status.Genesis != h.genesisHash {
		return fmt.Errorf("genesis hash mismatch: got %s, want %s", status.Genesis.Hex(), h.genesisHash.Hex())
	}
	if err := status.ValidateBlockRange(); err != nil {
		return fmt.Errorf("invalid block range: %w", err)
	}

	h.peerTracker.UpdatePeerRange(
		peerID,
		status.EarliestBlock,
		status.LatestBlock,
		status.LatestBlockHash,
	)

	log.Debug("received status from peer",
		"peer", peerID.String()[:8],
		"protocol", status.ProtocolVersion,
		"network", status.NetworkID,
		"earliest", status.EarliestBlock,
		"latest", status.LatestBlock,
	)
	return nil
}

// HandleBlockRangeUpdate processes an incoming BlockRangeUpdate message.
func (h *Handler) HandleBlockRangeUpdate(peerID peer.ID, update *BlockRangeUpdatePacket) error {
	if update == nil {
		return ErrInvalidBlockRangeUpdate
	}
	if err := update.ValidateBlockRange(); err != nil {
		return fmt.Errorf("invalid block range update: %w", err)
	}

	h.peerTracker.UpdatePeerRange(
		peerID,
		update.EarliestBlock,
		update.LatestBlock,
		update.LatestBlockHash,
	)

	log.Debug("received block range update",
		"peer", peerID.String()[:8],
		"earliest", update.EarliestBlock,
		"latest", update.LatestBlock,
	)
	return nil
}

// OnNewBlock is called when a new block is imported.
// It broadcasts a BlockRangeUpdate to peers if needed per the EIP-7642 epoch rules.
func (h *Handler) OnNewBlock(blk *block.Block) {
	blockNumber := blk.Number64().Uint64()
	if !h.peerTracker.ShouldSendUpdate(blockNumber) {
		return
	}

	update := h.MakeBlockRangeUpdatePacket()
	if update == nil {
		return
	}
	h.peerTracker.MarkUpdateSent(blockNumber)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := h.peerSender.BroadcastBlockRangeUpdate(ctx, update); err != nil {
		log.Debug("failed to broadcast block range update", "err", err)
	} else {
		log.Debug("sent block range update", "latest", blockNumber)
	}
}

// OnPeerDisconnect is called when a peer disconnects.
func (h *Handler) OnPeerDisconnect(peerID peer.ID) {
	h.peerTracker.RemovePeer(peerID)
	log.Debug("removed peer range info", "peer", peerID.String()[:8])
}

func (h *Handler) GetPeerRange(peerID peer.ID) (*PeerBlockRange, bool) {
	return h.peerTracker.GetPeerRange(peerID)
}

func (h *Handler) GetPeersWithBlock(blockNumber uint64) []peer.ID {
	return h.peerTracker.GetPeersWithBlock(blockNumber)
}

func (h *Handler) GetPeersWithBlockRange(start, end uint64) []peer.ID {
	return h.peerTracker.GetPeersWithBlockRange(start, end)
}

func (h *Handler) GetLocalRange() *PeerBlockRange {
	return h.peerTracker.GetLocalRange()
}

func (h *Handler) GetAllPeerRanges() map[peer.ID]*PeerBlockRange {
	return h.peerTracker.GetAllPeerRanges()
}

// makeForkID returns a 4-byte fork identifier for the eth/69 StatusPacket.
//
// N42 derives the fork ID from the genesis hash using the same approach as
// utils.CreateForkDigest: the first 4 bytes of the genesis hash serve as the
// network-specific fork fingerprint.  Peers that share the same genesis hash
// will therefore always agree on the fork ID, while peers on a different chain
// (different genesis) will detect the mismatch during the handshake.
func (h *Handler) makeForkID() []byte {
	if h.genesisHash == (types.Hash{}) {
		return []byte{}
	}
	id := make([]byte, 4)
	copy(id, h.genesisHash[:4])
	return id
}

// SetEarliestBlock updates the earliest available block (e.g., after pruning).
func (h *Handler) SetEarliestBlock(earliest uint64) {
	h.earliestBlock = earliest
	if current := h.chain.CurrentBlock(); current != nil {
		h.peerTracker.UpdateLocalRange(earliest, current.Number64().Uint64(), current.Hash())
	}
}
