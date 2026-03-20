package jmt

import (
	"fmt"
	"testing"
)

func gcTestKey(i int) Hash {
	return Blake3Hasher{}.Hash([]byte(fmt.Sprintf("key-%d", i)))
}

func TestGCBasicRefCounting(t *testing.T) {
	gc := NewNodeGC()

	h1 := Blake3Hasher{}.Hash([]byte("node1"))
	h2 := Blake3Hasher{}.Hash([]byte("node2"))

	gc.IncRef(h1)
	gc.IncRef(h1)
	gc.IncRef(h2)

	if gc.TrackedNodeCount() != 2 {
		t.Fatalf("expected 2 tracked nodes, got %d", gc.TrackedNodeCount())
	}

	gc.DecRef(h1) // refcount 2 → 1
	if gc.PendingDeleteCount() != 0 {
		t.Fatal("h1 should not be pending (refcount=1)")
	}

	gc.DecRef(h1) // refcount 1 → 0, queued for delete
	if gc.PendingDeleteCount() != 1 {
		t.Fatalf("expected 1 pending delete, got %d", gc.PendingDeleteCount())
	}

	gc.DecRef(h2)
	if gc.PendingDeleteCount() != 2 {
		t.Fatalf("expected 2 pending deletes, got %d", gc.PendingDeleteCount())
	}
}

func TestGCEmptyHashIgnored(t *testing.T) {
	gc := NewNodeGC()
	gc.IncRef(EmptyHash)
	gc.DecRef(EmptyHash)
	if gc.TrackedNodeCount() != 0 {
		t.Fatal("EmptyHash should be ignored")
	}
}

func TestGCCollectGarbageWithTree(t *testing.T) {
	store := NewMemStore()
	tree := New(store)
	tree.EnableGC()

	// Insert 10 keys
	for i := 0; i < 10; i++ {
		key := gcTestKey(i)
		tree.Put(key, []byte(fmt.Sprintf("value-%d", i)))
	}
	tree.Flush()
	initialStoreSize := store.Len()
	t.Logf("Store size after 10 inserts: %d nodes", initialStoreSize)

	// Update all 10 keys (creates new nodes, old ones become garbage)
	for i := 0; i < 10; i++ {
		key := gcTestKey(i)
		tree.Put(key, []byte(fmt.Sprintf("updated-%d", i)))
	}
	tree.Flush()
	postUpdateSize := store.Len()
	t.Logf("Store size after 10 updates: %d nodes (before GC)", postUpdateSize)

	// Store should have grown (old + new nodes)
	if postUpdateSize <= initialStoreSize {
		t.Fatal("store should grow after updates without GC")
	}

	// Run GC
	deleted, err := tree.CollectGarbage(store)
	if err != nil {
		t.Fatalf("CollectGarbage error: %v", err)
	}
	postGCSize := store.Len()
	t.Logf("GC deleted %d nodes, store size after GC: %d", deleted, postGCSize)

	if deleted == 0 {
		t.Fatal("expected some nodes to be garbage collected")
	}

	// Verify tree is still functional after GC
	for i := 0; i < 10; i++ {
		key := gcTestKey(i)
		val, err := tree.Get(key)
		if err != nil {
			t.Fatalf("Get(key-%d) after GC: %v", i, err)
		}
		expected := fmt.Sprintf("updated-%d", i)
		if string(val) != expected {
			t.Fatalf("Get(key-%d) = %s, want %s", i, val, expected)
		}
	}
}

func TestGCDeleteTriggersCollection(t *testing.T) {
	store := NewMemStore()
	tree := New(store)
	tree.EnableGC()

	// Insert 5 keys
	for i := 0; i < 5; i++ {
		tree.Put(gcTestKey(i), []byte(fmt.Sprintf("v%d", i)))
	}
	tree.Flush()
	sizeAfterInsert := store.Len()

	// Delete 3 keys
	for i := 0; i < 3; i++ {
		tree.Delete(gcTestKey(i))
	}
	tree.Flush()

	// Collect garbage
	deleted, err := tree.CollectGarbage(store)
	if err != nil {
		t.Fatalf("CollectGarbage error: %v", err)
	}
	t.Logf("After deleting 3 keys: GC collected %d nodes, store %d → %d",
		deleted, sizeAfterInsert, store.Len())

	// Remaining keys should still work
	for i := 3; i < 5; i++ {
		val, err := tree.Get(gcTestKey(i))
		if err != nil {
			t.Fatalf("Get(key-%d) after GC: %v", i, err)
		}
		if string(val) != fmt.Sprintf("v%d", i) {
			t.Fatalf("wrong value for key-%d", i)
		}
	}
}

func TestGCDisabledByDefault(t *testing.T) {
	store := NewMemStore()
	tree := New(store)

	// GC should be nil by default
	if tree.GC() != nil {
		t.Fatal("GC should be nil by default")
	}

	// CollectGarbage should be a no-op
	deleted, err := tree.CollectGarbage(store)
	if err != nil || deleted != 0 {
		t.Fatalf("expected no-op, got deleted=%d err=%v", deleted, err)
	}
}

func TestGCReset(t *testing.T) {
	gc := NewNodeGC()
	h := Blake3Hasher{}.Hash([]byte("test"))
	gc.IncRef(h)
	gc.DecRef(h)

	gc.Reset()
	if gc.TrackedNodeCount() != 0 || gc.PendingDeleteCount() != 0 {
		t.Fatal("Reset should clear all state")
	}
}
