// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Recovered pending commit. handleBlockCommitted persists the new committed
// QC even when CommitToCanonical has to defer (the block is executed but its
// lagged write is not durable yet: "ahead of applied head"). In-run, the
// import notification of that block retries the commit. A shutdown or crash
// in that window loses the retry: the service stops before the chain drains
// its last write, and pendingCommits lives only in memory. The node then
// restarts with a committed QC above its canonical head. The QC is genuine,
// so the restart treats its block as a pending commit again instead of as a
// diverged state.

package hotstuff

import (
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// maxRecoveredCommitDepth bounds the header walk from a persisted committed
// QC block down to the canonical head. A committed QC runs ahead of
// canonical by the commits deferred in one shutdown window, a handful of
// blocks; anything deeper is not this gap and is left to the operator.
const maxRecoveredCommitDepth = 1024

// CommittedQCAheadOfCanonical reports whether hash names a block above the
// canonical head (HeadBlockHash) that descends from it through local
// headers: the committed QC's block was committed by the fleet but this
// node's canonical chain has not reached it yet. number is that block's
// height. It is false when the block is unknown, at or below the canonical
// head, or on another branch.
func CommittedQCAheadOfCanonical(tx kv.Tx, hash types.Hash) (ahead bool, number uint64, err error) {
	if hash == (types.Hash{}) {
		return false, 0, nil
	}
	n := rawdb.ReadHeaderNumber(tx, hash)
	if n == nil {
		return false, 0, nil
	}
	number = *n
	headHash := rawdb.ReadHeadBlockHash(tx)
	headNum := rawdb.ReadHeaderNumber(tx, headHash)
	if headNum == nil || number <= *headNum || number-*headNum > maxRecoveredCommitDepth {
		return false, number, nil
	}
	cur, curNum := hash, number
	for curNum > *headNum {
		hdr := rawdb.ReadHeader(tx, cur, curNum)
		if hdr == nil {
			return false, number, nil
		}
		cur, curNum = hdr.ParentHash, curNum-1
	}
	return cur == headHash, number, nil
}

// retryRecoveredCommit canonicalizes the restart's pending commit when it
// is set and hash is zero (any) or names it. It stays set until
// CommitToCanonical succeeds: unlike an in-run pending commit, nothing else
// would ever retry it, and a body-only notification can arrive before the
// block is applied.
func (s *Service) retryRecoveredCommit(hash types.Hash) {
	s.pendingMu.Lock()
	rc := s.recoveredCommit
	s.pendingMu.Unlock()
	if rc == (types.Hash{}) || (hash != (types.Hash{}) && hash != rc) || s.blockProducer == nil {
		return
	}
	// The recovered block may have been unknown at restart. If canonical
	// has since reached its height on another hash, committing it would
	// rewrite canonical down to it (the commit's monotonicity guard only
	// short-circuits when canonical already names it): drop it instead.
	switch s.recoveredCommitCanonical(rc) {
	case recoveredCanonicalSame:
		s.clearRecoveredCommit()
		log.Info("hotstuff: recovered committed QC block is canonical", "hash", rc)
		return
	case recoveredCanonicalOther:
		s.clearRecoveredCommit()
		log.Warn("hotstuff: recovered committed QC block conflicts with the canonical chain at its height; dropped, not committed",
			"hash", rc)
		return
	}
	if err := s.blockProducer.CommitToCanonical(rc); err != nil {
		log.Debug("hotstuff: recovered committed QC block not canonical yet", "hash", rc, "err", err)
		return
	}
	s.clearRecoveredCommit()
	log.Info("hotstuff: recovered committed QC block is canonical", "hash", rc)
}

const (
	recoveredCanonicalOpen  = iota // height unknown here, or no canonical row there yet
	recoveredCanonicalSame         // canonical names the recovered block
	recoveredCanonicalOther        // canonical names another block at that height
)

// recoveredCommitCanonical compares the canonical row at rc's height with rc.
func (s *Service) recoveredCommitCanonical(rc types.Hash) int {
	if s.db == nil {
		return recoveredCanonicalOpen
	}
	status := recoveredCanonicalOpen
	_ = s.db.View(s.ctx, func(tx kv.Tx) error {
		n := rawdb.ReadHeaderNumber(tx, rc)
		if n == nil {
			return nil
		}
		c, err := rawdb.ReadCanonicalHash(tx, *n)
		if err != nil || c == (types.Hash{}) {
			return err
		}
		if c == rc {
			status = recoveredCanonicalSame
		} else {
			status = recoveredCanonicalOther
		}
		return nil
	})
	return status
}

func (s *Service) clearRecoveredCommit() {
	s.pendingMu.Lock()
	s.recoveredCommit = types.Hash{}
	s.pendingMu.Unlock()
}

// FlushPendingCommits retries every deferred canonical commit once. The
// chain calls it at shutdown after its last lagged write is durable
// (SetLagDrainedHook): this service has stopped by then, and the write's
// import notification may never be delivered.
func (s *Service) FlushPendingCommits() {
	if s.blockProducer == nil {
		return
	}
	s.pendingMu.Lock()
	hashes := make([]types.Hash, 0, len(s.pendingCommits))
	for h := range s.pendingCommits {
		hashes = append(hashes, h)
	}
	s.pendingMu.Unlock()
	for _, h := range hashes {
		if err := s.blockProducer.CommitToCanonical(h); err != nil {
			log.Debug("hotstuff: shutdown commit-to-canonical retry failed", "hash", h, "err", err)
			continue
		}
		s.pendingMu.Lock()
		delete(s.pendingCommits, h)
		s.pendingMu.Unlock()
		log.Info("hotstuff: deferred commit-to-canonical completed at shutdown", "hash", h)
	}
	s.retryRecoveredCommit(types.Hash{})
}
