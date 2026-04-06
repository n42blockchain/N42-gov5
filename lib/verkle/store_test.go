package verkle

import (
	"testing"

	gverkle "github.com/ethereum/go-verkle"
	"lukechampine.com/blake3"
)

func TestMemStoreBasic(t *testing.T) {
	s := NewMemStore()

	var key CommitmentKey
	h := blake3.Sum256([]byte("test"))
	copy(key[:], h[:])

	// Miss
	_, err := s.Get(key)
	if err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	ok, _ := s.Has(key)
	if ok {
		t.Fatal("expected Has=false")
	}

	// Put + Get
	data := []byte{1, 2, 3}
	if err := s.Put(key, data); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(key)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0] != 1 {
		t.Fatalf("expected [1,2,3], got %v", got)
	}
	ok, _ = s.Has(key)
	if !ok {
		t.Fatal("expected Has=true")
	}
	if s.Len() != 1 {
		t.Fatalf("expected Len=1, got %d", s.Len())
	}
}

func TestFlushToMemStore(t *testing.T) {
	// Build a small Verkle tree and flush it.
	root := gverkle.New()

	// Insert a few keys
	for i := byte(0); i < 5; i++ {
		key := blake3.Sum256([]byte{i})
		val := [32]byte{i + 1}
		if err := root.Insert(key[:], val[:], nil); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	root.Commit()

	store := NewMemStore()
	nodes, bytes, err := Flush(root, store)
	if err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if nodes == 0 {
		t.Fatal("expected nodes > 0")
	}
	if bytes == 0 {
		t.Fatal("expected bytes > 0")
	}
	if store.Len() != nodes {
		t.Fatalf("store.Len()=%d != nodes=%d", store.Len(), nodes)
	}
	t.Logf("Flushed %d nodes, %d bytes", nodes, bytes)
}
