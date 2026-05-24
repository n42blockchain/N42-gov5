// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Downloader issues eth/69 GetBlockHeaders and GetBlockBodies requests
// to connected devp2p peers. RequestHeaders builds the HashOrNumber
// request packet and sends it over a gethp2p.MsgReadWriter while
// responses are dispatched asynchronously by the ETH handler.

package devp2p

import (
	"context"
	"fmt"

	gethp2p "github.com/ethereum/go-ethereum/p2p"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/network/eth69"
	"github.com/n42blockchain/N42/log"
)

// Downloader fetches headers and bodies from connected peers.
type Downloader struct {
	server *Server
}

// NewDownloader creates a new block downloader.
func NewDownloader(server *Server) *Downloader {
	return &Downloader{server: server}
}

// RequestHeaders sends a GetBlockHeaders request to a specific peer.
// The response will be delivered asynchronously via the handler.
func (d *Downloader) RequestHeaders(ctx context.Context, peer *gethp2p.Peer, rw gethp2p.MsgReadWriter, origin uint64, amount uint64) error {
	req := eth69.GetBlockHeadersPacket{
		RequestID: uint64(origin), // use origin as simple request ID
		GetBlockHeadersQuery: &eth69.GetBlockHeadersQuery{
			Origin:  eth69.HashOrNumber{Number: origin},
			Amount:  amount,
			Skip:    0,
			Reverse: false,
		},
	}
	if err := gethp2p.Send(rw, eth69.GetBlockHeadersMsg, &req); err != nil {
		return fmt.Errorf("send GetBlockHeaders: %w", err)
	}
	log.Debug("Requested headers", "from", origin, "count", amount, "peer", peer.ID().String()[:16])
	return nil
}

// RequestBodies sends a GetBlockBodies request for the given block hashes.
func (d *Downloader) RequestBodies(ctx context.Context, rw gethp2p.MsgReadWriter, hashes []types.Hash) error {
	req := eth69.GetBlockBodiesPacket{
		RequestID: 0,
		Hashes:    hashes,
	}
	if err := gethp2p.Send(rw, eth69.GetBlockBodiesMsg, &req); err != nil {
		return fmt.Errorf("send GetBlockBodies: %w", err)
	}
	log.Debug("Requested bodies", "count", len(hashes))
	return nil
}
