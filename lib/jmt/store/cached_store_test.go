// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package store

import (
	"bytes"
	"testing"

	"github.com/n42blockchain/N42/lib/jmt"
)

func TestCachedStoreGetPut(t *testing.T) {
	inner := jmt.NewMemStore()
	cs := NewCachedStore(inner, 64)

	h := jmt.Hash{0x01}
	data := []byte("node-data")

	// Put via inner directly, then Get through cached store.
	if err := inner.Put(h, data); err != nil {
		t.Fatal(err)
	}

	// First Get — cache miss, reads from inner.
	got, err := cs.Get(h)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("Get: got %x, want %x", got, data)
	}

	// Second Get — cache hit.
	got2, err := cs.Get(h)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got2, data) {
		t.Fatalf("Get (cached): got %x, want %x", got2, data)
	}

	hits, misses := cs.Stats()
	if hits != 1 {
		t.Errorf("expected 1 hit, got %d", hits)
	}
	if misses != 1 {
		t.Errorf("expected 1 miss, got %d", misses)
	}

	// Put through cached store.
	h2 := jmt.Hash{0x02}
	data2 := []byte("node-data-2")
	if err := cs.Put(h2, data2); err != nil {
		t.Fatal(err)
	}

	// Should be available in both cache and inner.
	got3, err := cs.Get(h2)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got3, data2) {
		t.Fatalf("Get after Put: got %x, want %x", got3, data2)
	}

	// Verify inner also has it.
	innerGot, err := inner.Get(h2)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(innerGot, data2) {
		t.Fatal("inner store missing data after Put")
	}
}

func TestCachedStoreEviction(t *testing.T) {
	inner := jmt.NewMemStore()
	cs := NewCachedStore(inner, 3)

	// Fill cache to capacity.
	hashes := make([]jmt.Hash, 4)
	for i := range hashes {
		hashes[i] = jmt.Hash{byte(i + 1)}
		if err := cs.Put(hashes[i], []byte{byte(i + 1)}); err != nil {
			t.Fatal(err)
		}
	}

	// The first entry (hash 0x01) should have been evicted.
	// Getting it should be a cache miss (reads from inner).
	_, misses1 := cs.Stats()
	_, err := cs.Get(hashes[0])
	if err != nil {
		t.Fatal(err)
	}
	_, misses2 := cs.Stats()
	if misses2 <= misses1 {
		t.Error("expected a cache miss for evicted entry")
	}

	// The latest entry (hash 0x04) should still be cached.
	hits1, _ := cs.Stats()
	_, err = cs.Get(hashes[3])
	if err != nil {
		t.Fatal(err)
	}
	hits2, _ := cs.Stats()
	if hits2 <= hits1 {
		t.Error("expected a cache hit for recent entry")
	}
}

func TestCachedStorePopulate(t *testing.T) {
	inner := jmt.NewMemStore()
	cs := NewCachedStore(inner, 64)

	h := jmt.Hash{0xAA}
	data := []byte("populated-data")

	// Populate without writing to inner.
	cs.Populate(h, data)

	// Inner should NOT have the data.
	_, err := inner.Get(h)
	if err == nil {
		t.Fatal("expected inner to NOT have populated data")
	}

	// Cache should have it.
	got, err := cs.Get(h)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("Populate/Get: got %x, want %x", got, data)
	}

	hits, _ := cs.Stats()
	if hits != 1 {
		t.Errorf("expected 1 hit after Populate+Get, got %d", hits)
	}
}

func TestCachedStoreStats(t *testing.T) {
	inner := jmt.NewMemStore()
	cs := NewCachedStore(inner, 64)

	h := jmt.Hash{0x10}
	if err := inner.Put(h, []byte("val")); err != nil {
		t.Fatal(err)
	}

	// Miss.
	cs.Get(h) //nolint:errcheck
	hits, misses := cs.Stats()
	if hits != 0 || misses != 1 {
		t.Fatalf("after miss: hits=%d misses=%d, want 0/1", hits, misses)
	}

	// Hit.
	cs.Get(h) //nolint:errcheck
	hits, misses = cs.Stats()
	if hits != 1 || misses != 1 {
		t.Fatalf("after hit: hits=%d misses=%d, want 1/1", hits, misses)
	}

	// Another miss (key not found).
	cs.Get(jmt.Hash{0xFF}) //nolint:errcheck
	hits, misses = cs.Stats()
	if hits != 1 || misses != 2 {
		t.Fatalf("after not-found miss: hits=%d misses=%d, want 1/2", hits, misses)
	}
}

func TestCachedStoreDelete(t *testing.T) {
	inner := jmt.NewMemStore()
	cs := NewCachedStore(inner, 64)

	h := jmt.Hash{0x20}
	if err := cs.Put(h, []byte("to-delete")); err != nil {
		t.Fatal(err)
	}

	if err := cs.Delete(h); err != nil {
		t.Fatal(err)
	}

	// Should not exist in cache or inner.
	ok, err := cs.Has(h)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("expected Has to return false after Delete")
	}
}

func TestCachedStoreHas(t *testing.T) {
	inner := jmt.NewMemStore()
	cs := NewCachedStore(inner, 64)

	h := jmt.Hash{0x30}

	ok, err := cs.Has(h)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("expected Has=false for missing key")
	}

	// Put to inner only.
	if err := inner.Put(h, []byte("exists")); err != nil {
		t.Fatal(err)
	}
	ok, err = cs.Has(h)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected Has=true for key in inner")
	}

	// Populate to cache only.
	h2 := jmt.Hash{0x31}
	cs.Populate(h2, []byte("cached"))
	ok, err = cs.Has(h2)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected Has=true for key in cache")
	}
}
