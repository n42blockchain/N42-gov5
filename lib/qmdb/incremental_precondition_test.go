package qmdb

import (
	"errors"
	"testing"
)

// TestLoadIncrementalDeclinesOnHistRecDirtyGuards covers the three
// precondition guards in loadIncremental that
// TestIncrementalReloadDeclinesOnPreconditions (persist_incremental_test.go)
// does not reach: a full-history recorder attached, an active undo
// recording, and stale (dirty) twig internal nodes.
func TestLoadIncrementalDeclinesOnHistRecDirtyGuards(t *testing.T) {
	store := newMapStore()
	tr := New()
	tr.SetCold(ColdReaderFromGetter(store))
	tr.SetLeafStore(LeafStoreFromGetter(store))
	for i := uint64(0); i < 10; i++ {
		tr.Set(key(i), val(i))
	}
	tr.Root()
	if _, _, err := tr.FlushTo(store, 0); err != nil {
		t.Fatalf("FlushTo: %v", err)
	}
	cursor := tr.NextSlot()
	delta := tr.LiveBits() - tr.LiveCount()

	// History recorder attached.
	withHist := New()
	withHist.SetCold(ColdReaderFromGetter(store))
	withHist.SetLeafStore(LeafStoreFromGetter(store))
	if err := withHist.LoadFrom(store); err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	withHist.SetHistoryRecorder(NewHistoryRecorder(newMapHistoryStore()))
	if err := withHist.LoadIncremental(store, withHist.NextSlot(), withHist.LiveBits()-withHist.LiveCount()); !errors.Is(err, ErrIncrementalUnavailable) {
		t.Fatalf("history-recorder guard not declined: %v", err)
	}
	_ = cursor
	_ = delta

	// Active undo recording.
	withRec := New()
	withRec.SetCold(ColdReaderFromGetter(store))
	withRec.SetLeafStore(LeafStoreFromGetter(store))
	if err := withRec.LoadFrom(store); err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	withRec.StartUndoRecording()
	if err := withRec.LoadIncremental(store, withRec.NextSlot(), withRec.LiveBits()-withRec.LiveCount()); !errors.Is(err, ErrIncrementalUnavailable) {
		t.Fatalf("active-undo-recording guard not declined: %v", err)
	}

	// Stale (dirty) twig internal nodes.
	dirty := New()
	dirty.SetCold(ColdReaderFromGetter(store))
	dirty.SetLeafStore(LeafStoreFromGetter(store))
	if err := dirty.LoadFrom(store); err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	dirtyCursor, dirtyDelta := dirty.NextSlot(), dirty.LiveBits()-dirty.LiveCount()
	dirty.ForceDirty() // marks every resident twig dirty and bumps nDirtyTwigs
	if err := dirty.LoadIncremental(store, dirtyCursor, dirtyDelta); !errors.Is(err, ErrIncrementalUnavailable) {
		t.Fatalf("dirty-twigs guard not declined: %v", err)
	}
}
