package sync

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

func TestShardedAddressMap(t *testing.T) {
	m := NewShardedAddressMap[int]()

	addr1 := types.Address{1, 2, 3}
	addr2 := types.Address{4, 5, 6}

	if _, ok := m.Get(addr1); ok {
		t.Fatalf("expected miss on empty map")
	}
	if m.Has(addr1) {
		t.Fatalf("expected Has() false on empty map")
	}

	m.Set(addr1, 100)
	m.Set(addr2, 200)

	if v, ok := m.Get(addr1); !ok || v != 100 {
		t.Fatalf("Get(addr1) = %v, %v; want 100, true", v, ok)
	}
	if !m.Has(addr1) {
		t.Fatalf("expected Has(addr1) true")
	}
	if m.Len() != 2 {
		t.Fatalf("Len() = %d, want 2", m.Len())
	}

	seen := make(map[types.Address]int)
	m.Range(func(addr types.Address, value int) bool {
		seen[addr] = value
		return true
	})
	if len(seen) != 2 || seen[addr1] != 100 || seen[addr2] != 200 {
		t.Fatalf("Range did not visit all entries: %v", seen)
	}

	// Early stop.
	count := 0
	m.Range(func(addr types.Address, value int) bool {
		count++
		return false
	})
	if count != 1 {
		t.Fatalf("expected Range to stop after first callback, got count=%d", count)
	}

	m.Delete(addr1)
	if _, ok := m.Get(addr1); ok {
		t.Fatalf("expected addr1 deleted")
	}
	if m.Len() != 1 {
		t.Fatalf("Len() after delete = %d, want 1", m.Len())
	}
}

func TestShardedHashMap(t *testing.T) {
	m := NewShardedHashMap[string]()

	h1 := types.Hash{10, 20}
	h2 := types.Hash{30, 40}

	if _, ok := m.Get(h1); ok {
		t.Fatalf("expected miss on empty map")
	}

	m.Set(h1, "a")
	m.Set(h2, "b")

	if v, ok := m.Get(h1); !ok || v != "a" {
		t.Fatalf("Get(h1) = %v, %v; want a, true", v, ok)
	}

	m.Delete(h1)
	if _, ok := m.Get(h1); ok {
		t.Fatalf("expected h1 deleted")
	}
	if v, ok := m.Get(h2); !ok || v != "b" {
		t.Fatalf("Get(h2) = %v, %v; want b, true", v, ok)
	}
}

func TestShardedStringMap(t *testing.T) {
	m := NewShardedStringMap[int]()

	if _, ok := m.Get("missing"); ok {
		t.Fatalf("expected miss on empty map")
	}

	m.Set("foo", 1)
	m.Set("bar", 2)

	if v, ok := m.Get("foo"); !ok || v != 1 {
		t.Fatalf("Get(foo) = %v, %v; want 1, true", v, ok)
	}
	if v, ok := m.Get("bar"); !ok || v != 2 {
		t.Fatalf("Get(bar) = %v, %v; want 2, true", v, ok)
	}

	m.Delete("foo")
	if _, ok := m.Get("foo"); ok {
		t.Fatalf("expected foo deleted")
	}
}

func TestShardedMapConcurrentAccess(t *testing.T) {
	m := NewShardedAddressMap[int]()
	done := make(chan struct{})
	addr := types.Address{9, 9, 9}

	go func() {
		for i := 0; i < 1000; i++ {
			m.Set(addr, i)
		}
		close(done)
	}()
	for i := 0; i < 1000; i++ {
		m.Get(addr)
	}
	<-done
}
