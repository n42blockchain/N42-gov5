package hotstuff

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

// hashFromByte builds a distinct types.Hash from a small integer, used to
// generate many unique block hashes cheaply.
func hashFromByte(i int) types.Hash {
	var h types.Hash
	h[0] = byte(i)
	h[1] = byte(i >> 8)
	return h
}

// TestRememberImportedEvictsOldestOnOverflow drives rememberImported past
// MaxImportedBlocks and confirms the oldest entry (FIFO head) is evicted from
// both importedBlocks and importedParents, while the newest MaxImportedBlocks
// entries remain known.
func TestRememberImportedEvictsOldestOnOverflow(t *testing.T) {
	setup := newTestSetup(t, 4)
	engine, _ := newTestEngine(t, setup, 0)

	hashes := make([]types.Hash, MaxImportedBlocks+5)
	for i := range hashes {
		hashes[i] = hashFromByte(i + 1)
		parent := hashFromByte(i)
		engine.rememberImported(hashes[i], parent)
	}

	if len(engine.importedFIFO) != MaxImportedBlocks {
		t.Fatalf("importedFIFO len = %d, want %d", len(engine.importedFIFO), MaxImportedBlocks)
	}

	// The first 5 inserted hashes must have been evicted.
	for i := 0; i < 5; i++ {
		if engine.importedBlocks[hashes[i]] {
			t.Fatalf("expected hashes[%d] to be evicted from importedBlocks", i)
		}
		if _, ok := engine.importedParents[hashes[i]]; ok {
			t.Fatalf("expected hashes[%d] to be evicted from importedParents", i)
		}
	}

	// The most recent MaxImportedBlocks hashes must still be present.
	for i := 5; i < len(hashes); i++ {
		if !engine.importedBlocks[hashes[i]] {
			t.Fatalf("expected hashes[%d] to remain in importedBlocks", i)
		}
	}
}

// TestRememberImportedDuplicateBackfillsParent exercises the early-return
// branch for an already-known hash: a second call with a non-zero parent
// must backfill importedParents without disturbing the FIFO.
func TestRememberImportedDuplicateBackfillsParent(t *testing.T) {
	setup := newTestSetup(t, 4)
	engine, _ := newTestEngine(t, setup, 0)

	h := hashFromByte(1)
	engine.rememberImported(h, types.Hash{}) // first notify lacks a parent
	if _, ok := engine.importedParents[h]; ok {
		t.Fatal("expected no parent recorded yet")
	}
	if len(engine.importedFIFO) != 1 {
		t.Fatalf("importedFIFO len = %d, want 1", len(engine.importedFIFO))
	}

	parent := hashFromByte(2)
	engine.rememberImported(h, parent) // second notify backfills the parent
	if engine.importedParents[h] != parent {
		t.Fatalf("importedParents[h] = %v, want %v", engine.importedParents[h], parent)
	}
	if len(engine.importedFIFO) != 1 {
		t.Fatalf("importedFIFO len = %d, want 1 (no duplicate FIFO entry)", len(engine.importedFIFO))
	}
}

// TestOnBlockImportedCastsImportGatedVote exercises onBlockImported's
// import-gated voting branch: once a proposal is pending in the current view
// and this import matches it, onBlockImported must journal and send a Round
// 1 vote for that block.
func TestOnBlockImportedCastsImportGatedVote(t *testing.T) {
	setup := newTestSetup(t, 4)
	engine, outputCh := newTestEngine(t, setup, 1) // not the leader for view 1

	view := ViewNumber(1)
	blockHash := hashFromByte(9)
	engine.pendingProposals[view] = blockHash

	if err := engine.onBlockImported(blockHash, types.Hash{}, types.Hash{}); err != nil {
		t.Fatalf("onBlockImported: %v", err)
	}

	if !engine.importedBlocks[blockHash] {
		t.Fatal("expected the block to be remembered as imported")
	}
	if !engine.roundState.HasVotedInView(view) {
		t.Fatal("expected the import-gated vote to be recorded as cast")
	}

	outputs := drainOutputs(outputCh)
	foundVote := false
	for _, o := range outputs {
		if o.Type == OutputSendToValidator && o.Message != nil && o.Message.Type == MsgVote {
			foundVote = true
		}
	}
	if !foundVote {
		t.Fatal("expected onBlockImported to emit a Round 1 vote")
	}
}

// TestOnBlockImportedDAVerificationFailureReturnsError exercises the Baby
// Raptr DA verification branch: when the actual tx root computed at import
// does not match the proposal's recorded TxRootHash, onBlockImported must
// return a *DAVerificationError and not cast a vote.
func TestOnBlockImportedDAVerificationFailureReturnsError(t *testing.T) {
	setup := newTestSetup(t, 4)
	engine, _ := newTestEngine(t, setup, 1)

	blockHash := hashFromByte(7)
	expectedRoot := hashFromByte(100)
	actualRoot := hashFromByte(101)
	engine.pendingTxRoots[blockHash] = expectedRoot

	err := engine.onBlockImported(blockHash, actualRoot, types.Hash{})
	if err == nil {
		t.Fatal("expected a DA verification error")
	}
	daErr, ok := err.(*DAVerificationError)
	if !ok {
		t.Fatalf("expected *DAVerificationError, got %T", err)
	}
	if daErr.ExpectedRoot != expectedRoot || daErr.ActualRoot != actualRoot {
		t.Fatalf("DAVerificationError = %+v, want expected=%v actual=%v", daErr, expectedRoot, actualRoot)
	}
	if _, stillPending := engine.pendingTxRoots[blockHash]; stillPending {
		t.Fatal("expected pendingTxRoots entry to be consumed even on mismatch")
	}
}

// TestOnBlockImportedNoPendingProposalIsNoop exercises the common no-op
// branch: an import that does not match the current view's pending
// proposal (and no deferred vote is possible) must return nil without
// casting any vote.
func TestOnBlockImportedNoPendingProposalIsNoop(t *testing.T) {
	setup := newTestSetup(t, 4)
	engine, outputCh := newTestEngine(t, setup, 1)

	blockHash := hashFromByte(42)
	if err := engine.onBlockImported(blockHash, types.Hash{}, types.Hash{}); err != nil {
		t.Fatalf("onBlockImported: %v", err)
	}
	if engine.roundState.HasVotedInView(engine.roundState.CurrentView()) {
		t.Fatal("expected no vote to be cast when there is no matching pending proposal")
	}
	if outputs := drainOutputs(outputCh); len(outputs) != 0 {
		t.Fatalf("expected no outputs, got %d: %+v", len(outputs), outputs)
	}
}
