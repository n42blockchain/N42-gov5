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

// TestSortAddressesByBucket_PropertyRandom runs many random batches
// and asserts the parallel-bucket sort matches stdlib sort.Slice
// byte-for-byte. This complements the FFOverflow targeted test by
// catching any subtle ordering bug that only surfaces under specific
// distributions (e.g. all-same-first-byte, asymmetric clustering,
// edge cases at bucket boundaries).
func TestSortAddressesByBucket_PropertyRandom(t *testing.T) {
	for trial := 0; trial < 64; trial++ {
		// Vary size to cross the parallel threshold (100k) and beyond.
		sizes := []int{50_000, 110_000, 250_000, 500_000}
		n := sizes[trial%len(sizes)]
		seed := int64(0xc0ffee00 + trial)
		rng := rand.New(rand.NewSource(seed))

		addrs := make([]types.Address, n)
		for i := range addrs {
			rng.Read(addrs[i][:])
		}
		// Randomly cluster a portion at a single first byte to
		// exercise non-uniform bucket loads.
		if trial%4 == 0 {
			pin := byte(rng.Intn(256))
			for i := 0; i < n/3; i++ {
				addrs[i][0] = pin
			}
			rng.Shuffle(n, func(i, j int) { addrs[i], addrs[j] = addrs[j], addrs[i] })
		}

		want := make([]types.Address, n)
		copy(want, addrs)
		sort.Slice(want, func(i, j int) bool {
			return bytes.Compare(want[i][:], want[j][:]) < 0
		})

		sortAddressesByBucket(addrs)

		// Compare; on first divergence dump details.
		for i := range addrs {
			if addrs[i] != want[i] {
				t.Fatalf("trial=%d seed=%d size=%d index=%d: bucket=%v want=%v",
					trial, seed, n, i, addrs[i], want[i])
			}
		}
	}
}

// TestSortStoEntriesByBucket_PropertyRandom is the storage analogue.
func TestSortStoEntriesByBucket_PropertyRandom(t *testing.T) {
	for trial := 0; trial < 32; trial++ {
		sizes := []int{100_000, 250_000, 500_000, 1_000_000}
		n := sizes[trial%len(sizes)]
		seed := int64(0xbeef0000 + trial)
		rng := rand.New(rand.NewSource(seed))

		entries := make([]stoKV, n)
		for i := range entries {
			k := make([]byte, storageCompositeKeyLen)
			rng.Read(k)
			entries[i].key = k
			entries[i].value = []byte{byte(i), byte(i >> 8)}
		}
		// Cluster pattern to stress non-uniform buckets.
		if trial%3 == 0 {
			pin := byte(rng.Intn(256))
			for i := 0; i < n/4; i++ {
				entries[i].key[0] = pin
			}
		}

		want := make([]stoKV, n)
		copy(want, entries)
		sort.Slice(want, func(i, j int) bool {
			return bytes.Compare(want[i].key, want[j].key) < 0
		})

		sortStoEntriesByBucket(entries)

		// Verify monotonic — easier to spot non-sorted runs.
		for i := 1; i < len(entries); i++ {
			if bytes.Compare(entries[i-1].key, entries[i].key) > 0 {
				t.Fatalf("trial=%d seed=%d size=%d: NOT sorted at index %d (key[i-1]=%x, key[i]=%x)",
					trial, seed, n, i, entries[i-1].key, entries[i].key)
			}
		}
		// Verify exact match.
		for i := range entries {
			if !bytes.Equal(entries[i].key, want[i].key) {
				t.Fatalf("trial=%d seed=%d size=%d index=%d: bucket key=%x want key=%x",
					trial, seed, n, i, entries[i].key, want[i].key)
			}
		}
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
