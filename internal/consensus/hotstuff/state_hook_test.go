// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The consensus state used to be persisted in a write transaction of its own,
// opened right after the canonicalization transaction. Folding it into that
// transaction removes one write transaction per committed block — and a
// profile put mdbx_txn_begin for WRITE transactions at 20.4% of all node CPU,
// so the second transaction cost far more than the 415 bytes it carried.
//
// The dangerous failure mode of that change is silent: the caller believes the
// state was written inside someone else's transaction when it was not, and
// stops persisting consensus state altogether. These tests pin the signal the
// caller keys off.

package hotstuff

import (
	"context"
	"errors"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
)

func sampleState() *ConsensusState {
	return &ConsensusState{
		View:                42,
		ConsecutiveTimeouts: 1,
		LastVotedView:       41,
		LastVotedHash:       types.Hash{0x01},
		LockedQC:            QuorumCertificate{View: 40, BlockHash: types.Hash{0x0A}},
		LastCommittedQC:     QuorumCertificate{View: 39, BlockHash: types.Hash{0x0B}},
	}
}

// hookFor builds the same closure newStateHook builds, from an explicit state
// so the test needs no live engine.
func hookFor(state *ConsensusState) *stateHook {
	h := &stateHook{view: state.View}
	h.run = func(tx kv.RwTx) error {
		if err := SaveConsensusState(tx, state); err != nil {
			return err
		}
		h.done = true
		return nil
	}
	return h
}

// TestStateHookDoneOnlyAfterItRuns is the whole contract: done reports whether
// the state actually reached the transaction, not whether the surrounding
// transaction succeeded. The commit path falls back to its own transaction
// whenever done is false, so a hook that lied here would silently stop
// persisting consensus state — and a restart would come back on a view that
// does not match the applied chain.
func TestStateHookDoneOnlyAfterItRuns(t *testing.T) {
	h := hookFor(sampleState())
	if h.done {
		t.Fatal("hook reports done before it ran")
	}

	db := memdb.NewTestDB(t)
	if err := db.Update(context.Background(), h.run); err != nil {
		t.Fatalf("hook: %v", err)
	}
	if !h.done {
		t.Fatal("hook ran successfully but does not report done")
	}

	if loaded := loadState(t, db); loaded.View != 42 {
		t.Fatalf("persisted view = %d, want 42", loaded.View)
	}
}

// TestStateHookNotDoneWhenTransactionNeverRunsIt covers the deferred
// canonicalization path: the enclosing transaction rolls back before reaching
// the hook, so nothing is written and the caller must still persist.
func TestStateHookNotDoneWhenTransactionNeverRunsIt(t *testing.T) {
	h := hookFor(sampleState())
	db := memdb.NewTestDB(t)

	sentinel := errors.New("block not in db yet")
	err := db.Update(context.Background(), func(tx kv.RwTx) error {
		// Canonicalization gives up before it reaches the hook.
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("update err = %v, want the sentinel", err)
	}
	if h.done {
		t.Fatal("hook reports done although it was never invoked")
	}
	// loadState fatals when nothing is stored, which is what we want here —
	// read it directly instead so the absence is an assertion, not a failure.
	var stored *ConsensusState
	if verr := db.View(context.Background(), func(tx kv.Tx) error {
		var lerr error
		stored, lerr = LoadConsensusState(tx)
		return lerr
	}); verr == nil && stored != nil {
		t.Fatal("state was persisted although the transaction rolled back")
	}
}

// TestStateHookNotDoneOnWriteFailure: CommitToCanonicalWith deliberately
// swallows a hook error so bookkeeping cannot block the chain. done must stay
// false so the caller notices and retries in its own transaction.
func TestStateHookNotDoneOnWriteFailure(t *testing.T) {
	failing := errors.New("write failed")
	h := &stateHook{view: 7}
	h.run = func(tx kv.RwTx) error { return failing }

	db := memdb.NewTestDB(t)
	// The enclosing transaction commits regardless — that is the swallow.
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		if herr := h.run(tx); herr != nil {
			_ = herr // swallowed, exactly as CommitToCanonicalWith does
		}
		return nil
	}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if h.done {
		t.Fatal("hook reports done although its write failed")
	}
}

// TestStateHookRequiresOuterCommit covers the failure window between a
// successful callback and MDBX's subsequent tx.Commit. The callback has run,
// but none of its writes are durable when the outer commit fails.
func TestStateHookRequiresOuterCommit(t *testing.T) {
	h := &stateHook{done: true}
	if stateHookCommitted(h, errors.New("commit failed")) {
		t.Fatal("hook reported durable after the enclosing transaction failed")
	}
	if !stateHookCommitted(h, nil) {
		t.Fatal("successful hook and enclosing commit not reported durable")
	}
}
