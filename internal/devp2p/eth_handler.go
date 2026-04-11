// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package devp2p

import (
	"fmt"

	gethp2p "github.com/ethereum/go-ethereum/p2p"

	n42block "github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/params"
)

// BlockProvider reads chain data for serving to peers.
type BlockProvider interface {
	CurrentHead() (*n42block.Header, types.Hash, error)
	GetHeaderByNumber(number uint64) (*n42block.Header, error)
	GetHeaderByHash(hash types.Hash) (*n42block.Header, error)
}

// EthHandler processes eth/68-69 protocol messages.
type EthHandler struct {
	chainConfig *params.ChainConfig
	provider    BlockProvider
	networkID   uint64
	genesis     types.Hash
	genesisTime uint64
}

// NewEthHandler creates a new eth protocol message handler.
func NewEthHandler(cfg *params.ChainConfig, genesisHash types.Hash, genesisTime uint64, provider BlockProvider) (*EthHandler, error) {
	if genesisHash == (types.Hash{}) {
		return nil, fmt.Errorf("genesis hash unavailable")
	}
	return &EthHandler{
		chainConfig: cfg,
		provider:    provider,
		networkID:   networkID(cfg),
		genesis:     genesisHash,
		genesisTime: genesisTime,
	}, nil
}

func (h *EthHandler) currentForkID(head *n42block.Header) forkID {
	if head == nil {
		return forkID{}
	}
	return newForkID(h.chainConfig, h.genesis, h.genesisTime, head.Number64().Uint64(), head.Time)
}

// runPeer is called by the p2p server for each new peer connection.
func (h *EthHandler) runPeer(peer *gethp2p.Peer, rw gethp2p.MsgReadWriter) error {
	log.Info("devp2p peer connected", "id", peer.ID().String()[:16], "name", peer.Name())

	// Handshake: send our status.
	head, headHash, err := h.provider.CurrentHead()
	if err != nil {
		return fmt.Errorf("current head: %w", err)
	}
	status := statusPacket{
		ProtocolVersion: uint32(69),
		NetworkID:       h.networkID,
		Genesis:         h.genesis,
		ForkID:          h.currentForkID(head),
		EarliestBlock:   0,
		LatestBlock:     head.Number64().Uint64(),
		LatestBlockHash: headHash,
	}
	if err := gethp2p.Send(rw, 0, &status); err != nil {
		return fmt.Errorf("send status: %w", err)
	}

	// Read peer's status.
	msg, err := rw.ReadMsg()
	if err != nil {
		return err
	}
	if msg.Code != 0 {
		return fmt.Errorf("expected status message, got %d", msg.Code)
	}
	var peerStatus statusPacket
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
	case 1:
		// Peer announces new block hashes — log and ignore for now.
		log.Debug("devp2p: new block hashes", "peer", peer.ID().String()[:16])
		return nil

	case 2:
		// Peer broadcasts transactions — forward to mempool (TODO).
		return nil

	case 3:
		return h.handleGetBlockHeaders(rw, msg)

	case 5:
		return h.handleGetBlockBodies(rw, msg)

	case 7:
		log.Debug("devp2p: new block", "peer", peer.ID().String()[:16])
		return nil

	case 8:
		return nil // ignore

	case 9:
		return nil // TODO: serve from txpool

	case 15:
		return nil // TODO: serve from freezer

	case 17:
		return nil // eth/69 range update, log only

	default:
		return fmt.Errorf("unknown message code: %d", msg.Code)
	}
}

// handleGetBlockHeaders serves block headers to a requesting peer.
func (h *EthHandler) handleGetBlockHeaders(rw gethp2p.MsgReadWriter, msg gethp2p.Msg) error {
	var req getBlockHeadersPacket
	if err := msg.Decode(&req); err != nil {
		return err
	}

	headers := make([]*n42block.Header, 0, req.Amount)
	num := req.Origin.Number
	if req.Origin.Hash != (types.Hash{}) {
		header, err := h.provider.GetHeaderByHash(req.Origin.Hash)
		if err != nil {
			return err
		}
		if header == nil {
			return gethp2p.Send(rw, 4, &blockHeadersPacket{RequestID: req.RequestID})
		}
		num = header.Number64().Uint64()
	}

	for i := uint64(0); i < req.Amount && i < 1024; i++ {
		hdr, err := h.provider.GetHeaderByNumber(num)
		if err != nil {
			return err
		}
		if hdr == nil {
			break
		}
		headers = append(headers, n42block.CopyHeader(hdr))
		if req.Reverse {
			if num <= req.Skip+1 {
				break
			}
			num -= req.Skip + 1
		} else {
			num += req.Skip + 1
		}
	}

	resp := blockHeadersPacket{
		RequestID: req.RequestID,
		Headers:   headers,
	}
	return gethp2p.Send(rw, 4, &resp)
}

// handleGetBlockBodies serves block bodies to a requesting peer.
func (h *EthHandler) handleGetBlockBodies(rw gethp2p.MsgReadWriter, msg gethp2p.Msg) error {
	var req getBlockBodiesPacket
	if err := msg.Decode(&req); err != nil {
		return err
	}

	resp := blockBodiesPacket{
		RequestID: req.RequestID,
		Bodies:    nil,
	}
	return gethp2p.Send(rw, 6, &resp)
}
