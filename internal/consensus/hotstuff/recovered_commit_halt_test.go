// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus"
)

// The restart's recovered commit and the shutdown flush call
// CommitToCanonical outside the Decide path: a conflicting commit there
// halts the node too and is dropped, never retried.
func TestRecoveredAndShutdownCommitConflictHalts(t *testing.T) {
	conflict := func(h types.Hash) error {
		return &consensus.ConflictingCommitError{
			Target: h, TargetNumber: 9, Height: 9,
			Committed: types.HexToHash("0xaa"), Incoming: h,
			CommittedHead: types.HexToHash("0xaa"), CommittedHeadNumber: 9,
		}
	}
	t.Run("recovered", func(t *testing.T) {
		s, _, producer := hsTNewOutputService(t)
		rc := types.HexToHash("0xbb")
		producer.canonErr = conflict(rc)
		s.recoveredCommit = rc
		s.retryRecoveredCommit(types.Hash{})
		if !s.SafetyHalted() {
			t.Fatal("conflicting recovered commit did not halt")
		}
		if s.recoveredCommit != (types.Hash{}) {
			t.Fatal("conflicting recovered commit kept for retry")
		}
	})
	t.Run("shutdown", func(t *testing.T) {
		s, _, producer := hsTNewOutputService(t)
		h := types.HexToHash("0xcc")
		producer.canonErr = conflict(h)
		s.pendingCommits = map[types.Hash]struct{}{h: {}}
		s.FlushPendingCommits()
		if !s.SafetyHalted() {
			t.Fatal("conflicting shutdown commit did not halt")
		}
		if _, ok := s.pendingCommits[h]; ok {
			t.Fatal("conflicting shutdown commit kept for retry")
		}
	})
}
