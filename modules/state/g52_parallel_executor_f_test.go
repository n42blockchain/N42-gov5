// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"fmt"
	"sync"
	"testing"
)

// TestG52ExecuteBlockParallelFDisjointTxs mirrors
// TestParallelExecutor_DisjointTxs but through the per-worker base-factory
// entry point (ExecuteBlockParallelF), which nothing else in the suite
// drives. Each worker gets its own MapBaseReader instance via the
// factory, as a real MDBX-RoTx-per-worker caller would.
func TestG52ExecuteBlockParallelFDisjointTxs(t *testing.T) {
	const numTxs = 24
	factory := func() (MVBaseReader, func()) {
		return NewMapBaseReader(nil), nil
	}

	executor := func(txIdx int) TxExecutor {
		return func(v *MVStateView) error {
			key := []byte(fmt.Sprintf("tx%d-key", txIdx))
			val := []byte(fmt.Sprintf("tx%d-val", txIdx))
			v.Set(key, val)
			return nil
		}
	}

	results, mv, err := ExecuteBlockParallelF(numTxs, 4, factory, executor)
	if err != nil {
		t.Fatalf("ExecuteBlockParallelF: %v", err)
	}
	if len(results) != numTxs {
		t.Fatalf("results len=%d want %d", len(results), numTxs)
	}
	for _, r := range results {
		if r.Err != nil {
			t.Errorf("tx %d: %v", r.TxIdx, r.Err)
		}
	}
	for i := 0; i < numTxs; i++ {
		key := []byte(fmt.Sprintf("tx%d-key", i))
		val, _, st := mv.Read(key, numTxs)
		if st != MVOk || string(val) != fmt.Sprintf("tx%d-val", i) {
			t.Errorf("tx %d: st=%d val=%s", i, st, val)
		}
	}
}

// TestG52ExecuteBlockParallelFEmptyBlock covers the numTxs==0 fast path
// through the factory entry point.
func TestG52ExecuteBlockParallelFEmptyBlock(t *testing.T) {
	results, mv, err := ExecuteBlockParallelF(0, 4, func() (MVBaseReader, func()) {
		return NewMapBaseReader(nil), nil
	}, nil)
	if err != nil || len(results) != 0 || mv == nil {
		t.Fatalf("empty block: results=%v err=%v mv=%v", results, err, mv)
	}
}

// TestG52ExecuteBlockParallelFFactoryFailure covers the pre-flight guard: a
// factory that always returns a nil base must fail fast with a descriptive
// error instead of deadlocking workers on wg.Wait.
func TestG52ExecuteBlockParallelFFactoryFailure(t *testing.T) {
	_, _, err := ExecuteBlockParallelF(4, 2, func() (MVBaseReader, func()) {
		return nil, nil
	}, func(int) TxExecutor { return func(*MVStateView) error { return nil } })
	if err == nil {
		t.Fatal("expected an error when the base factory always returns nil")
	}
}

// TestG52RerunTxFinal drives rerunTxFinal directly: a tx re-executed at a
// bumped incarnation that writes a DIFFERENT key than its previous run must
// both install the new write and delete the stale key the previous
// incarnation had written (the write-set reconciliation loop), while
// txResults/txViews/txWrittenKeys are updated under the per-tx mutex.
func TestG52RerunTxFinal(t *testing.T) {
	const numTxs = 1
	mv := NewMVHashMap(4)
	base := NewMapBaseReader(nil)
	txMu := make([]sync.Mutex, numTxs)
	txViews := make([]*MVStateView, numTxs)
	txWrittenKeys := make([][]string, numTxs)
	txResults := make([]Result, numTxs)

	// Seed a "previous incarnation" that wrote staleKey.
	staleKey := []byte("stale")
	prevView := NewMVStateView(mv, base, 0, 0)
	prevView.Set(staleKey, []byte("old"))
	prevKeys := prevView.FlushWrites()
	txViews[0] = prevView
	txWrittenKeys[0] = prevKeys
	if _, _, st := mv.Read(staleKey, numTxs); st != MVOk {
		t.Fatalf("precondition: staleKey should be readable before rerun, st=%d", st)
	}

	newKey := []byte("fresh")
	executor := func(txIdx int) TxExecutor {
		return func(v *MVStateView) error {
			v.Set(newKey, []byte("new"))
			return nil
		}
	}

	if err := rerunTxFinal(0, mv, base, executor, txViews, txWrittenKeys, txResults, txMu); err != nil {
		t.Fatalf("rerunTxFinal: %v", err)
	}

	if txResults[0].Err != nil {
		t.Fatalf("rerunTxFinal result Err = %v, want nil", txResults[0].Err)
	}
	if txViews[0] == nil || txViews[0].incarnation != 1 {
		t.Fatalf("expected the stored view's incarnation to bump to 1, got %v", txViews[0])
	}
	if _, _, st := mv.Read(newKey, numTxs); st != MVOk {
		t.Fatalf("expected the new write to be visible, st=%d", st)
	}
	if _, _, st := mv.Read(staleKey, numTxs); st != MVNotFound {
		t.Fatalf("expected the stale no-longer-written key to be deleted, st=%d", st)
	}
}
