// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// n42-datc cs-to-spill — extends the leaf-history + change-index spill from
// the forward changesets ALONE (no EVM, no trie, no 880 GB build MDBX): the
// acctcs/storcs freezer rows ARE the leaf rows ("CS = leaf"), only block-
// ordered instead of key-ordered — finalize-leaves does the reorder. This is
// how the 15.22M→25.44M range reaches the archive without resuming the OOM-
// prone build for anything but the node records (Pipeline B).
//
// Two per-block rules need PRE-state the changesets do not carry; both are
// served by (old leaf segments' floor at the boundary) ∪ (a side MDBX of
// in-range liveness), mirroring blockApply exactly:
//   - ghost-storage drop: storage rows of an account absent in pre-state and
//     not created this block are dropped (same-block create+destruct).
//   - SELFDESTRUCT wipes: per-slot tombstones for every pre-state slot. The
//     storcs generator (collectPreWipeSlots) already emits them; the belt here
//     re-derives the set and adds only what is missing (logged — data tells
//     us whether the new-range storcs is complete).
//
// Output goes to <out>/<--spill> (default leafspill2/) — the original spill
// and segments are never touched. Rows written per batch behind frame cuts:
// a hard kill loses only the in-flight batch (finalize skips the kill-tail),
// and resume re-emits the overlap deterministically (stable finalize sort).
//
// cs-spill-compare verifies converter output row-for-row against the already
// built archive: run cs-to-spill over a slice INSIDE the built range, then
// compare its leaf rows (as a key→value set) against the existing segments —
// the same equivalence discipline that caught the StorChg clobber bug.

package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/c2h5oh/datasize"
	"github.com/holiman/uint256"
	"github.com/klauspost/compress/zstd"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

// Side-DB tables: in-range liveness overlay over the old segments' boundary
// floor. SAcct: addrHash → 1/0 (live/dead). SSlot: addrHash|slotHash → 1/0.
// SMeta: "prog" → last fully committed block + belt counters.
const (
	tSideAcct = "SAcct"
	tSideSlot = "SSlot"
	tSideMeta = "SMeta"
)

func sideTableCfg(kv.TableCfg) kv.TableCfg {
	return kv.TableCfg{
		tSideAcct: {},
		tSideSlot: {},
		tSideMeta: {},
	}
}

