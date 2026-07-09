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
		log.Debug("block by hash: not found locally", "hash", hash.Hex()[:12])
		// Say so explicitly instead of silently closing: a bare close makes the
		// requester read a naked EOF ("failed to read status code from first
		// chunk"), indistinguishable from a real transport fault — and since
		// the fetch fans out to every peer while typically only the leader has
		// the block, the log drowned in phantom EOF errors.
		writeErrorResponseToStream(responseCodeServerError, "block not found", stream, s.cfg.p2p)
		return
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
	// Already have the block BODY: don't just return — the caller needs it
	// APPLIED. On a branch switch the whole ancestor sub-branch may already be
	// in the DB from earlier rounds but never executed onto the world state.
	// Align SYNCHRONOUSLY along the parent chain in ONE pass: walk back until
	// a block imports (its parent is applied — the unwind-on-switch import
	// path reverts the sibling branch), then replay descendants in order.
	// Doing this asynchronously per block (the previous behavior) let several
	// stale same-height candidates' retries tug the applied head between
	// sibling branches every 2s, so the CURRENT proposal's parent never
	// stayed aligned long enough for its import to succeed.
	if blk, _ := s.cfg.chain.GetBlockByHash(hash); blk != nil {
		go func() {
			cur := blk
			pending := []block.IBlock{}
			for depth := 0; depth < 32; depth++ {
				if _, err := s.insertAuthorized([]block.IBlock{cur}); err == nil {
					break
				}
				p, _ := s.cfg.chain.GetBlockByHash(cur.ParentHash())
				if p == nil {
					// Genuinely missing ancestor: fall back to the remote fetch
					// for it; this block stays in the future queue meanwhile.
					s.FetchBlockByHash(cur.ParentHash())
					return
				}
				pending = append(pending, cur)
				cur = p
			}
			// Replay the collected descendants oldest-first down to the target.
			for i := len(pending) - 1; i >= 0; i-- {
				if _, err := s.insertAuthorized([]block.IBlock{pending[i]}); err != nil {
					log.Debug("fetch-on-miss: chain-align replay failed",
						"number", pending[i].Number64().Uint64(),
						"hash", pending[i].Hash().Hex()[:12], "err", err)
					return
				}
			}
			if n := s.cfg.blockImportNotifier; n != nil {
				n.NotifyBlockImported(blk.Hash(), blk.TxHash())
			}
		}()
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
				// Debug: with the fan-out, most peers legitimately answer
				// "block not found" — only aggregate failure is interesting.
				log.Debug("fetch-on-miss: read failed", "hash", hash.Hex()[:12], "err", err)
				return
			}
			if blk.Hash() != hash {
				log.Info("fetch-on-miss: hash mismatch", "want", hash.Hex()[:12], "got", blk.Hash().Hex()[:12])
				return // peer returned the wrong block
			}
			if _, err := s.insertAuthorized([]block.IBlock{blk}); err != nil {
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

// insertAuthorized imports consensus-requested blocks WITH branch-switch
// authority (the engine named this block/branch via a proposal, so reverting
// a losing applied sibling is legitimate). Falls back to the plain insert if
// the chain implementation doesn't support authorization.
func (s *Service) insertAuthorized(blocks []block.IBlock) (int, error) {
	if a, ok := s.cfg.chain.(interface {
		InsertChainAuthorized([]block.IBlock) (int, error)
	}); ok {
		return a.InsertChainAuthorized(blocks)
	}
	return s.cfg.chain.InsertChain(blocks)
}
