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

// TestStoRootMergeWeekly simulates the weekly cadence: full export → table
// dropped → continuation writes a delta (new blocks for old addrs + brand-new
// addrs) → stroot-merge folds segments ∪ delta into fresh segments. The merged
// set must answer every floor probe identically to a reference DB that holds
// ALL rows.
func TestStoRootMergeWeekly(t *testing.T) {
	ctx := context.Background()
	mkKey := func(ah []byte, blk uint64) []byte {
		k := make([]byte, 40)
		copy(k, ah)
		binary.BigEndian.PutUint64(k[32:], blk)
		return k
	}
	rng := rand.New(rand.NewSource(11))
	mkAddr := func(b0 byte) []byte {
		ah := make([]byte, 32)
		rng.Read(ah)
		ah[0] = b0
		return ah
	}
	mkRoot := func() []byte { v := make([]byte, 32); rng.Read(v); return v }

	// Week-1 rows.
	type row struct {
		k, v []byte
	}
	var week1, week2 []row
	var oldAddrs [][]byte
	for i := 0; i < 30; i++ {
		ah := mkAddr([]byte{0x00, 0x33, 0xcc}[i%3])
		oldAddrs = append(oldAddrs, ah)
		blk := uint64(10 + rng.Intn(500))
		week1 = append(week1, row{mkKey(ah, blk), mkRoot()})
		if i%3 == 0 {
			week1 = append(week1, row{mkKey(ah, blk+100), nil}) // tombstone
		}
	}
	// Week-2 delta: new blocks for half the old addrs + 10 brand-new addrs
	// (incl. a bucket 0x77 that week 1 never touched).
	for i, ah := range oldAddrs {
		if i%2 == 0 {
			week2 = append(week2, row{mkKey(ah, 1000+uint64(rng.Intn(500))), mkRoot()})
		}
	}
	for i := 0; i < 10; i++ {
		ah := mkAddr([]byte{0x77, 0x33}[i%2])
		week2 = append(week2, row{mkKey(ah, 1200+uint64(rng.Intn(300))), mkRoot()})
	}

	put := func(db kv.RwDB, rows []row) {
		rw, err := db.BeginRw(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rows {
			if err := rw.Put(tDatcStoRoot, r.k, r.v); err != nil {
				t.Fatal(err)
			}
		}
		if err := rw.Commit(); err != nil {
			t.Fatal(err)
		}
	}

	// Live DB: week 1 → export → clear (≈drop) → week 2 delta → merge.
	live := openStoRootTestDB(t)
	put(live, week1)
	segd := t.TempDir()
	tx1, err := live.BeginRo(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := exportStoRoot(tx1, segd, 1); err != nil {
		t.Fatal(err)
	}
	tx1.Rollback()
	rw, err := live.BeginRw(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := rw.ClearBucket(tDatcStoRoot); err != nil {
		t.Fatal(err)
	}
	if err := rw.Commit(); err != nil {
		t.Fatal(err)
	}
	put(live, week2)

	t.Setenv("N42_DATC_LEAFSEG_DIR", ".")
	tx2, err := live.BeginRo(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx2.Rollback()
	merged, err := mergeStoRoot(tx2, segd, segd, 1)
	if err != nil {
		t.Fatal(err)
	}
	if want := uint64(len(week1) + len(week2)); merged != want {
		t.Fatalf("merged %d rows, want %d", merged, want)
	}

	// Reference DB with ALL rows.
	ref := openStoRootTestDB(t)
	put(ref, week1)
	put(ref, week2)
	refTx, err := ref.BeginRo(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer refTx.Rollback()
	rc, err := refTx.Cursor(tDatcStoRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()

	set, ok, err := openLeafSegSet(segd, segTabStoRoot, newFrameLRU())
	if err != nil || !ok {
		t.Fatalf("open merged segments: %v ok=%v", err, ok)
	}
	defer set.Close()
	sc := set.Cursor()

	probes := 0
	allAddrs := append([][]byte{}, oldAddrs...)
	for _, r := range week2 {
		allAddrs = append(allAddrs, r.k[:32])
	}
	for _, ah := range allAddrs {
		for _, at := range []uint64{0, 50, 300, 700, 1100, 1600} {
			mr, mh, mhit := stoRootFloorScan(rc, ah, at)
			sr, sh, shit := stoRootFloorScan(sc, ah, at)
			if mr != sr || mh != sh || mhit != shit {
				t.Fatalf("ah=%x at=%d: ref=(%x,%v,%v) merged=(%x,%v,%v)",
					ah[:4], at, mr[:4], mh, mhit, sr[:4], sh, shit)
			}
			probes++
		}
	}
	t.Logf("weekly merge equivalence: %d probes identical (%d merged rows)", probes, merged)
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
