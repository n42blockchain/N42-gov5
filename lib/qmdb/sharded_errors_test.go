package qmdb

import (
	"errors"
	"testing"
)

// putterOnly wraps a mapStore but intentionally does NOT implement Deleter, to
// exercise shardTablePutter.Delete's "wrapped Putter has no Delete" no-op path.
type putterOnly struct{ mapStore }

func (p putterOnly) Put(table string, k, v []byte) error { return p.mapStore.Put(table, k, v) }

// TestShardTablePutterDeleteWithoutDeleter covers FlushTo's dead-row deletion
// attempt when the underlying Putter does not implement Deleter: it must be a
// silent no-op, not a panic or error.
func TestShardTablePutterDeleteWithoutDeleter(t *testing.T) {
	st, err := NewSharded(2)
	if err != nil {
		t.Fatalf("NewSharded: %v", err)
	}
	for _, s := range st.shards {
		s.SetLeafStore(LeafStoreFromGetter(newMapStore()))
	}
	for i := uint64(0); i < 100; i++ {
		st.Set(key(i), val(i))
	}
	po := putterOnly{mapStore: newMapStore()}
	// First flush establishes rows; second with overwrites exercises the dead-row
	// path, which must tolerate the missing Deleter.
	next, _, err := st.FlushTo(po, nil)
	if err != nil {
		t.Fatalf("FlushTo #1: %v", err)
	}
	for i, s := range st.shards {
		s.SetCold(ColdReaderFromGetter(shardTableGetter{inner: po, sid: byte(i)}))
	}
	for i := range st.shards {
		st.shards[i].EvictThrough(next[i])
	}
	for i := uint64(0); i < 100; i += 3 {
		st.Set(key(i), val(i+1))
	}
	if _, _, err := st.FlushTo(po, next); err != nil {
		t.Fatalf("FlushTo #2 (dead-row delete attempt without Deleter): %v", err)
	}
}

// failingShardGetter makes shard 1's LoadFrom fail, so ShardedTree.LoadFrom's
// error-wrapping branch (identifying which shard failed) is exercised.
type failingShardGetter struct{ mapStore }

func (g failingShardGetter) GetOne(table string, key []byte) ([]byte, error) {
	if len(key) > 0 && key[0] == 1 { // shard-prefixed key for shard 1
		return nil, errors.New("injected read failure")
	}
	return g.mapStore.GetOne(table, key)
}

func TestShardedLoadFromWrapsShardError(t *testing.T) {
	st, err := NewSharded(2)
	if err != nil {
		t.Fatalf("NewSharded: %v", err)
	}
	for i := uint64(0); i < 20; i++ {
		st.Set(key(i), val(i))
	}
	store := newMapStore()
	if _, _, err := st.FlushTo(store, nil); err != nil {
		t.Fatalf("FlushTo: %v", err)
	}

	fresh, err := NewSharded(2)
	if err != nil {
		t.Fatalf("NewSharded (reload): %v", err)
	}
	err = fresh.LoadFrom(failingShardGetter{mapStore: store})
	if err == nil {
		t.Fatalf("LoadFrom did not propagate the injected shard failure")
	}
}
