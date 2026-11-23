// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Authenticated production gate (sync review S0a / C2).
//
// HeightBehind trusts the single highest height any connected peer advertised
// in its Status message. The Status height is not authenticated, so one peer
// claiming head+10^6 made every leader connected to it skip every view it led
// and stopped the chain. HeightBehindQuorum replaces it as the gate signal:
// the node counts as "behind" only by a gap that is either
//
//   - proven: a block number named by a CommitQC this node verified (the
//     targets CatchUpTo / CatchUpToHash receive from the consensus layer),
//     recorded in verifiedCommitTarget; or
//   - reported by a quorum: the (f+1)-th highest height advertised by
//     distinct peers (peers.Status.QuorumHeight), which at most f faulty
//     peers cannot raise above an honest peer's height.
//
// The catch-up TARGET may still follow the highest claim (equal-height fork
// recovery needs it, see peers.Status.BestPeers); only the decision to stop
// producing is protected here. N42_SYNC_LEGACY_HEIGHT_GATE=1 restores the old
// gate.

package sync

import (
	"github.com/n42blockchain/N42/internal/synclegacy"
)

// noteVerifiedCommitTarget raises the verified-commit watermark to number.
// Callers must only pass numbers bound to a hash a verified CommitQC (or a
// verified QC the engine extends) names.
func (s *Service) noteVerifiedCommitTarget(number uint64) {
	for {
		prev := s.verifiedCommitTarget.Load()
		if number <= prev || s.verifiedCommitTarget.CompareAndSwap(prev, number) {
			return
		}
	}
}

// HeightBehindQuorum reports how many blocks the local committed head trails
// the network, counting only an authenticated gap: the highest verified
// CommitQC target, or the quorum-th highest advertised peer height. quorum is
// f+1 of the current validator set; values below 1 count as 1. Implements the
// optional gate probe of hotstuff.Service.heightBehind.
func (s *Service) HeightBehindQuorum(quorum int) uint64 {
	if synclegacy.HeightGate.On() {
		return s.HeightBehind()
	}
	self := currentBlockNumber(s.cfg.chain)
	cur := self.Uint64()
	var behind uint64
	if v := s.verifiedCommitTarget.Load(); v > cur {
		behind = v - cur
	}
	if q, _ := s.cfg.p2p.Peers().QuorumHeight(quorum, self); q != nil {
		if qh := q.Uint64(); qh > cur && qh-cur > behind {
			behind = qh - cur
		}
	}
	return behind
}
