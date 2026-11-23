// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Branch-switch authority on the sync ingress paths (sync review S0b / C3).
//
// Only a block consensus named -- a proposal or commit target the engine asks
// for (FetchBlockByHash), the hash chain below one (alignAndImport), or the
// consensus parent the miner imports -- may switch the applied branch. Two
// sync paths handed that authority to blocks an arbitrary peer named:
//
//   - a pushed or gossiped block whose parent was missing fetched the parent
//     through FetchBlockByHash, i.e. with authority. The parent hash comes from
//     the peer's block, not from consensus. It is now fetched passively:
//     imported with plain InsertChain, its own missing parent fetched the same
//     way. A committed sibling still arrives through the consensus-named path
//     (the proposal's fetch-on-miss walks back to it with authority, and a
//     CommitQC drives catch-up).
//   - the range catch-up's equal-height fork recovery imports with authority
//     (unchanged in this stage; authenticating the range is S1), but blocks it
//     could not import yet are no longer future-queued with authority
//     (InsertChainCatchUp).
//
// N42_SYNC_LEGACY_FUTURE_AUTHORITY=1 restores both.

package sync

import (
	"context"
	"errors"
	gosync "sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/internal/p2p"
	"github.com/n42blockchain/N42/internal/synclegacy"
	"github.com/n42blockchain/N42/log"
)

// maxPassiveFetchEntries bounds the passive fetch rate-limit map.
const maxPassiveFetchEntries = 4096

// passiveFetchLimiter is the per-hash rate limit of passive parent fetches.
// It is separate from the fetch-on-miss limiter so a passive fetch never
// suppresses the authorized fetch consensus issues for the same hash.
type passiveFetchLimiter struct {
	mu   gosync.Mutex
	last map[types.Hash]time.Time
}

func (l *passiveFetchLimiter) allow(h types.Hash, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.last == nil || len(l.last) >= maxPassiveFetchEntries {
		l.last = make(map[types.Hash]time.Time)
	}
	if t, ok := l.last[h]; ok && now.Sub(t) < fetchMissInterval {
		return false
	}
	l.last[h] = now
	return true
}

// insertCatchUpAuthorized is the range catch-up's authorized insert: the
// switch authority applies to this call only (InsertChainCatchUp).
func (s *Service) insertCatchUpAuthorized(blocks []block.IBlock) (int, error) {
	if !synclegacy.FutureAuthority.On() {
		if c, ok := s.cfg.chain.(interface {
			InsertChainCatchUp([]block.IBlock) (int, error)
		}); ok {
			return c.InsertChainCatchUp(blocks)
		}
	}
	return s.insertAuthorized(blocks)
}

// NoteConsensusNamed records that a verified proposal or CommitQC named
// hash, so the chain's lag executor may import it with branch-switch
// authority (internal/lag_authority.go). The HotStuff service calls it for
// every verified proposal; CatchUpToHash for every commit target.
func (s *Service) NoteConsensusNamed(hash types.Hash) {
	if c, ok := s.cfg.chain.(interface{ MarkConsensusNamed(types.Hash) }); ok {
		c.MarkConsensusNamed(hash)
	}
}

// fetchMissingParent fetches the missing parent of a pushed or gossiped
// block. The parent hash is named by a peer, so it is fetched without
// branch-switch authority.
func (s *Service) fetchMissingParent(hash types.Hash) {
	if synclegacy.FutureAuthority.On() {
		s.FetchBlockByHash(hash)
		return
	}
	s.fetchPassive(hash, 0)
}

// maxPassiveFetchDepth bounds the passive walk-back through missing parents.
const maxPassiveFetchDepth = 32

// fetchPassive fetches hash from every connected peer and imports the first
// matching copy with plain InsertChain (no branch switch). A missing parent
// is fetched the same way, up to maxPassiveFetchDepth levels.
func (s *Service) fetchPassive(hash types.Hash, depth int) {
	if hash == (types.Hash{}) || depth > maxPassiveFetchDepth || s.hasBadBlock(hash) {
		return
	}
	if blk, _ := s.cfg.chain.GetBlockByHash(hash); blk != nil {
		return // stored: only a consensus-named fetch may re-apply it
	}
	if !s.passiveFetch.allow(hash, time.Now()) {
		return
	}
	protoID := protocol.ID(p2p.RPCBlockByHashTopicV1 + s.cfg.p2p.Encoding().ProtocolSuffix())
	log.Debug("passive parent fetch: requesting block", "hash", hash.Hex()[:12], "depth", depth)
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
			blk, err := ReadChunkedBlock(stream, s.cfg.p2p, true)
			if err != nil || blk.Hash() != hash {
				return
			}
			if _, busy := s.passiveImporting.LoadOrStore(hash, struct{}{}); busy {
				return
			}
			defer s.passiveImporting.Delete(hash)
			s.observeCatchUpBlock(hash, blk.Number64().Uint64())
			s.importPassive(blk, depth)
		}(pid)
	}
}

// importPassive inserts a passively fetched block without branch-switch
// authority and notifies the engine only on applied-state evidence.
func (s *Service) importPassive(blk block.IBlock, depth int) {
	if s.rejectBadBlock(s.ctx, blk) {
		return
	}
	if _, err := s.cfg.chain.InsertChain([]block.IBlock{blk}); err != nil {
		if isAncestorError(err) {
			s.fetchPassive(blk.ParentHash(), depth+1)
			return
		}
		if errors.Is(err, consensus.ErrExecutionInvalid) {
			s.setBadBlock(s.ctx, blk.Hash())
		}
		log.Debug("passive parent fetch: insert failed", "number", blk.Number64().Uint64(),
			"hash", blk.Hash().Hex()[:12], "err", err)
		return
	}
	if n := s.cfg.blockImportNotifier; n != nil && s.blockApplied(blk.Hash(), blk.Number64().Uint64()) {
		n.NotifyBlockImported(blk.Hash(), blk.TxHash())
	}
	s.retryDeferredChildren(blk.Hash())
}