func runCSToSpill(args []string) {
	fs := flag.NewFlagSet("cs-to-spill", flag.ExitOnError)
	out := fs.String("out", "", "DATC dir (leafseg/ + DatcMeta for schedules)")
	csDir := fs.String("changesets", `D:/N42-eth1177/chain/freezer`, "acctcs/storcs freezer dir")
	spillSub := fs.String("spill", "leafspill2", "output spill subdir under --out")
	sideDir := fs.String("side", "", "side MDBX dir (liveness overlay + progress; default <out>/csside)")
	start := fs.Uint64("start", 0, "first block (0 = resume from side progress; REQUIRED on first run)")
	end := fs.Uint64("end", 0, "end block (exclusive)")
	batch := fs.Uint64("batch", 8192, "blocks per spill frame-cut + side commit")
	mapGB := fs.Int("map.gb", 512, "MDBX map size GB")
	_ = fs.Parse(args)
	if *out == "" || *end == 0 {
		die("--out and --end required")
	}
	if *sideDir == "" {
		*sideDir = filepath.Join(*out, "csside")
	}

	logger := log.New()
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg

	// DATC DB read-only: schedules only (written by writeBuildMeta).
	ddb, err := mdbxkv.NewMDBX(logger).Path(*out).Label(kv.ChainDB).
		MapSize(datasize.ByteSize(*mapGB) * datasize.GB).Accede().Readonly().Open(context.Background())
	if err != nil {
		die("open DATC db: %v", err)
	}
	var sched, stoSched epochSchedule
	{
		tx, err := ddb.BeginRo(context.Background())
		if err != nil {
			die("begin: %v", err)
		}
		schedV, _ := tx.GetOne(tDatcMeta, []byte("sched"))
		if len(schedV) < (maxChgDepth+1)*8 {
			die("DATC meta 'sched' missing — converter must reuse the build's epoch schedule")
		}
		for d := 0; d <= maxChgDepth; d++ {
			sched.e[d] = binary.BigEndian.Uint64(schedV[d*8:])
		}
		stoSched = sched
		if ssV, _ := tx.GetOne(tDatcMeta, []byte("stoSched")); len(ssV) >= (maxChgDepth+1)*8 {
			for d := 0; d <= maxChgDepth; d++ {
				stoSched.e[d] = binary.BigEndian.Uint64(ssV[d*8:])
			}
		}
		tx.Rollback()
	}
	ddb.Close() // schedules read; the old LEAF SEGMENTS are the only other input

	// Old leaf segments: boundary-floor oracle for existence + wipe enumeration.
	cache := newFrameLRU()
	segA, okA, err := openLeafSegSet(*out, segTabLeafA, cache)
	if err != nil || !okA {
		die("open old leafseg (accounts): %v ok=%v", err, okA)
	}
	segS, okS, err := openLeafSegSet(*out, segTabLeafS, cache)
	if err != nil || !okS {
		die("open old leafseg (storage): %v ok=%v", err, okS)
	}
	q := &querier{segA: segA, segS: segS, sched: sched, stoSched: stoSched}

	// Side DB.
	sdb, err := mdbxkv.NewMDBX(logger).Path(*sideDir).Label(kv.ChainDB).
		MapSize(256 * datasize.GB).WithTableCfg(sideTableCfg).Open(context.Background())
	if err != nil {
		die("open side db: %v", err)
	}
	defer sdb.Close()

	// Resume.
	{
		tx, err := sdb.BeginRo(context.Background())
		if err == nil {
			if pv, _ := tx.GetOne(tSideMeta, []byte("prog")); len(pv) >= 8 {
				p := binary.BigEndian.Uint64(pv)
				if *start == 0 {
					*start = p
					fmt.Printf("[cs-to-spill] auto-resume from side progress: --start %d\n", p)
				} else if *start != p {
					fmt.Printf("[cs-to-spill] WARNING: explicit --start %d != side progress %d\n", *start, p)
				}
			}
			tx.Rollback()
		}
	}
	if *start == 0 {
		die("--start required on first run (no side progress found); the archive head is the usual value")
	}

	acctTbl := openCS(*csDir, "acctcs")
	storTbl := openCS(*csDir, "storcs")

	spill, err := newLeafSpillWriterDir(filepath.Join(*out, *spillSub))
	if err != nil {
		die("spill: %v", err)
	}

	// Lean builder: ONLY the emission machinery (emitBlock/recordChange/
	// flushChgAgg) — no trie, no node records, no gold check.
	b := &builder{
		sched: sched, stoSched: stoSched,
		addrHashCache: make(map[types.Address][32]byte, 1<<16),
		slotHashCache: make(map[types.Hash][32]byte, 1<<16),
		chgStoAgg:     make(map[string]*[]chgEvent, 1<<14),
		spill:         spill,
		outDir:        *out,
	}
	for d := 0; d <= maxChgDepth; d++ {
		size := 1
		for i := 0; i < d; i++ {
			size *= 16
		}
		b.accDirty[d] = make([]uint16, size)
		b.chgAccAgg[d] = make([]chgSlot, size)
		b.stoDirty[d] = make(map[string]*uint16, 1<<10)
	}
	b.chgAggCapBytes = 2 << 30

	conv := &csConverter{q: q, b: b, sdb: sdb, boundary: *start}
	fmt.Printf("cs-to-spill: blocks [%d, %d) → %s (batch %d)\n", *start, *end, *spillSub, *batch)

	t0 := time.Now()
	stx, err := sdb.BeginRw(context.Background())
	if err != nil {
		die("side begin: %v", err)
	}
	commit := func(n uint64) {
		// Drain aggregated chg rows for the batch, then cut spill frames BEFORE
		// the side commit — progress must never run ahead of durable rows.
		b.flushChgAgg()
		if err := drainChgToSpill(b); err != nil {
			die("drain chg: %v", err)
		}
		if err := spill.flushBatch(); err != nil {
			die("spill cut: %v", err)
		}
		var p [8]byte
		binary.BigEndian.PutUint64(p[:], n)
		if err := stx.Put(tSideMeta, []byte("prog"), p[:]); err != nil {
			die("prog: %v", err)
		}
		if err := stx.Commit(); err != nil {
			die("side commit: %v", err)
		}
		stx, err = sdb.BeginRw(context.Background())
		if err != nil {
			die("side begin: %v", err)
		}
		// Bound the per-batch dirty scratch (only node records consume these,
		// and the converter writes none).
		for d := 0; d <= maxChgDepth; d++ {
			if len(b.stoDirty[d]) > 0 {
				b.stoDirty[d] = make(map[string]*uint16, 1<<10)
			}
			for _, idx := range b.accTouched[d] {
				b.accDirty[d][idx] = 0
			}
			b.accTouched[d] = b.accTouched[d][:0]
		}
	}

	for n := *start; n < *end; n++ {
		if err := conv.block(stx, acctTbl, storTbl, n); err != nil {
			die("block %d: %v", n, err)
		}
		if (n+1)%*batch == 0 {
			commit(n + 1)
			if (n+1)%(*batch*16) == 0 {
				el := time.Since(t0)
				done := n + 1 - *start
				rate := float64(done) / el.Seconds()
				fmt.Printf("[cs-to-spill] %d / %d  (%.0f blk/s, ETA %s)  leafA=%d leafS=%d chg=%d beltTomb=%d ghostDrop=%d\n",
					n+1, *end, rate, time.Duration(float64(*end-n-1)/rate*float64(time.Second)).Round(time.Minute),
					b.leafAPuts, b.leafSPuts, b.chgPuts, conv.beltTombstones, conv.ghostDrops)
			}
		}
	}
	commit(*end)
	stx.Rollback()
	if err := spill.close(); err != nil {
		die("spill close: %v", err)
	}
	fmt.Printf("cs-to-spill DONE [%d, %d) in %s: leafA=%d leafS=%d chg=%d beltTombstones=%d ghostDrops=%d\n",
		*start, *end, time.Since(t0).Round(time.Second), b.leafAPuts, b.leafSPuts, b.chgPuts,
		conv.beltTombstones, conv.ghostDrops)
	fmt.Println("next: finalize-leaves --spill leafspill2 --seg-old leafseg --seg-out leafseg2 --frame-kb 32")
}

