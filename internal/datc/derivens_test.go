// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package datc

import (
	"context"
	"encoding/binary"
	"math/rand"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
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

// TestE2E_WeeklyExtension is the weekly update end to end: an archive built
// to block 300 and put on the exact ladder, then the build resumed to the
// scenario's end (new leaf rows merged into the existing segments), then
// derive-ns --from 300. Every height — before, across and after the seam —
// must give the reference storage root through the ladder, for listed and
// unlisted contracts, and the account trie must still reconstruct.
func TestE2E_WeeklyExtension(t *testing.T) {
	sc := getScenario(t)
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	out := t.TempDir()
	db, err := openDatcDB(log.New(), out, 4, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	writeFwd(t, db, sc)
	o := e2eOpts{sched: epochSchedule{e: [maxChgDepth + 1]uint64{8, 64, 16, 1, 4096, 4096}}, batch: 50, stoCache: 64, accDepth: 4, stoDepth: 2, leafSeg: true, accRoot: 1}
	end, seam := uint64(len(sc.blocks)), uint64(300)

	b := newTestBuilder(t, db, out, sc, o, 0)
	if err := b.run(0, seam, o.batch); err != nil {
		t.Fatalf("build to the seam: %v", err)
	}
	if err := b.spill.close(); err != nil {
		t.Fatal(err)
	}
	if err := finalizeLeafSegments(out); err != nil {
		t.Fatal(err)
	}
	stageBin = 8
	defer func() { stageBin = 1024 }()
	dom := func(a types.Address) []byte { h := keccak(a[:]); return h[:] }
	listed := map[types.Address]int{sc.big[0]: 2, sc.big[1]: 3, sc.small[2]: 1}
	var contracts []deriveContract
	for a, d := range listed {
		contracts = append(contracts, deriveContract{dom(a), d})
	}
	if err := deriveNS(out, out, contracts, 4, 16, deriveOpts{}); err != nil {
		t.Fatalf("derive-ns: %v", err)
	}

	// The week's blocks, built the way an exact-ladder archive is extended:
	// --sto-depth 0. The storage epoch records and their change rows are dead
	// weight once ns exists, so the resumed build stops writing them.
	week := o
	week.stoDepth, week.noStoRecs = 0, true
	nodesBefore := tableRows(t, db, tDatcStoNode)
	if err := newTestBuilder(t, db, out, sc, week, seam).run(seam, end, o.batch); err != nil {
		t.Fatalf("resumed build: %v", err)
	}
	if after := tableRows(t, db, tDatcStoNode); after != nodesBefore {
		t.Fatalf("--sto-depth 0 still wrote storage node records: %d -> %d", nodesBefore, after)
	}
	if err := deriveNS(out, out, contracts, 4, 16, deriveOpts{extendFrom: seam}); err != nil {
		t.Fatalf("derive-ns --from %d: %v", seam, err)
	}
	if err := deriveNS(out, out, contracts, 4, 16, deriveOpts{extendFrom: 100}); err == nil {
		t.Fatal("an extension that starts below a contract's last rung must be refused")
	}

	q, closeQ := openTestQuerier(t, db, out, o)
	defer closeQ()
	if q.exact == nil || !q.accExact {
		t.Fatalf("exact ladders not in force (storage %v, account %v)", q.exact != nil, q.accExact)
	}
	for n := uint64(0); n < end; n++ {
		root, exists, err := q.nodeHashAt(nil, nil, n)
		if err != nil {
			t.Fatalf("account root at %d: %v", n, err)
		}
		if !exists {
			root = emptyTrieRoot
		}
		if root != sc.roots[n] {
			t.Fatalf("account root at %d: %x != reference %x", n, root[:6], sc.roots[n][:6])
		}
		for _, a := range []types.Address{sc.big[0], sc.big[1], sc.small[2], sc.small[7]} {
			got, exists, err := q.exactRootAt(dom(a), n)
			if err != nil {
				t.Fatalf("%x at %d: %v", a[:4], n, err)
			}
			if !exists {
				got = emptyTrieRoot
			}
			if want := sc.storageRootAt(a, n); got != want {
				t.Fatalf("%x (depth %d) at %d (seam %d): root %x != reference %x", a[:4], listed[a], n, seam, got[:6], want[:6])
			}
		}
	}
}

func tableRows(t *testing.T, db kv.RwDB, table string) (n int) {
	t.Helper()
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		c, err := tx.Cursor(table)
		if err != nil {
			return err
		}
		defer c.Close()
		for k, _, err := c.First(); k != nil; k, _, err = c.Next() {
			if err != nil {
				return err
			}
			n++
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return n
}
