// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package parallel

import (
	"fmt"
	"math/rand"
	"sort"
	"testing"

	"github.com/holiman/uint256"
)

// arenaBlockOutcome is a block's applied store: every location's final
// value or delta, in key order.
type arenaBlockOutcome []string

// runArenaBlock executes a block of transfers over a hot set of recipients
// (credited by delta, so their lists grow across many classes) and chained
// senders (full writes), then applies and releases the store.
func runArenaBlock(t *testing.T, seed int64, numTxs int) arenaBlockOutcome {
	rng := rand.New(rand.NewSource(seed))
	senders, recips := 1+rng.Intn(40), 1+rng.Intn(300)
	from := make([]int, numTxs)
	to := make([]int, numTxs)
	amt := make([]uint64, numTxs)
	for i := range from {
		from[i] = rng.Intn(senders)
		// A skewed recipient: a few take most of the block's credits.
		to[i] = int(float64(recips) * rng.Float64() * rng.Float64() * rng.Float64())
		amt[i] = uint64(1 + rng.Intn(1000))
	}
	key := func(kind byte, n int) LocationKey {
		k := LocationKey{Field: FieldBalance}
		k.Address[0] = kind
		k.Address[1], k.Address[2] = byte(n), byte(n>>8)
		return k
	}
	var ex *Executor
	ex = NewExecutor(numTxs, 4, func(i int, rw *ReadWriteSet) error {
		s := key(1, from[i])
		v, wtx, winc, found := ex.MVS().Read(s, i)
		if found {
			rw.RecordRead(s, wtx, winc, false)
		} else {
			rw.RecordRead(s, -1, 0, true)
			v = make([]byte, 8)
		}
		nv := append([]byte(nil), v...)
		nv[0]++
		rw.RecordWrite(s, nv)
		rw.RecordDeltaWrite(key(2, to[i]), uint256.NewInt(amt[i]))
		return nil
	})
	ex.SetAffinity(func(i int) uint64 { return uint64(from[i]) })
	for i, r := range ex.Run() {
		if r.Err != nil {
			t.Fatalf("tx %d: %v", i, r.Err)
		}
	}
	var out arenaBlockOutcome
	if err := ex.MVS().ApplyAll(numTxs, func(k LocationKey, value []byte, delta *uint256.Int) error {
		d := "-"
		if delta != nil {
			d = delta.Dec()
		}
		out = append(out, fmt.Sprintf("%x/%d=%x+%s", k.Address[:3], k.Field, value, d))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	ex.Release()
	return out
}

// TestVersionArenaReuseMatchesFresh: blocks run on stores whose version
// lists come from arenas reused across blocks apply exactly what stores
// growing their lists by append apply, block after block (the arenas are
// released and taken again between them, of other sizes).
func TestVersionArenaReuseMatchesFresh(t *testing.T) {
	for round := 0; round < 12; round++ {
		seed := int64(round*7919 + 1)
		n := 50 + (round*977)%3000
		restore := SetScratchPoolForTest(false)
		want := runArenaBlock(t, seed, n)
		restore()
		restore = SetScratchPoolForTest(true)
		got := runArenaBlock(t, seed, n)
		restore()
		if len(want) != len(got) {
			t.Fatalf("round %d: %d locations fresh, %d with the arena", round, len(want), len(got))
		}
		for i := range want {
			if want[i] != got[i] {
				t.Fatalf("round %d: location %d: fresh %s, arena %s", round, i, want[i], got[i])
			}
		}
	}
}

// TestVersionShardCarving: lists of every class come back empty with
// their class's capacity, never overlap, and a list given back is reused
// for its class; reset clears the slabs.
func TestVersionShardCarving(t *testing.T) {
	var a versionArena
	s := &a.shards[0]
	seen := map[*versionedValue]bool{}
	var lists [][]versionedValue
	for c := 0; c < 11; c++ {
		for r := 0; r < 3; r++ {
			l := s.alloc(c)
			if len(l) != 0 || cap(l) != 1<<c {
				t.Fatalf("class %d: len %d cap %d", c, len(l), cap(l))
			}
			full := l[:cap(l)]
			for i := range full {
				if seen[&full[i]] {
					t.Fatalf("class %d: overlapping lists", c)
				}
				seen[&full[i]] = true
				full[i] = versionedValue{txIndex: c, value: []byte{byte(c)}}
			}
			lists = append(lists, l)
		}
	}
	back := lists[4]
	s.give(back)
	if again := s.alloc(versionClass(cap(back))); &again[:1][0] != &back[:1][0] {
		t.Fatal("a given-back list was not reused for its class")
	}
	a.reset()
	for _, sl := range s.slabs {
		for i := range sl {
			if sl[i].value != nil || sl[i].txIndex != 0 {
				t.Fatal("reset left a version in a slab")
			}
		}
	}
	if l := s.alloc(0); &l[:1][0] != &s.slabs[0][0] {
		t.Fatal("reset did not restart carving at the first slab")
	}
}