// drainChgToSpill moves the drained chg rows from the builder's sorted buffers
// into the spill (the flushAllBufs spill branch, without the MDBX arm).
func drainChgToSpill(b *builder) error {
	for _, e := range []struct {
		tab int
		buf *[]kvPair
	}{
		{segTabChgA, &b.chgAccBuf}, {segTabChgS, &b.chgStoBuf},
	} {
		for i := range *e.buf {
			if err := b.spill.add(e.tab, (*e.buf)[i].k, (*e.buf)[i].v); err != nil {
				return err
			}
		}
		*e.buf = (*e.buf)[:0]
	}
	return nil
}

// csConverter carries the pre-state oracles for the two state-dependent rules.
type csConverter struct {
	q        *querier // old leaf segments: floor at the boundary
	b        *builder
	sdb      kv.RwDB
	boundary uint64 // conversion start: old segments answer pre-state below this

	beltTombstones uint64 // wipe tombstones the belt ADDED (storcs lacked them)
	ghostDrops     uint64
}

// acctLive: account existence in pre-state of block n = side overlay, else the
// old segments' floor (all old rows are < boundary ≤ n-1, so floor at n-1).
func (c *csConverter) acctLive(stx kv.Tx, ah [32]byte, n uint64) (bool, error) {
	if v, err := stx.GetOne(tSideAcct, ah[:]); err != nil {
		return false, err
	} else if len(v) == 1 {
		return v[0] == 1, nil
	}
	val, ok, err := c.q.leafFloor(false, ah[:], n-1)
	if err != nil {
		return false, err
	}
	return ok && len(val) > 0, nil
}

