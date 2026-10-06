package qmdb

import (
	"errors"
	"testing"
)

type retentionFailStore struct{ mapStore }

func (s retentionFailStore) Put(table string, key, value []byte) error {
	if table == MetaTable && string(key) == "liveCount" {
		return errors.New("injected final metadata write failure")
	}
	return s.mapStore.Put(table, key, value)
}
func cloneRetentionStore(source mapStore) mapStore {
	result := newMapStore()
	for table, rows := range source {
		for key, value := range rows {
			_ = result.Put(table, []byte(key), value)
		}
	}
	return result
}
func TestRetainedCommittedRowsAreReclaimedAfterAbort(t *testing.T) {
	for _, failedFlush := range []bool{false, true} {
		store := newMapStore()
		tr := New()
		tr.SetCold(ColdReaderFromGetter(store))
		tr.SetLeafStore(LeafStoreFromGetter(store))
		tr.Set(key(1), val(1))
		next, _, err := tr.FlushTo(store, 0)
		if err != nil {
			t.Fatal(err)
		}
		if tr.flushedThrough != 0 {
			t.Fatal("uncommitted flush advanced committed cursor")
		}
		tr.CommitFlush()
		if tr.flushedThrough != next || tr.ResidentEntries() != 1 {
			t.Fatal("committed resident boundary missing")
		}
		original := tr.Root()
		tr.StartUndoRecording()
		tr.Set(key(1), val(2))
		undo := tr.StopUndoRecording()
		staged := cloneRetentionStore(store)
		var sink Putter = staged
		if failedFlush {
			sink = retentionFailStore{staged}
		}
		_, _, err = tr.FlushTo(sink, next)
		if failedFlush != (err != nil) {
			t.Fatalf("unexpected flush error: %v", err)
		}
		if tr.flushedThrough != next {
			t.Fatal("pending flush changed committed boundary")
		}
		tr.AbortFlush()
		tr.CommitFlush() // abort must disarm any staged cursor, even if called again.
		if tr.flushedThrough != next {
			t.Fatal("aborted flush advanced committed boundary")
		}
		if err := tr.ApplyUndo(undo); err != nil {
			t.Fatal(err)
		}
		if tr.Root() != original {
			t.Fatal("abort/undo changed original root")
		}
		tr.Set(key(1), val(3))
		if _, _, err := tr.FlushTo(store, next); err != nil {
			t.Fatal(err)
		}
		tr.CommitFlush()
		if entryRows(store) != tr.LiveCount() || entryRows(store) != 1 {
			t.Fatalf("resident old row was not reclaimed: %d", entryRows(store))
		}
		rebuilt := New()
		rebuilt.SetCold(ColdReaderFromGetter(store))
		rebuilt.SetLeafStore(LeafStoreFromGetter(store))
		if err := rebuilt.LoadFrom(store); err != nil {
			t.Fatal(err)
		}
		if rebuilt.Root() != tr.Root() {
			t.Fatal("restart root differs")
		}
	}
}
func TestRetainedLeaflessRowsDoNotAccumulateReclaimWork(t *testing.T) {
	store := newMapStore()
	tr := New()
	var through uint64
	for i := uint64(0); i < 20; i++ {
		tr.Set(key(1), val(i))
		var err error
		through, _, err = tr.FlushTo(store, through)
		if err != nil {
			t.Fatal(err)
		}
		tr.CommitFlush()
	}
	if len(tr.deadFlushed) != 0 {
		t.Fatal("queued resident reclamation that cannot be performed without leaf blobs")
	}
	if entryRows(store) != 20 {
		t.Fatal("leafless persistence lost frozen leaf recovery rows")
	}
}
