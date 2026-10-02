// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// WarmupCache had no test file at all. Covers the full surface: enable/disable
// gating every operation, branch/account/storage put-get-evict cycles, the
// combined get-and-evict operations, EvictPlainKey touching both account and
// storage entries, and Clear dropping everything.

package commitment

import (
	"testing"
)

func TestWarmupCacheDisabledNoOps(t *testing.T) {
	c := NewWarmupCache()
	if !c.IsEnabled() {
		t.Fatal("NewWarmupCache must start enabled")
	}
	c.Enable(false)
	if c.IsEnabled() {
		t.Fatal("Enable(false) must disable the cache")
	}

	c.PutBranch([]byte("p"), []byte("d"))
	if _, ok := c.GetBranch([]byte("p")); ok {
		t.Fatal("GetBranch must report !ok while disabled")
	}
	if _, ok := c.GetAndEvictBranch([]byte("p")); ok {
		t.Fatal("GetAndEvictBranch must report !ok while disabled")
	}
	c.EvictBranch([]byte("p")) // must not panic

	c.PutAccount([]byte("a"), &Update{Nonce: 1})
	if _, ok := c.GetAccount([]byte("a")); ok {
		t.Fatal("GetAccount must report !ok while disabled")
	}
	if c.GetAndEvictAccount([]byte("a")) != nil {
		t.Fatal("GetAndEvictAccount must return nil while disabled")
	}
	c.EvictAccount([]byte("a"))

	c.PutStorage([]byte("s"), &Update{Nonce: 2})
	if _, ok := c.GetStorage([]byte("s")); ok {
		t.Fatal("GetStorage must report !ok while disabled")
	}
	if c.GetAndEvictStorage([]byte("s")) != nil {
		t.Fatal("GetAndEvictStorage must return nil while disabled")
	}
	c.EvictStorage([]byte("s"))
	c.EvictPlainKey([]byte("a")) // must not panic
}

func TestWarmupCacheBranchLifecycle(t *testing.T) {
	c := NewWarmupCache()
	prefix := []byte{1, 2, 3}

	if _, ok := c.GetBranch(prefix); ok {
		t.Fatal("expected a miss before any Put")
	}

	c.PutBranch(prefix, []byte("branch-data"))
	data, ok := c.GetBranch(prefix)
	if !ok || string(data) != "branch-data" {
		t.Fatalf("GetBranch = (%q,%v), want (branch-data,true)", data, ok)
	}

	// PutBranch must copy the input slice (mutating the original afterward
	// must not affect the cached copy).
	orig := []byte("mutate-me")
	c.PutBranch([]byte{9}, orig)
	orig[0] = 'X'
	cached, _ := c.GetBranch([]byte{9})
	if string(cached) != "mutate-me" {
		t.Fatalf("cached branch data was mutated via the caller's slice: %q", cached)
	}

	got, ok := c.GetAndEvictBranch(prefix)
	if !ok || string(got) != "branch-data" {
		t.Fatalf("GetAndEvictBranch = (%q,%v), want (branch-data,true)", got, ok)
	}
	if _, ok := c.GetBranch(prefix); ok {
		t.Fatal("expected a miss after GetAndEvictBranch")
	}
	if _, ok := c.GetAndEvictBranch(prefix); ok {
		t.Fatal("expected a second GetAndEvictBranch to miss (already evicted)")
	}

	c.PutBranch([]byte{7}, []byte("v"))
	c.EvictBranch([]byte{7})
	if _, ok := c.GetBranch([]byte{7}); ok {
		t.Fatal("expected a miss after EvictBranch")
	}
	// Evicting an absent key must not panic.
	c.EvictBranch([]byte{200})
}