// slotLiveSet returns the pre-state live slotHashes of addr: the old segments'
// distinct slots with a non-empty floor, overlaid by the side table (in-range
// writes/deletes/wipe marks win).
func (c *csConverter) slotLiveSet(stx kv.Tx, ah [32]byte, n uint64) (map[[32]byte]bool, error) {
	live := make(map[[32]byte]bool, 16)
	// Old segments: iterate distinct slots under the 40-byte domain prefix,
	// keeping each slot's floor ≤ n-1 (adaptive: linear then seek — the
	// asOfLeaves walk, specialized to value-emptiness only).
	domain := make([]byte, 40)
	copy(domain, ah[:])
	cur := c.q.segS.Cursor()
	defer cur.Close()
	const linearBudget = 4
	var curSlot [32]byte
	var haveSlot, floorLive bool
	linear := 0
	flush := func() {
		if haveSlot && floorLive {
			live[curSlot] = true
		}
	}
	k, v, err := cur.Seek(domain)
	for k != nil && err == nil {
		if !bytes.HasPrefix(k, domain) {
			break
		}
		if len(k) != 72+8 {
			k, v, err = cur.Next()
			continue
		}
		var sh [32]byte
		copy(sh[:], k[40:72])
		if !haveSlot || sh != curSlot {
			flush()
			curSlot, haveSlot, floorLive, linear = sh, true, false, 0
		}
		blk := binary.BigEndian.Uint64(k[72:])
		switch {
		case blk > n-1:
			k, v, err = seekNextKey(cur, k[:72])
			continue
		case linear < linearBudget:
			floorLive = len(v) > 0
			linear++
		default:
			seek := make([]byte, 0, 80)
			seek = append(seek, k[:72]...)
			seek = binary.BigEndian.AppendUint64(seek, n)
			fk, fv, ferr := cur.Seek(seek)
			if ferr != nil {
				return nil, ferr
			}
			if fk == nil {
				fk, fv, ferr = cur.Last()
			} else {
				fk, fv, ferr = cur.Prev()
			}
			if ferr != nil {
				return nil, ferr
			}
			if fk != nil && bytes.Equal(fk[:72], k[:72]) {
				floorLive = len(fv) > 0
			}
			k, v, err = seekNextKey(cur, k[:72])
			continue
		}
		k, v, err = cur.Next()
	}
	if err != nil {
		return nil, err
	}
	flush()
	// Side overlay: in-range liveness wins (covers writes, deletes, and the
	// dead marks written at earlier wipes).
	sc, err := stx.Cursor(tSideSlot)
	if err != nil {
		return nil, err
	}
	defer sc.Close()
	for k, v, err := sc.Seek(ah[:]); k != nil; k, v, err = sc.Next() {
		if err != nil {
			return nil, err
		}
		if !bytes.HasPrefix(k, ah[:]) {
			break
		}
		var sh [32]byte
		copy(sh[:], k[32:64])
		if len(v) == 1 && v[0] == 1 {
			live[sh] = true
		} else {
			delete(live, sh)
		}
	}
	return live, nil
}

