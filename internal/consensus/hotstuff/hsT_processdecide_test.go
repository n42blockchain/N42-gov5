// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

// hsTBuildCommitQC builds a real, BLS-verifiable CommitQC for the given view
// and block hash, signed by a quorum of setup's keys.
func hsTBuildCommitQC(t *testing.T, setup *testSetup, view ViewNumber, blockHash types.Hash) *QuorumCertificate {
	t.Helper()
	msg := CommitSigningMessage(view, blockHash)
	vc := NewVoteCollector(view, blockHash, setup.vs.Len())
	quorum := setup.vs.QuorumSize()
	for i := 0; i < quorum; i++ {
		sig := setup.keys[i].Sign(msg)
		if err := vc.AddVote(ValidatorIndex(i), sig); err != nil {
			t.Fatal(err)
		}
	}
	qc, err := vc.BuildQCWithMessage(setup.vs, msg)
	if err != nil {
		t.Fatal(err)
	}
	return qc
}

// TestProcessDecide_StaleViewIsNoop covers the decide.View < currentView
// early return.
func TestProcessDecide_StaleViewIsNoop(t *testing.T) {
	setup := newTestSetup(t, 4)
	e, _ := newTestEngine(t, setup, 0)
	// Current view starts at 1; a Decide for view 0 is stale.
	if err := e.processDecide(&Decide{View: 0}); err != nil {
		t.Fatalf("processDecide(stale) = %v, want nil", err)
	}
}

// TestProcessDecide_InsufficientSigners covers the quorum-size guard on the
// embedded CommitQC.
func TestProcessDecide_InsufficientSigners(t *testing.T) {
	setup := newTestSetup(t, 4)
	e, _ := newTestEngine(t, setup, 0)
	view := e.roundState.CurrentView()
	qc := &QuorumCertificate{View: view, BlockHash: types.Hash{0x01}, Signers: []bool{true}} // 1 < quorum(3)

	if err := e.processDecide(&Decide{View: view, BlockHash: types.Hash{0x01}, CommitQC: *qc}); err == nil {
		t.Fatal("expected an InsufficientVotesError")
	}
}

// TestProcessDecide_ViewMismatch covers the Decide/CommitQC view-agreement
// guard.
func TestProcessDecide_ViewMismatch(t *testing.T) {
	setup := newTestSetup(t, 4)
	e, _ := newTestEngine(t, setup, 0)
	view := e.roundState.CurrentView()
	qc := hsTBuildCommitQC(t, setup, view, types.Hash{0x02})
	qc.View = view + 1 // desync from decide.View below

	if err := e.processDecide(&Decide{View: view, BlockHash: types.Hash{0x02}, CommitQC: *qc}); err == nil {
		t.Fatal("expected a ViewMismatchError")
	}
}

// TestProcessDecide_BlockHashMismatch covers the Decide/CommitQC block-hash
// agreement guard.
func TestProcessDecide_BlockHashMismatch(t *testing.T) {
	setup := newTestSetup(t, 4)
	e, _ := newTestEngine(t, setup, 0)
	view := e.roundState.CurrentView()
	qc := hsTBuildCommitQC(t, setup, view, types.Hash{0x03})

	if err := e.processDecide(&Decide{View: view, BlockHash: types.Hash{0x04}, CommitQC: *qc}); err == nil {
		t.Fatal("expected a BlockHashMismatchError")
	}
}

// TestProcessDecide_Success covers the full happy path: OutputBlockCommitted
// and OutputViewChanged are emitted and the view advances.
func TestProcessDecide_Success(t *testing.T) {
	setup := newTestSetup(t, 4)
	e, outputCh := newTestEngine(t, setup, 0)
	view := e.roundState.CurrentView()
	hash := types.Hash{0x05}
	qc := hsTBuildCommitQC(t, setup, view, hash)

	if err := e.processDecide(&Decide{View: view, BlockHash: hash, CommitQC: *qc}); err != nil {
		t.Fatalf("processDecide: %v", err)
	}
	outputs := drainOutputs(outputCh)
	var sawCommitted, sawViewChanged bool
	for _, o := range outputs {
		if o.Type == OutputBlockCommitted && o.Hash == hash {
			sawCommitted = true
		}
		if o.Type == OutputViewChanged {
			sawViewChanged = true
		}
	}
	if !sawCommitted || !sawViewChanged {
		t.Fatalf("outputs = %+v, want OutputBlockCommitted and OutputViewChanged", outputs)
	}
	if e.roundState.CurrentView() != view+1 {
		t.Fatalf("CurrentView = %d, want %d", e.roundState.CurrentView(), view+1)
	}
}

// TestProcessDecide_LargeViewGapRequestsSync covers the SyncGapThreshold
// branch: a Decide far ahead of the local view also emits OutputSyncRequired.
func TestProcessDecide_LargeViewGapRequestsSync(t *testing.T) {
	setup := newTestSetup(t, 4)
	e, outputCh := newTestEngine(t, setup, 0)
	currentView := e.roundState.CurrentView()
	farView := currentView + SyncGapThreshold + 1
	hash := types.Hash{0x06}
	qc := hsTBuildCommitQC(t, setup, farView, hash)

	if err := e.processDecide(&Decide{View: farView, BlockHash: hash, CommitQC: *qc}); err != nil {
		t.Fatalf("processDecide: %v", err)
	}
	outputs := drainOutputs(outputCh)
	found := false
	for _, o := range outputs {
		if o.Type == OutputSyncRequired && o.TargetView == farView {
			found = true
		}
	}
	if !found {
		t.Fatalf("outputs = %+v, want an OutputSyncRequired for the far view", outputs)
	}
}
