// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package devp2p

import (
	"fmt"

	gethp2p "github.com/ethereum/go-ethereum/p2p"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/network/eth69"
	"github.com/n42blockchain/N42/lib/rlp"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/params"
)

// BlockProvider reads chain data for serving to peers.
type BlockProvider interface {
	// CurrentHead returns the current chain head block number and hash.
	CurrentHead() (uint64, types.Hash)
	// GetHeaderRLP returns the RLP-encoded header for a block number.
	GetHeaderRLP(number uint64) ([]byte, error)
	// GetBodyRLP returns the RLP-encoded body for a block number.
	GetBodyRLP(number uint64) ([]byte, error)
}

// EthHandler processes eth/68-69 protocol messages.
type EthHandler struct {
	chainConfig *params.ChainConfig
	provider    BlockProvider
	networkID   uint64
	genesis     types.Hash
}

// NewEthHandler creates a new eth protocol message handler.
func NewEthHandler(cfg *params.ChainConfig, provider BlockProvider) *EthHandler {
	return &EthHandler{
		chainConfig: cfg,
		provider:    provider,
		networkID:   params.NetworkIDByChainName(cfg.ChainName),
		genesis:     *params.GenesisHashByChainName(cfg.ChainName),
	}
}

// runPeer is called by the p2p server for each new peer connection.
func (h *EthHandler) runPeer(peer *gethp2p.Peer, rw gethp2p.MsgReadWriter) error {
	log.Info("devp2p peer connected", "id", peer.ID().String()[:16], "name", peer.Name())

	// Handshake: send our status.
	headNum, headHash := h.provider.CurrentHead()
	status := eth69.StatusPacket{
		ProtocolVersion: uint32(eth69.ETH68),
		NetworkID:       h.networkID,
		Genesis:         h.genesis,
		EarliestBlock:   0,
		LatestBlock:     headNum,
		LatestBlockHash: headHash,
	}
	statusRLP, err := rlp.EncodeToBytes(&status)
	if err != nil {
		return fmt.Errorf("encode status: %w", err)
	}
	if err := gethp2p.Send(rw, eth69.StatusMsg, statusRLP); err != nil {
		return fmt.Errorf("send status: %w", err)
	}

	// Read peer's status.
	msg, err := rw.ReadMsg()
	if err != nil {
		return err
	}
	if msg.Code != eth69.StatusMsg {
		return fmt.Errorf("expected status message, got %d", msg.Code)
	}
	var peerStatus eth69.StatusPacket
	if err := msg.Decode(&peerStatus); err != nil {
		return fmt.Errorf("decode peer status: %w", err)
	}
	if peerStatus.NetworkID != h.networkID {
		return fmt.Errorf("network ID mismatch: ours=%d theirs=%d", h.networkID, peerStatus.NetworkID)
	}
	if peerStatus.Genesis != h.genesis {
		return fmt.Errorf("genesis mismatch: ours=%s theirs=%s", h.genesis.Hex()[:10], peerStatus.Genesis.Hex()[:10])
	}

	log.Info("devp2p handshake complete",
		"peer", peer.ID().String()[:16],
		"head", peerStatus.LatestBlock)

	// Message loop.
	for {
		msg, err := rw.ReadMsg()
		if err != nil {
			return err
		}
		if err := h.handleMessage(peer, rw, msg); err != nil {
			log.Warn("devp2p message error", "peer", peer.ID().String()[:16],
				"msg", msg.Code, "err", err)
			return err
		}
	}
}

// handleMessage routes an incoming message to the appropriate handler.
func (h *EthHandler) handleMessage(peer *gethp2p.Peer, rw gethp2p.MsgReadWriter, msg gethp2p.Msg) error {
	defer msg.Discard()

	switch msg.Code {
	case eth69.NewBlockHashesMsg:
		// Peer announces new block hashes — log and ignore for now.
		log.Debug("devp2p: new block hashes", "peer", peer.ID().String()[:16])
		return nil

	case eth69.TransactionsMsg:
		// Peer broadcasts transactions — forward to mempool (TODO).
		return nil

	case eth69.GetBlockHeadersMsg:
		return h.handleGetBlockHeaders(rw, msg)

	case eth69.GetBlockBodiesMsg:
		return h.handleGetBlockBodies(rw, msg)

	case eth69.NewBlockMsg:
		log.Debug("devp2p: new block", "peer", peer.ID().String()[:16])
		return nil

	case eth69.NewPooledTransactionHashesMsg:
		return nil // ignore

	case eth69.GetPooledTransactionsMsg:
		return nil // TODO: serve from txpool

	case eth69.GetReceiptsMsg:
		return nil // TODO: serve from freezer

	case eth69.BlockRangeUpdateMsg:
		return nil // eth/69 range update, log only

	default:
		return fmt.Errorf("unknown message code: %d", msg.Code)
	}
}

// handleGetBlockHeaders serves block headers to a requesting peer.
func (h *EthHandler) handleGetBlockHeaders(rw gethp2p.MsgReadWriter, msg gethp2p.Msg) error {
	var req eth69.GetBlockHeadersPacket
	if err := msg.Decode(&req); err != nil {
		return err
	}

	headers := make([][]byte, 0, req.Amount)
	num := req.Origin.Number
	if req.Origin.Hash != (types.Hash{}) {
		// TODO: resolve hash to number
		return nil
	}

	for i := uint64(0); i < req.Amount && i < 1024; i++ {
		hdr, err := h.provider.GetHeaderRLP(num)
		if err != nil || hdr == nil {
			break
		}
		headers = append(headers, hdr)
		if req.Reverse {
			if num <= req.Skip+1 {
				break
			}
			num -= req.Skip + 1
		} else {
			num += req.Skip + 1
		}
	}

	resp := eth69.BlockHeadersPacket{
		RequestID: req.RequestID,
		Headers:   headers,
	}
	return gethp2p.Send(rw, eth69.BlockHeadersMsg, &resp)
}

// handleGetBlockBodies serves block bodies to a requesting peer.
func (h *EthHandler) handleGetBlockBodies(rw gethp2p.MsgReadWriter, msg gethp2p.Msg) error {
	var req eth69.GetBlockBodiesPacket
	if err := msg.Decode(&req); err != nil {
		return err
	}

	// TODO: resolve hashes to block numbers and serve bodies.
	// For now, return empty response.
	resp := eth69.BlockBodiesPacket{
		RequestID: req.RequestID,
		Bodies:    nil,
	}
	return gethp2p.Send(rw, eth69.BlockBodiesMsg, &resp)
}
