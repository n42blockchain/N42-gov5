// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package sync

import (
	"context"
	"io"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/p2p"
	"github.com/n42blockchain/N42/log"
)

// blockByHashStreamHandler serves a single block requested by its 32-byte hash.
// It is the server side of fetch-on-miss: a follower that received a HotStuff
// Proposal but not the referenced block (its direct push was dropped) asks a
// peer for the block here so it can import and cast its import-gated vote.
func (s *Service) blockByHashStreamHandler(stream network.Stream) {
	defer func() { _ = stream.Close() }()

	var hb [32]byte
	if _, err := io.ReadFull(stream, hb[:]); err != nil {
		return
	}
	hash := types.BytesToHash(hb[:])
	blk, err := s.cfg.chain.GetBlockByHash(hash)
	if err != nil || blk == nil {
		log.Info("block by hash: not found locally", "hash", hash.Hex()[:12])
		return // we don't have it either
	}
	log.Info("block by hash: serving", "reqHash", hash.Hex()[:12], "blkHash", blk.Hash().Hex()[:12], "number", blk.Number64().Uint64())
	if err := WriteBlockChunk(stream, s.cfg.chain, s.cfg.p2p.Encoding(), blk); err != nil {
		log.Debug("block by hash: write failed", "hash", hash.Hex()[:12], "err", err)
	}
}

// FetchBlockByHash requests a block by hash from all connected peers and imports
// the first valid response, then notifies the consensus engine. This is the
// client side of fetch-on-miss, invoked when the engine asks to execute a
// proposed block we don't yet have. Implements hotstuff.BlockFetcher.
func (s *Service) FetchBlockByHash(hash types.Hash) {
	// Already have it (direct push or a previous fetch arrived): nothing to do.
	if blk, _ := s.cfg.chain.GetBlockByHash(hash); blk != nil {
		return
	}
	protoID := protocol.ID(p2p.RPCBlockByHashTopicV1 + s.cfg.p2p.Encoding().ProtocolSuffix())
	log.Info("fetch-on-miss: requesting block", "hash", hash.Hex()[:12], "peers", len(s.cfg.p2p.Peers().Connected()))
	for _, pid := range s.cfg.p2p.Peers().Connected() {
		go func(pid peer.ID) {
			ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
			defer cancel()
			stream, err := s.cfg.p2p.Host().NewStream(ctx, pid, protoID)
			if err != nil {
				return
			}
			defer func() { _ = stream.Close() }()
			if _, err := stream.Write(hash[:]); err != nil {
				return
			}
			// Do NOT CloseWrite: the handler reads exactly 32 bytes (io.ReadFull)
			// then writes the block back on the same stream. Closing the write half
			// here tears the stream down before the response can be read (observed
			// as "read status code: EOF").
			blk, err := ReadChunkedBlock(stream, s.cfg.p2p, true)
			if err != nil {
				log.Info("fetch-on-miss: read failed", "hash", hash.Hex()[:12], "err", err)
				return
			}
			if blk.Hash() != hash {
				log.Info("fetch-on-miss: hash mismatch", "want", hash.Hex()[:12], "got", blk.Hash().Hex()[:12])
				return // peer returned the wrong block
			}
			if _, err := s.cfg.chain.InsertChain([]block.IBlock{blk}); err != nil {
				log.Debug("fetch-on-miss: insert failed", "number", blk.Number64().Uint64(), "err", err)
				return
			}
			log.Info("fetch-on-miss: imported block", "number", blk.Number64().Uint64(), "hash", blk.Hash().Hex()[:12])
			if n := s.cfg.blockImportNotifier; n != nil {
				n.NotifyBlockImported(blk.Hash(), blk.TxHash())
			}
		}(pid)
	}
}
