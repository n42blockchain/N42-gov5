// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"bytes"
	"math/rand"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/types"
)

// TestSortAddressesByBucket_FFOverflow pins the int(byte)+1 invariant in
// the counting-sort pass. The pre-fix code did `counts[addr[0]+1]++` —
// `byte+1` wraps to 0 for the 0xFF bucket, scrambling the bucket layout
// and panicking with "index out of range" once 0xFF-prefixed addresses
// overflow their (undersized) target slot during distribution.
//
// Production hit at block 10700000 with 1.1 M accounts.
func TestSortAddressesByBucket_FFOverflow(t *testing.T) {
	// Need to exceed the 100k parallel threshold to actually hit the
	// counting-sort path (small inputs short-circuit to sort.Slice).
	const n = 200_000
	rng := rand.New(rand.NewSource(0xff42))

	addrs := make([]types.Address, n)
	for i := range addrs {
		rng.Read(addrs[i][:])
	}
	// Force ~25% to start with 0xFF so the previously buggy bucket is
	// densely populated.
	for i := 0; i < n/4; i++ {
		addrs[i][0] = 0xff
	}
	rng.Shuffle(n, func(i, j int) { addrs[i], addrs[j] = addrs[j], addrs[i] })

	// Snapshot expected order via stdlib sort.
	want := make([]types.Address, n)
	copy(want, addrs)
	sort.Slice(want, func(i, j int) bool {
		return bytes.Compare(want[i][:], want[j][:]) < 0
	})

	sortAddressesByBucket(addrs)

	for i := range addrs {
		require.Equal(t, want[i], addrs[i],
			"sortAddressesByBucket diverged from sort.Slice at index %d", i)
	}
}

// TestSortStoEntriesByBucket_FFOverflow is the storage-key analogue.
func TestSortStoEntriesByBucket_FFOverflow(t *testing.T) {
	const n = 300_000 // exceed the 200k storage threshold
	rng := rand.New(rand.NewSource(0xbeef))

	entries := make([]stoKV, n)
	for i := range entries {
		k := make([]byte, storageCompositeKeyLen)
		rng.Read(k)
		entries[i].key = k
		entries[i].value = []byte{byte(i)}
	}
	for i := 0; i < n/4; i++ {
		entries[i].key[0] = 0xff
	}
	rng.Shuffle(n, func(i, j int) { entries[i], entries[j] = entries[j], entries[i] })

	want := make([]stoKV, n)
	copy(want, entries)
	sort.Slice(want, func(i, j int) bool {
		return bytes.Compare(want[i].key, want[j].key) < 0
	})

	sortStoEntriesByBucket(entries)

	for i := range entries {
		require.Equal(t, want[i].key, entries[i].key,
			"sortStoEntriesByBucket key diverged at index %d", i)
		require.Equal(t, want[i].value, entries[i].value,
			"sortStoEntriesByBucket value diverged at index %d", i)
	}
}
