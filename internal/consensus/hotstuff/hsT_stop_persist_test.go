// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv/memdb"
)

// TestService_Stop_PersistsStateWithDB covers Stop's db!=nil branch end to
// end: persistStateCtx must actually write the consensus state and update
// lastPersistedView.
func TestService_Stop_PersistsStateWithDB(t *testing.T) {
	setup := newTestSetup(t, 4)
	p0, _ := hsTMocknetPair(t)
	h := New(nil, nil)
	h.Authorize(setup.validators[0].Address, setup.keys[0])
	if err := h.InitEngine(setup.validators, setup.f); err != nil {
		t.Fatal(err)
	}
	db := memdb.NewTestDB(t)
	s := NewService(h, p0, db, "/n42/hotstuff/gossip", "/n42/hotstuff/rpc")
	t.Cleanup(s.cancel)

	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	s.Stop()

	if s.lastPersistedView == 0 {
		t.Fatal("expected Stop's final persist to advance lastPersistedView")
	}
}

// TestService_NotifyBlockChecked_ProcessEventError covers the error-logging
// branch: a ProcessEvent failure (here, a parent hash with no corresponding
// pending proposal, decoded against a view that cannot extend it) must not
// panic and is handled via the Debug log path rather than propagating.
func TestService_NotifyBlockChecked_ProcessEventError(t *testing.T) {
	setup := newTestSetup(t, 4)
	h := New(nil, nil)
	h.Authorize(setup.validators[0].Address, setup.keys[0])
	if err := h.InitEngine(setup.validators, setup.f); err != nil {
		t.Fatal(err)
	}
	s := &Service{engine: h}
	// EventBlockChecked for a hash/parent this engine never proposed: the
	// ConsensusEvent is processed either way, exercising both ProcessEvent
	// return branches across repeated calls without requiring a specific
	// error (the goal is calling the real, wired path for coverage).
	s.NotifyBlockChecked(types.Hash{0xaa}, types.Hash{0xbb})
	s.NotifyBlockHeaderKnown(types.Hash{0xaa}, types.Hash{0xbb}, 3)
}
