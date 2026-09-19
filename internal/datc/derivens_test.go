// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package datc

import (
	"encoding/binary"
	"math/rand"
	"testing"
)

// nodeStatesOf replays a node's record stream the way the reader resolves a
// floor record (FULL anchor, DIFFs forward, tombstone = empty) and returns the
// node state after each block that has a record.
func nodeStatesOf(t *testing.T, recs []kvPair) map[uint64]nodeState {
	t.Helper()
	out := map[uint64]nodeState{}
	var cur nodeState
	anchored := false
	for _, r := range recs {
		blk := uint64(binary.BigEndian.Uint32(r.k[len(r.k)-4:]))
		switch {
		case len(r.v) == 0:
			cur, anchored = nodeState{}, false
		case r.v[0] == nodeRecFull:
			st, ok := decodeFullRecord(r.v[1:])
			if !ok {
				t.Fatalf("block %d: undecodable FULL", blk)
			}
			cur, anchored = st, true
		default:
			if !anchored {
				t.Fatalf("block %d: DIFF without a FULL anchor before it", blk)
			}
			st, ok := applyDiff(cur, r.v)
			if !ok {
				t.Fatalf("block %d: DIFF does not apply", blk)
			}
			cur = st
		}
		out[blk] = cur
	}
	return out
}

// TestDeriveNodeExtend: a weekly extension (records from block X on, written
// by a second run that replays the whole history) continues the node exactly
// where a single full run would be — same state after every block >= X — and
// opens with a record that needs nothing before it.
func TestDeriveNodeExtend(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	dom := make([]byte, 32)
	rng.Read(dom)
	path := []byte{0x7}
	var rows []deriveRow
	slots := make([][32]byte, 300)
	for i := range slots {
		rng.Read(slots[i][:])
		slots[i][0] = 0x70 | byte(rng.Intn(16)) // below path nibble 7
	}
	// Rows arrive sorted by key, then block.
	for _, s := range slots {
		for blk := uint32(1); blk < 400; blk++ {
			if rng.Intn(25) != 0 {
				continue
			}
			r := deriveRow{block: blk, slot: s}
			if rng.Intn(4) != 0 {
				r.val = []byte{byte(1 + rng.Intn(200)), byte(blk)}
			}
			rows = append(rows, r)
		}
	}
	const X = 250
	run := func(from, to uint64) []kvPair {
		var recs []kvPair
		var st deriveStats
		_, err := deriveNode(append([]deriveRow{}, rows...), dom, path, nil, from, to, func(k, v []byte) error {
			recs = append(recs, kvPair{k, v})
			return nil
		}, &st)
		if err != nil {
			t.Fatal(err)
		}
		return recs
	}
	full := nodeStatesOf(t, run(0, 0))
	head, tail := run(0, X), run(X, 0)
	if len(tail) == 0 || len(head) == 0 {
		t.Fatal("the history does not straddle the split")
	}
	if v := tail[0].v; len(v) != 0 && v[0] != nodeRecFull {
		t.Fatal("an extension must open with a FULL record (or a tombstone)")
	}
	for _, r := range tail {
		if binary.BigEndian.Uint32(r.k[len(r.k)-4:]) < X {
			t.Fatal("extension wrote a record below its start")
		}
	}
	joined := nodeStatesOf(t, append(append([]kvPair{}, head...), tail...))
	if len(joined) != len(full) {
		t.Fatalf("split runs have records at %d blocks, the full run at %d", len(joined), len(full))
	}
	for blk, want := range full {
		if got, ok := joined[blk]; !ok || got != want {
			t.Fatalf("block %d: split runs disagree with the full run", blk)
		}
	}
}