// block converts one block: decode CS → dirty maps → ghost drop → wipe belt →
// emitBlock → side-table updates. Mirrors blockApply minus trie/node records.
func (c *csConverter) block(stx kv.RwTx, acctTbl, storTbl *freezer.FreezerTable, n uint64) error {
	dirtyA := make(map[types.Address]*account.StateAccount)
	dirtyS := make(map[types.Address]map[types.Hash]*uint256.Int)

	accBlob, err := acctTbl.Retrieve(n)
	if err != nil {
		return fmt.Errorf("acctcs: %w", err)
	}
	stoBlob, err := storTbl.Retrieve(n)
	if err != nil {
		return fmt.Errorf("storcs: %w", err)
	}
	if len(accBlob) > 0 {
		entries, err := ethel.DecodeAccountChanges(accBlob)
		if err != nil {
			return fmt.Errorf("decode acctcs: %w", err)
		}
		for _, e := range entries {
			if len(e.NewValue) == 0 {
				dirtyA[e.Address] = nil
				continue
			}
			var acct account.StateAccount
			if err := acct.DecodeForStorage(e.NewValue); err != nil {
				return fmt.Errorf("decode account %x: %w", e.Address, err)
			}
			dirtyA[e.Address] = &acct
		}
	}
	if len(stoBlob) > 0 {
		entries, err := ethel.DecodeStorageChanges(stoBlob)
		if err != nil {
			return fmt.Errorf("decode storcs: %w", err)
		}
		for _, e := range entries {
			var addr types.Address
			var slot types.Hash
			copy(addr[:], e.CompositeKey[:20])
			copy(slot[:], e.CompositeKey[20:])
			// Same rule as addSlot: drop intra-block WRITES of an account
			// deleted this block, keep its wipe entries (new = empty).
			if a, ok := dirtyA[addr]; ok && a == nil && len(e.NewValue) != 0 {
				continue
			}
			inner, ok := dirtyS[addr]
			if !ok {
				inner = make(map[types.Hash]*uint256.Int, 8)
				dirtyS[addr] = inner
			}
			if len(e.NewValue) == 0 {
				inner[slot] = nil
			} else {
				inner[slot] = new(uint256.Int).SetBytes(e.NewValue)
			}
		}
	}
	if len(dirtyA) == 0 && len(dirtyS) == 0 {
		return nil
	}

	// Ghost-storage drop (blockApply rule): storage-only address absent in
	// pre-state cannot exist at block end either — drop its rows.
	for addr := range dirtyS {
		if _, inA := dirtyA[addr]; inA {
			continue
		}
		ah := c.b.addrHash(addr)
		live, err := c.acctLive(stx, ah, n)
		if err != nil {
			return err
		}
		if !live {
			delete(dirtyS, addr)
			c.ghostDrops++
		}
	}
	if len(dirtyA) == 0 && len(dirtyS) == 0 {
		return nil
	}

	var blk8 [8]byte
	binary.BigEndian.PutUint64(blk8[:], n)

	// SELFDESTRUCT wipes: per-slot tombstones for every PRE-state slot of each
	// deleted account, emitted DIRECTLY (putLeaf + recordChange) exactly like
	// blockApply's wipe loop — never through dirtyS (a plain-slot map cannot
	// carry hash-only keys). storcs-provided wipe rows also flow via emitBlock;
	// the resulting duplicate tombstones match the build's own output and are
	// harmless to the floor semantics. beltTombstones counts enumerated slots
	// with NO matching storcs wipe row — data answers whether the new-range
	// storcs (collectPreWipeSlots) is complete.
	type wipeMark struct{ k [64]byte }
	var wipeMarks []wipeMark
	for addr, acct := range dirtyA {
		if acct != nil {
			continue
		}
		ah := c.b.addrHash(addr)
		liveSlots, err := c.slotLiveSet(stx, ah, n)
		if err != nil {
			return err
		}
		if len(liveSlots) == 0 {
			continue
		}
		// storcs wipe-row hashes for the belt counter.
		csWiped := make(map[[32]byte]bool, 8)
		for slot, v := range dirtyS[addr] {
			if v == nil {
				csWiped[c.b.slotHash(slot)] = true
			}
		}
		domain := make([]byte, 40)
		copy(domain, ah[:])
		for sh := range liveSlots {
			var comp [72]byte
			copy(comp[:40], domain)
			copy(comp[40:], sh[:])
			if err := c.b.putLeaf(true, append(comp[:], blk8[:]...), nil); err != nil {
				return err
			}
			c.b.leafSPuts++
			c.b.recordChange(true, comp[:40], nibblesOf(comp[40:]), n)
			if !csWiped[sh] {
				c.beltTombstones++
			}
			var wm wipeMark
			copy(wm.k[:32], ah[:])
			copy(wm.k[32:], sh[:])
			wipeMarks = append(wipeMarks, wm)
		}
	}

	if err := c.b.emitBlock(n, dirtyA, dirtyS, blk8); err != nil {
		return err
	}

	// Side-table updates: end-of-block liveness. Wipe dead-marks first so a
	// same-block re-create (storcs write row) wins with live=1.
	for _, wm := range wipeMarks {
		if err := stx.Put(tSideSlot, wm.k[:], []byte{0}); err != nil {
			return err
		}
	}
	for addr, acct := range dirtyA {
		ah := c.b.addrHash(addr)
		v := []byte{0}
		if acct != nil {
			v[0] = 1
		}
		if err := stx.Put(tSideAcct, ah[:], v); err != nil {
			return err
		}
	}
	for addr, slots := range dirtyS {
		ah := c.b.addrHash(addr)
		for slot, val := range slots {
			sh := c.b.slotHash(slot)
			k := make([]byte, 64)
			copy(k, ah[:])
			copy(k[32:], sh[:])
			v := []byte{0}
			if val != nil && !val.IsZero() {
				v[0] = 1
			}
			if err := stx.Put(tSideSlot, k, v); err != nil {
				return err
			}
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// cs-spill-compare — the equivalence gate: converter leaf rows (a scratch
// spill produced over a slice INSIDE the built range) vs the existing
// segments' rows for the same keys. Duplicates collapse to a key→value SET
// (the build itself emits duplicate wipe tombstones), values must byte-match.

func runCSSpillCompare(args []string) {
	fs := flag.NewFlagSet("cs-spill-compare", flag.ExitOnError)
	out := fs.String("out", "", "DATC dir (leafseg/ = truth)")
	spillSub := fs.String("spill", "", "scratch spill subdir produced by cs-to-spill over a built slice")
	_ = fs.Parse(args)
	if *out == "" || *spillSub == "" {
		die("--out and --spill required")
	}

	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	cache := newFrameLRU()
	segs := [2]*leafSegSet{}
	for tab := 0; tab < 2; tab++ {
		s, ok, err := openLeafSegSet(*out, tab, cache)
		if err != nil || !ok {
			die("open leafseg tab %d: %v ok=%v", tab, err, ok)
		}
		segs[tab] = s
	}

	zr, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1))
	if err != nil {
		die("zstd: %v", err)
	}
	defer zr.Close()

	var checked, missing, valDiff, dupes int64
	for tab := 0; tab < 2; tab++ {
		files, _ := filepath.Glob(filepath.Join(*out, *spillSub, segTabNames[tab]+".*.zspill"))
		seen := make(map[string][]byte, 1<<16)
		for _, src := range files {
			comp, err := os.ReadFile(src)
			if err != nil {
				die("read %s: %v", src, err)
			}
			raw, err := zr.DecodeAll(comp, nil)
			if err != nil {
				die("decode %s: %v (converter output should have no kill-tail)", src, err)
			}
			p := 0
			for p < len(raw) {
				kl, m := binary.Uvarint(raw[p:])
				ks := p + m
				k := raw[ks : ks+int(kl)]
				vl, m2 := binary.Uvarint(raw[ks+int(kl):])
				vs := ks + int(kl) + m2
				v := raw[vs : vs+int(vl)]
				p = vs + int(vl)
				if old, dup := seen[string(k)]; dup {
					dupes++
					if !bytes.Equal(old, v) {
						die("converter emitted CONFLICTING duplicates for key %x", k)
					}
					continue
				}
				seen[string(k)] = append([]byte{}, v...)
			}
		}
		cur := segs[tab].Cursor()
		for k, v := range seen {
			checked++
			gk, gv, err := cur.Seek([]byte(k))
			if err != nil {
				die("seek: %v", err)
			}
			if gk == nil || !bytes.Equal(gk, []byte(k)) {
				missing++
				if missing <= 10 {
					fmt.Printf("MISSING in segments: tab=%d key=%x\n", tab, k)
				}
				continue
			}
			if !bytes.Equal(gv, v) {
				valDiff++
				if valDiff <= 10 {
					fmt.Printf("VALUE DIFF: tab=%d key=%x conv=%x seg=%x\n", tab, k, v, gv)
				}
			}
		}
		cur.Close()
	}
	fmt.Printf("cs-spill-compare: %d rows checked, %d missing, %d value-diffs (%d dup rows collapsed)\n",
		checked, missing, valDiff, dupes)
	if missing == 0 && valDiff == 0 {
		fmt.Println("✅ converter output row-exact vs the built archive")
	} else {
		fmt.Println("❌ converter DIVERGES — do not proceed to the real range")
	}
}
