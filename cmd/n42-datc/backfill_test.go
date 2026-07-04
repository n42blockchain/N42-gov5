// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package main

import (
	"math/rand"
	"testing"
)

// newMarkBuilder builds the minimal builder state recordChange /
// backfill*Marks touch, with a small schedule so epochs roll frequently.
func newMarkBuilder(t *testing.T) *builder {
	t.Helper()
	b := &builder{}
	b.sched = epochSchedule{e: [maxChgDepth + 1]uint64{4, 8, 16, 1, 64, 64}}
	size := 1
	for d := 0; d <= maxChgDepth; d++ {
		b.accDirty[d] = make([]uint16, size)
		b.chgAccAgg[d] = make([]chgSlot, size)
		b.stoDirty[d] = make(map[string]*uint16, 16)
		size *= 16
	}
	b.chgStoAgg = make(map[string]*[]chgEvent)
	return b
}

// clearEpochMarks mimics what flushEpoch/flushStoLevel do to the mark state
// at an epoch boundary for level d: consume and reset.
func clearEpochMarks(b *builder, d int) {
	for _, idx := range b.accTouched[d] {
		b.accDirty[d][idx] = 0
	}
	b.accTouched[d] = b.accTouched[d][:0]
	b.stoDirty[d] = make(map[string]*uint16, 16)
}

// clearAllMarks mimics a process restart: every in-memory mark is gone.
func clearAllMarks(b *builder) {
	for d := 0; d <= maxChgDepth; d++ {
		clearEpochMarks(b, d)
		// chg agg state is irrelevant here (drained at every batch commit in
		// the real build), but reset it for symmetry.
		for i := range b.chgAccAgg[d] {
			b.chgAccAgg[d][i] = chgSlot{}
		}
		b.chgAccAggTouched[d] = b.chgAccAggTouched[d][:0]
	}
	b.chgStoAgg = make(map[string]*[]chgEvent)
	b.chgStoAggBytes = 0
}

type markEvent struct {
	acc  bool
	nibs []byte // 64 nibbles (account hashed key or slot hashed key)
	dom  []byte // storage only: 40-byte domain
}

// applyLive replays one block's events the way the live build does, then
// rolls epoch boundaries exactly like the main loop (flush after block n when
// (n+1)%e[d]==0).
func applyLive(b *builder, n uint64, evs []markEvent) {
	for _, e := range evs {
		if e.acc {
			b.recordChange(false, nil, e.nibs, n)
		} else {
			b.recordChangeStorage(e.dom, e.nibs, n)
		}
	}
	for d := 0; d <= maxChgDepth; d++ {
		if (n+1)%b.sched.e[d] == 0 {
			clearEpochMarks(b, d)
		}
	}
}

func randNibs(r *rand.Rand) []byte {
	n := make([]byte, 64)
	for i := range n {
		n[i] = byte(r.Intn(16))
	}
	return n
}

