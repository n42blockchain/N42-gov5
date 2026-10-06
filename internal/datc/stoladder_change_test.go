// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package datc

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/n42blockchain/N42/lib/kv"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
)

// TestLadderChangeMidBuild is the weekly-extension case: the first half is
// built with one record shape for a contract, the resumed second half with
// another (deeper, and its level 0 recorded per block). The ladder sets the
// epoch divisor of that contract's records, so the reader must apply the
// ladder that was in force AT THE QUERIED HEIGHT — an approximate lookup near
// the change block would compute epochs on the wrong scale and could land on
// a record from a later height.
func TestLadderChangeMidBuild(t *testing.T) {
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

	hot := sc.big[0]
	hotHash := keccak(hot[:])
	o := e2eOpts{sched: schedE0is4, stoSched: [maxChgDepth + 1]uint64{8, 8, 8, 8, 64, 64},
		batch: 50, stoCache: 64, accDepth: 2, stoDepth: 2}
	end := uint64(len(sc.blocks))
	split := end / 2

	// First half: no map, everything at the default depth 2.
	b1 := newTestBuilder(t, db, out, sc, o, 0)
	if err := b1.run(0, split, o.batch); err != nil {
		t.Fatalf("first half: %v", err)
	}
	// Second half: the hot contract goes to depth 3 with level 0 per block.
	mapPath := filepath.Join(out, "depth.map")
	if err := os.WriteFile(mapPath, []byte(fmt.Sprintf("%x 3 0 3\n", hotHash[:])), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := loadStoDepthMap(mapPath)
	if err != nil {
		t.Fatal(err)
	}
	b2 := newTestBuilder(t, db, out, sc, o, split)
	b2.stoDepth = 2 // unnamed contracts keep the default of the first half
	b2.stoDepthMap = m
	b2.stoDepthSeen = make(map[string]stoLadder, len(m))
	b2.stoShifts = []uint8{0, 3}
	if err := b2.run(split, end, o.batch); err != nil {
		t.Fatalf("second half: %v", err)
	}

	q, closeQ := openTestQuerier(t, db, out, o)
	defer closeQ()

	// The ladder the reader applies must follow the height.
	if l := q.stoLadderAt(hotHash[:], split-1); l.depth != 2 || l.shift != 0 {
		t.Fatalf("before the change: ladder %+v, want depth 2 shift 0", l)
	}
	if l := q.stoLadderAt(hotHash[:], end-1); l.depth != 3 || l.level != 0 || l.shift != 3 {
		t.Fatalf("after the change: ladder %+v, want depth 3 level 0 shift 3", l)
	}

	// Every height: account root byte-exact, and the hot contract's storage
	// proofs verify against the reference storage root on both sides of the
	// change, especially right around it.
	for n := uint64(0); n < end; n++ {
		root, _, err := q.nodeHashAt(nil, nil, n)
		if err != nil {
			t.Fatal(err)
		}
		if root != sc.roots[n] {
			t.Fatalf("height %d: root %x != reference %x", n, root[:6], sc.roots[n][:6])
		}
		sm := sc.storageAt(hot, n)
		if len(sm) == 0 || (n%7 != 0 && (n < split-16 || n > split+16)) {
			continue
		}
		want := sc.storageRootAt(hot, n)
		for slot := range sm {
			sh := keccak(slot[:])
			nodes, err := q.proofPath(hotHash[:], nibblesOfBytes(sh[:]), n)
			if err != nil {
				t.Fatalf("height %d proofPath: %v", n, err)
			}
			got, err := walkProof(want, nodes, nibblesOfBytes(sh[:]))
			if err != nil {
				t.Fatalf("height %d: storage proof does not verify: %v", n, err)
			}
			if v := sm[slot]; !bytes.Equal(got, rStr(v)) {
				t.Fatalf("height %d: slot value %x != %x", n, got, rStr(v))
			}
			break // one live slot per height is enough
		}
	}
}
