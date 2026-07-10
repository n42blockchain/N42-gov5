// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Differential test for the DatcStoRoot → sr.*.seg migration: build a small
// MDBX table with multi-bucket, multi-block, tombstoned rows; export it with
// tiny frames (forcing frame/bucket boundaries); then compare stoRootFloorScan
// answers between the MDBX cursor and the segment cursor for every
// (addrHash, block) probe including absent addresses.

package main

import (
	"context"
	"encoding/binary"
	"math/rand"
	"testing"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
)

func openStoRootTestDB(t *testing.T) kv.RwDB {
	t.Helper()
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	db, err := mdbxkv.NewMDBX(log.New()).Path(t.TempDir()).Label(kv.ChainDB).
		MapSize(datasize.ByteSize(1) * datasize.GB).
		WithTableCfg(func(_ kv.TableCfg) kv.TableCfg {
			d := kv.TableCfg{}
			for n, it := range kv.ChaindataTablesCfg {
				d[n] = it
			}
			d[tDatcStoRoot] = kv.TableCfgItem{}
			d[tDatcMeta] = kv.TableCfgItem{}
			return d
		}).Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	return db
}

func TestStoRootSegFloorEquivalence(t *testing.T) {
	db := openStoRootTestDB(t)
	ctx := context.Background()

	// Rows: 60 addrHashes spread across byte-0 buckets 0x00,0x01,0x7f,0xff,
	// each with versions at several blocks; ~1/4 of versions are tombstones
	// (empty value = storage emptied at that block).
	rng := rand.New(rand.NewSource(9))
	type addrRows struct {
		ah     []byte
		blocks []uint64
		vals   map[uint64][]byte // block → 32B root or nil (tombstone)
	}
	var addrs []addrRows
	buckets := []byte{0x00, 0x01, 0x7f, 0xff}
	for i := 0; i < 60; i++ {
		ah := make([]byte, 32)
		rng.Read(ah)
		ah[0] = buckets[i%len(buckets)]
		a := addrRows{ah: ah, vals: map[uint64][]byte{}}
		nb := 1 + rng.Intn(6)
		blk := uint64(rng.Intn(50))
		for j := 0; j < nb; j++ {
			blk += 1 + uint64(rng.Intn(300))
			var v []byte
			if rng.Intn(4) != 0 {
				v = make([]byte, 32)
				rng.Read(v)
			}
			a.blocks = append(a.blocks, blk)
			a.vals[blk] = v
		}
		addrs = append(addrs, a)
	}

	rw, err := db.BeginRw(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var head uint64
	for _, a := range addrs {
		for _, blk := range a.blocks {
			k := make([]byte, 40)
			copy(k, a.ah)
			binary.BigEndian.PutUint64(k[32:], blk)
			if err := rw.Put(tDatcStoRoot, k, a.vals[blk]); err != nil {
				t.Fatal(err)
			}
			if blk > head {
				head = blk
			}
		}
	}
	var h8 [8]byte
	binary.BigEndian.PutUint64(h8[:], head)
	if err := rw.Put(tDatcMeta, []byte("head"), h8[:]); err != nil {
		t.Fatal(err)
	}
	if err := rw.Commit(); err != nil {
		t.Fatal(err)
	}

	// Export with 1 KiB frames — forces multi-frame buckets.
	tx, err := db.BeginRo(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	segd := t.TempDir()
	rows, err := exportStoRoot(tx, segd, 1)
	if err != nil {
		t.Fatal(err)
	}
	var want uint64
	for _, a := range addrs {
		want += uint64(len(a.blocks))
	}
	if rows != want {
		t.Fatalf("exported %d rows, want %d", rows, want)
	}

	// Open the segment set from the export dir. openLeafSegSet joins
	// outDir+subdir, so point the override at the flat temp dir.
	t.Setenv("N42_DATC_LEAFSEG_DIR", ".")
	set, ok, err := openLeafSegSet(segd, segTabStoRoot, newFrameLRU())
	if err != nil || !ok {
		t.Fatalf("open sr segments: %v ok=%v", err, ok)
	}
	defer set.Close()

	mc, err := tx.Cursor(tDatcStoRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer mc.Close()
	sc := set.Cursor()

	// Exhaustive probes: every addr × blocks {0, first-1, first, mid, exact,
	// exact+1, head, head+100}; plus 200 absent random addrHashes.
	probeBlocks := func(a addrRows) []uint64 {
		ps := []uint64{0, head, head + 100}
		for _, b := range a.blocks {
			if b > 0 {
				ps = append(ps, b-1)
			}
			ps = append(ps, b, b+1)
		}
		return ps
	}
	checks := 0
	for _, a := range addrs {
		for _, at := range probeBlocks(a) {
			mr, mh, mhit := stoRootFloorScan(mc, a.ah, at)
			sr, sh, shit := stoRootFloorScan(sc, a.ah, at)
			if mr != sr || mh != sh || mhit != shit {
				t.Fatalf("ah=%x at=%d: mdbx=(%x,%v,%v) seg=(%x,%v,%v)",
					a.ah[:4], at, mr[:4], mh, mhit, sr[:4], sh, shit)
			}
			checks++
		}
	}
	for i := 0; i < 200; i++ {
		ah := make([]byte, 32)
		rng.Read(ah)
		at := rng.Uint64() % (head + 10)
		mr, mh, mhit := stoRootFloorScan(mc, ah, at)
		sr, sh, shit := stoRootFloorScan(sc, ah, at)
		if mr != sr || mh != sh || mhit != shit {
			t.Fatalf("absent ah=%x at=%d: mdbx=(%v,%v) seg=(%v,%v)", ah[:4], at, mh, mhit, sh, shit)
		}
		checks++
	}
	t.Logf("floor equivalence: %d probes identical (%d rows, 1KiB frames)", checks, rows)
}

// TestStoRootSegQuerierRouting: a querier with segSR set must answer
// storageRootAt from segments even when the MDBX table is ABSENT (post-drop).
func TestStoRootSegQuerierRouting(t *testing.T) {
	db := openStoRootTestDB(t)
	ctx := context.Background()

	ah := make([]byte, 32)
	ah[0] = 0x42
	root := make([]byte, 32)
	root[0] = 0xaa
	rw, err := db.BeginRw(ctx)
	if err != nil {
		t.Fatal(err)
	}
	k := make([]byte, 40)
	copy(k, ah)
	binary.BigEndian.PutUint64(k[32:], 100)
	if err := rw.Put(tDatcStoRoot, k, root); err != nil {
		t.Fatal(err)
	}
	binary.BigEndian.PutUint64(k[32:], 200)
	if err := rw.Put(tDatcStoRoot, k, nil); err != nil { // tombstone at 200
		t.Fatal(err)
	}
	if err := rw.Commit(); err != nil {
		t.Fatal(err)
	}

	tx, err := db.BeginRo(ctx)
	if err != nil {
		t.Fatal(err)
	}
	segd := t.TempDir()
	if _, err := exportStoRoot(tx, segd, 1); err != nil {
		t.Fatal(err)
	}
	tx.Rollback()

	t.Setenv("N42_DATC_LEAFSEG_DIR", ".")
	set, ok, err := openLeafSegSet(segd, segTabStoRoot, newFrameLRU())
	if err != nil || !ok {
		t.Fatalf("open sr segments: %v ok=%v", err, ok)
	}
	defer set.Close()

	// Fresh DB WITHOUT the table contents (simulates the post-drop state):
	// querier tx only serves tDatcMeta; segSR carries the history.
	db2 := openStoRootTestDB(t)
	tx2, err := db2.BeginRo(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx2.Rollback()

	q := &querier{tx: tx2, segSR: set}
	if root2, has, found := q.storageRootAt(ah, 150); !found || !has || root2[0] != 0xaa {
		t.Fatalf("at 150: want (0xaa..,true,true), got (%x,%v,%v)", root2[:4], has, found)
	}
	if _, has, found := q.storageRootAt(ah, 250); !found || has {
		t.Fatalf("at 250 (tombstone floor): want (has=false,found=true), got (has=%v,found=%v)", has, found)
	}
	if _, has, found := q.storageRootAt(ah, 50); found || has {
		t.Fatalf("at 50 (before first row): want miss, got (has=%v,found=%v)", has, found)
	}
}