// TestBackfillRestoresMarks: run the live mark path to block S and to block
// END on one builder; on a second builder stop ("crash") at S, wipe all
// in-memory state, backfill [minEpochStart(S), S), and continue to END. At
// every epoch flush point after S the two builders' pending mark sets must be
// identical — i.e. the records the epoch flush would emit are identical.
func TestBackfillRestoresMarks(t *testing.T) {
	r := rand.New(rand.NewSource(42))
	const END = 200
	// S deliberately mid-epoch for every level with e>1 (4,8,16,64):
	const S = 71 // 71%4=3, 71%8=7, 71%16=7, 71%64=7

	// Pre-generate the event stream so both runs see identical input.
	blocks := make([][]markEvent, END)
	// A small pool of hot keys plus fresh random ones each block.
	hotA := make([][]byte, 8)
	hotS := make([][]byte, 8)
	hotD := make([][]byte, 4)
	for i := range hotA {
		hotA[i] = randNibs(r)
		hotS[i] = randNibs(r)
	}
	for i := range hotD {
		d := make([]byte, 40)
		r.Read(d[:32])
		hotD[i] = d
	}
	for n := 0; n < END; n++ {
		cnt := 1 + r.Intn(6)
		evs := make([]markEvent, 0, cnt)
		for i := 0; i < cnt; i++ {
			switch r.Intn(4) {
			case 0: // hot account
				evs = append(evs, markEvent{acc: true, nibs: hotA[r.Intn(len(hotA))]})
			case 1: // fresh account
				evs = append(evs, markEvent{acc: true, nibs: randNibs(r)})
			case 2: // hot storage slot
				evs = append(evs, markEvent{dom: hotD[r.Intn(len(hotD))], nibs: hotS[r.Intn(len(hotS))]})
			default: // fresh storage slot
				evs = append(evs, markEvent{dom: hotD[r.Intn(len(hotD))], nibs: randNibs(r)})
			}
		}
		blocks[n] = evs
	}

	live := newMarkBuilder(t)
	crash := newMarkBuilder(t)

	for n := uint64(0); n < S; n++ {
		applyLive(live, n, blocks[n])
		applyLive(crash, n, blocks[n])
	}

	// Crash + restart at S: all in-memory marks are lost, then backfilled.
	clearAllMarks(crash)
	crash.resumed = true
	from := uint64(S)
	for d := 0; d <= maxChgDepth; d++ {
		if crash.sched.e[d] <= 1 {
			continue
		}
		if es := uint64(S) - uint64(S)%crash.sched.e[d]; es < from {
			from = es
		}
	}
	for n := from; n < S; n++ {
		for _, e := range blocks[n] {
			if e.acc {
				crash.backfillAccMarks(e.nibs, n, S)
			} else {
				// Storage-only changes also dirty the account path in
				// emitBlock; the test stream has no paired account events for
				// storage keys, matching backfillDirty's unconditional call.
				crash.backfillStoMarks(e.dom, e.nibs, n, S)
			}
		}
	}

	compareMarks(t, live, crash, "immediately after backfill at S")

	// Continue both to END; verify at every subsequent epoch flush point the
	// pending sets agree (checked just BEFORE the boundary clear inside
	// applyLive by comparing after each block).
	for n := uint64(S); n < END; n++ {
		for _, e := range blocks[n] {
			if e.acc {
				live.recordChange(false, nil, e.nibs, n)
				crash.recordChange(false, nil, e.nibs, n)
			} else {
				live.recordChangeStorage(e.dom, e.nibs, n)
				crash.recordChangeStorage(e.dom, e.nibs, n)
			}
		}
		compareMarks(t, live, crash, "pre-flush at block "+itoa(n))
		for d := 0; d <= maxChgDepth; d++ {
			if (n+1)%live.sched.e[d] == 0 {
				clearEpochMarks(live, d)
				clearEpochMarks(crash, d)
			}
		}
	}
}

// TestBackfillNoopOnBoundary: resume exactly on every level's epoch boundary
// must not require (or produce) any marks.
func TestBackfillNoopOnBoundary(t *testing.T) {
	b := newMarkBuilder(t)
	// lcm(4,8,16,64) = 64 → block 128 is a boundary for every e>1 level.
	const S = 128
	from := uint64(S)
	for d := 0; d <= maxChgDepth; d++ {
		if b.sched.e[d] <= 1 {
			continue
		}
		if es := uint64(S) - uint64(S)%b.sched.e[d]; es < from {
			from = es
		}
	}
	if from != S {
		t.Fatalf("boundary resume computed backfill window [%d,%d), want empty", from, S)
	}
}

func compareMarks(t *testing.T, a, b *builder, where string) {
	t.Helper()
	for d := 0; d <= maxChgDepth; d++ {
		for idx := range a.accDirty[d] {
			if a.accDirty[d][idx] != b.accDirty[d][idx] {
				t.Fatalf("%s: accDirty[%d][%d] live=%04x crash=%04x", where, d, idx, a.accDirty[d][idx], b.accDirty[d][idx])
			}
		}
		// accTouched as a set (order may differ).
		at := map[uint32]bool{}
		for _, i := range a.accTouched[d] {
			at[i] = true
		}
		bt := map[uint32]bool{}
		for _, i := range b.accTouched[d] {
			bt[i] = true
		}
		if len(at) != len(bt) {
			t.Fatalf("%s: accTouched[%d] size live=%d crash=%d", where, d, len(at), len(bt))
		}
		for i := range at {
			if !bt[i] {
				t.Fatalf("%s: accTouched[%d] missing idx %d in crash", where, d, i)
			}
		}
		if len(a.stoDirty[d]) != len(b.stoDirty[d]) {
			for k := range b.stoDirty[d] {
				if _, ok := a.stoDirty[d][k]; !ok {
					t.Logf("stoDirty[%d] only-crash key %x", d, []byte(k))
				}
			}
			for k := range a.stoDirty[d] {
				if _, ok := b.stoDirty[d][k]; !ok {
					t.Logf("stoDirty[%d] only-live key %x", d, []byte(k))
				}
			}
			t.Fatalf("%s: stoDirty[%d] size live=%d crash=%d", where, d, len(a.stoDirty[d]), len(b.stoDirty[d]))
		}
		for k, av := range a.stoDirty[d] {
			bv, ok := b.stoDirty[d][k]
			if !ok {
				t.Fatalf("%s: stoDirty[%d] missing key %x in crash", where, d, []byte(k))
			}
			if *av != *bv {
				t.Fatalf("%s: stoDirty[%d][%x] live=%04x crash=%04x", where, d, []byte(k), *av, *bv)
			}
		}
	}
}

func itoa(n uint64) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	return s
}