func TestWarmupCacheAccountLifecycle(t *testing.T) {
	c := NewWarmupCache()
	key := []byte("addr1")
	upd := &Update{Nonce: 42}

	c.PutAccount(key, upd)
	got, ok := c.GetAccount(key)
	if !ok || got.Nonce != 42 {
		t.Fatalf("GetAccount = (%+v,%v), want nonce 42", got, ok)
	}
	// Must be a deep copy: mutating the original must not affect the cache.
	upd.Nonce = 999
	got2, _ := c.GetAccount(key)
	if got2.Nonce != 42 {
		t.Fatalf("cached account was affected by mutating the original: nonce=%d", got2.Nonce)
	}

	// PutAccount with a nil update stores a nil update (deletion marker).
	c.PutAccount([]byte("deleted"), nil)
	gotNil, ok := c.GetAccount([]byte("deleted"))
	if !ok || gotNil != nil {
		t.Fatalf("GetAccount(deleted) = (%v,%v), want (nil,true)", gotNil, ok)
	}

	entry := c.GetAndEvictAccount(key)
	if entry == nil || entry.update.Nonce != 42 {
		t.Fatalf("GetAndEvictAccount = %+v, want nonce 42", entry)
	}
	if _, ok := c.GetAccount(key); ok {
		t.Fatal("expected a miss after GetAndEvictAccount")
	}
	if c.GetAndEvictAccount(key) != nil {
		t.Fatal("expected nil on a second GetAndEvictAccount (already evicted)")
	}

	c.PutAccount([]byte("to-evict"), &Update{Nonce: 1})
	c.EvictAccount([]byte("to-evict"))
	if _, ok := c.GetAccount([]byte("to-evict")); ok {
		t.Fatal("expected a miss after EvictAccount")
	}
	c.EvictAccount([]byte("never-put")) // must not panic
}

func TestWarmupCacheStorageLifecycle(t *testing.T) {
	c := NewWarmupCache()
	key := []byte("slot1")
	upd := &Update{Nonce: 7}

	c.PutStorage(key, upd)
	got, ok := c.GetStorage(key)
	if !ok || got.Nonce != 7 {
		t.Fatalf("GetStorage = (%+v,%v), want nonce 7", got, ok)
	}

	entry := c.GetAndEvictStorage(key)
	if entry == nil || entry.update.Nonce != 7 {
		t.Fatalf("GetAndEvictStorage = %+v, want nonce 7", entry)
	}
	if _, ok := c.GetStorage(key); ok {
		t.Fatal("expected a miss after GetAndEvictStorage")
	}
	if c.GetAndEvictStorage(key) != nil {
		t.Fatal("expected nil on a second GetAndEvictStorage (already evicted)")
	}

	c.PutStorage([]byte("to-evict"), &Update{Nonce: 3})
	c.EvictStorage([]byte("to-evict"))
	if _, ok := c.GetStorage([]byte("to-evict")); ok {
		t.Fatal("expected a miss after EvictStorage")
	}
	c.EvictStorage([]byte("never-put")) // must not panic
}

func TestWarmupCacheEvictPlainKeyAndClear(t *testing.T) {
	c := NewWarmupCache()
	key := []byte("shared-key")
	c.PutAccount(key, &Update{Nonce: 1})
	c.PutStorage(key, &Update{Nonce: 2})

	c.EvictPlainKey(key)
	if _, ok := c.GetAccount(key); ok {
		t.Fatal("EvictPlainKey must evict the account entry")
	}
	if _, ok := c.GetStorage(key); ok {
		t.Fatal("EvictPlainKey must evict the storage entry")
	}

	c.PutBranch([]byte("b"), []byte("v"))
	c.PutAccount([]byte("a"), &Update{Nonce: 1})
	c.PutStorage([]byte("s"), &Update{Nonce: 1})
	c.Clear()
	if _, ok := c.GetBranch([]byte("b")); ok {
		t.Fatal("Clear must drop branch entries")
	}
	if _, ok := c.GetAccount([]byte("a")); ok {
		t.Fatal("Clear must drop account entries")
	}
	if _, ok := c.GetStorage([]byte("s")); ok {
		t.Fatal("Clear must drop storage entries")
	}
}
