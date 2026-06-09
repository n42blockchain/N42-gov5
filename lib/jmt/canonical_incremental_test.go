package jmt

import (
	"encoding/binary"
	"testing"
)

// h32 builds a deterministic 32-byte key hash from a seed.
func h32(seed uint64) Hash {
	var h Hash
	binary.BigEndian.PutUint64(h[24:], seed)
	// spread some entropy into the high nibbles so keys diverge early in the trie
	binary.BigEndian.PutUint64(h[0:], seed*0x9E3779B97F4A7C15)
	return h
}

func v32(seed uint64) []byte {
	b := make([]byte, 32)
	binary.BigEndian.PutUint64(b[24:], seed+1)
	return b
}

// TestJMTIncrementalEqualsBatch checks the fundamental invariant: the JMT root
// must be a deterministic function of the live key→value set, independent of the
// path taken to reach it. A tree built by many small incremental BatchUpdates
// (with intervening deletes/overwrites) must hash-equal a single batch built over
// the identical final leaf set. A divergence means incremental updates leave the
// tree in a non-canonical structure (e.g. single-child internal nodes not
// collapsed), which makes header.Root disagree with a from-snapshot rebuild.
func TestJMTIncrementalEqualsBatch(t *testing.T) {
	const N = 400

	// Incremental tree: insert, overwrite, delete-and-reinsert across "blocks".
	inc := New(NewMemStore())
	live := map[uint64][]byte{}
	step := uint64(0)
	commit := func() {
		step++
		inc.SetVersion(step)
	}

	// Phase 1: insert N keys, one small batch per 10 keys.
	for i := uint64(0); i < N; i++ {
		if _, err := inc.BatchUpdate([]BatchEntry{{KeyHash: h32(i), Value: v32(i)}}); err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
		live[i] = v32(i)
		commit()
	}
	// Phase 2: delete every 3rd key.
	for i := uint64(0); i < N; i += 3 {
		if _, err := inc.BatchUpdate([]BatchEntry{{KeyHash: h32(i), Value: nil}}); err != nil {
			t.Fatalf("del %d: %v", i, err)
		}
		delete(live, i)
		commit()
	}
	// Phase 3: overwrite every 5th surviving key.
	for i := uint64(1); i < N; i += 5 {
		if _, ok := live[i]; ok {
			nv := v32(i + 1000)
			if _, err := inc.BatchUpdate([]BatchEntry{{KeyHash: h32(i), Value: nv}}); err != nil {
				t.Fatalf("ovw %d: %v", i, err)
			}
			live[i] = nv
			commit()
		}
	}

	incRoot := inc.Root()

	// Canonical batch over the identical final live set.
	batch := New(NewMemStore())
	entries := make([]BatchEntry, 0, len(live))
	for k, val := range live {
		entries = append(entries, BatchEntry{KeyHash: h32(k), Value: val})
	}
	batchRoot, err := batch.BatchUpdate(entries)
	if err != nil {
		t.Fatalf("batch: %v", err)
	}

	if incRoot != batchRoot {
		t.Fatalf("incremental root != canonical batch root over identical leaves\n  incremental = %x\n  batch       = %x\n  liveKeys=%d",
			incRoot[:], batchRoot[:], len(live))
	}
}

// TestJMTIncrementalEqualsBatch_RandomChurn hammers the invariant with many
// rounds of pseudo-random insert/delete/overwrite churn (deterministic LCG, no
// external randomness), comparing the incremental tree to a fresh single-batch
// build over the live set after every round. Any divergence means a delete or
// update left the tree in a non-canonical shape.
func TestJMTIncrementalEqualsBatch_RandomChurn(t *testing.T) {
	inc := New(NewMemStore())
	live := map[uint64][]byte{}
	rng := uint64(0x243F6A8885A308D3) // deterministic seed
	next := func() uint64 {
		rng = rng*6364136223846793005 + 1442695040888963407
		return rng >> 11
	}

	const keySpace = 500
	const rounds = 60
	ver := uint64(0)
	for r := 0; r < rounds; r++ {
		// 20 random ops per round.
		for op := 0; op < 20; op++ {
			k := next() % keySpace
			switch next() % 3 {
			case 0, 1: // insert or overwrite
				val := v32(next())
				if _, err := inc.BatchUpdate([]BatchEntry{{KeyHash: h32(k), Value: val}}); err != nil {
					t.Fatalf("put %d: %v", k, err)
				}
				live[k] = val
			default: // delete
				if _, err := inc.BatchUpdate([]BatchEntry{{KeyHash: h32(k), Value: nil}}); err != nil {
					t.Fatalf("del %d: %v", k, err)
				}
				delete(live, k)
			}
		}
		ver++
		inc.SetVersion(ver)

		// Canonical batch over the current live set.
		batch := New(NewMemStore())
		entries := make([]BatchEntry, 0, len(live))
		for k, val := range live {
			entries = append(entries, BatchEntry{KeyHash: h32(k), Value: val})
		}
		var batchRoot Hash
		if len(entries) > 0 {
			var err error
			batchRoot, err = batch.BatchUpdate(entries)
			if err != nil {
				t.Fatalf("round %d batch: %v", r, err)
			}
		} else {
			batchRoot = EmptyHash
		}

		if inc.Root() != batchRoot {
			t.Fatalf("round %d: incremental != batch (liveKeys=%d)\n  inc   = %x\n  batch = %x",
				r, len(live), inc.Root(), batchRoot[:])
		}

		// Every live key must still be retrievable with the right value.
		for k, want := range live {
			got, err := inc.Get(h32(k))
			if err != nil {
				t.Fatalf("round %d: get %d: %v", r, k, err)
			}
			if !bytesEq(got, want) {
				t.Fatalf("round %d: get %d value mismatch", r, k)
			}
		}
	}
}

func bytesEq(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
