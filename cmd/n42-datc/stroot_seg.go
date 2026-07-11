// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// stroot-export — migrates the dense storage-root history (DatcStoRoot,
// addrHash32|block8 → root32, empty value = "storage emptied" tombstone) from
// MDBX to static leafseg-format segment files (sr.XX.seg, bucketed on
// addrHash[0]). The table is append-only history with floor(≤N) reads — the
// same shape as the leaf history that already lives in segments — so the
// reader reuses segLeafCursor unchanged and storageRootAt routes through the
// segment set when present (MDBX table remains the fallback until dropped).
//
// The export streams the already-sorted MDBX rows straight into segFrameWriter
// (no spill stage). Each bucket writes to sr.XX.seg.tmp and renames on finish,
// so a hard kill leaves no partial .seg the reader would pick up.
//
//	n42-datc stroot-export --out D:/n42-datc-... [--frame-kb 32] [--spot 2000]
package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"time"

	"github.com/c2h5oh/datasize"
	"github.com/klauspost/compress/zstd"
	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
)

// segDirOf resolves the segment subdir the readers use (N42_DATC_LEAFSEG_DIR
// override, default leafSegDir) — the export must land where they look.
func segDirOf(outDir string) string {
	sub := leafSegDir
	if v := os.Getenv("N42_DATC_LEAFSEG_DIR"); v != "" {
		sub = v
	}
	return filepath.Join(outDir, sub)
}

func runStoRootExport(args []string) {
	fs := flag.NewFlagSet("stroot-export", flag.ExitOnError)
	out := fs.String("out", "", "DATC dir (MDBX with DatcStoRoot + leafseg dir)")
	frameKB := fs.Int("frame-kb", 32, "uncompressed frame-size target (KiB); 32 = fine frames, the point-read sweet spot")
	mapGB := fs.Int("map.gb", 2048, "MDBX map GB")
	spot := fs.Int("spot", 2000, "post-export random floor(≤N) A/B checks against the MDBX table (0 = skip)")
	force := fs.Bool("force", false, "overwrite existing sr.*.seg files")
	_ = fs.Parse(args)
	if *out == "" {
		die("--out required")
	}

	segd := segDirOf(*out)
	if err := os.MkdirAll(segd, 0o755); err != nil {
		die("mkdir %s: %v", segd, err)
	}
	if existing, _ := filepath.Glob(filepath.Join(segd, segTabNames[segTabStoRoot]+".*.seg")); len(existing) > 0 && !*force {
		die("%d sr.*.seg files already exist in %s — pass --force to overwrite", len(existing), segd)
	}

	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	db, err := mdbxkv.NewMDBX(log.New()).Path(*out).Label(kv.ChainDB).
		MapSize(datasize.ByteSize(*mapGB) * datasize.GB).Accede().Readonly().Open(context.Background())
	if err != nil {
		die("open: %v", err)
	}
	defer db.Close()
	tx, err := db.BeginRo(context.Background())
	if err != nil {
		die("begin: %v", err)
	}
	defer tx.Rollback()

	t0 := time.Now()
	rows, err := exportStoRoot(tx, segd, *frameKB)
	if err != nil {
		die("export: %v", err)
	}
	fmt.Printf("[stroot-export] done: %d rows in %s → %s\n", rows, time.Since(t0).Truncate(time.Second), segd)

	if *spot > 0 {
		spotCheckStoRoot(tx, segd, *out, *spot, rows)
	}
}

// exportStoRoot streams the sorted DatcStoRoot rows into per-bucket sr.XX.seg
// files (leafseg record encoding: uvarint kl|key|uvarint vl|val). Each bucket
// writes to .tmp and renames on finish, so a hard kill leaves no partial .seg.
func exportStoRoot(tx kv.Tx, segd string, frameKB int) (uint64, error) {
	c, err := tx.Cursor(tDatcStoRoot)
	if err != nil {
		return 0, fmt.Errorf("cursor %s: %w", tDatcStoRoot, err)
	}
	defer c.Close()

	oldTarget := segFrameRawTarget
	segFrameRawTarget = frameKB << 10
	defer func() { segFrameRawTarget = oldTarget }()

	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault))
	if err != nil {
		return 0, err
	}
	defer enc.Close()

	var (
		w          *segFrameWriter
		wBucket    = -1
		wTmp, wDst string
		rows       uint64
		rec        []byte
		t0         = time.Now()
	)
	finishBucket := func() error {
		if w == nil {
			return nil
		}
		if err := w.finish(); err != nil {
			return fmt.Errorf("finish bucket %02x: %w", wBucket, err)
		}
		w = nil
		return os.Rename(wTmp, wDst)
	}
	k, v, err := c.First()
	for ; k != nil && err == nil; k, v, err = c.Next() {
		if len(k) != 40 {
			return rows, fmt.Errorf("unexpected key length %d (want 40) at row %d", len(k), rows)
		}
		bucket := segBucketOf(segTabStoRoot, k)
		if bucket != wBucket {
			if err := finishBucket(); err != nil {
				return rows, err
			}
			wDst = filepath.Join(segd, segFileName(segTabStoRoot, bucket)+".seg")
			wTmp = wDst + ".tmp"
			if w, err = newSegFrameWriter(wTmp, enc); err != nil {
				return rows, fmt.Errorf("create %s: %w", wTmp, err)
			}
			wBucket = bucket
		}
		rec = rec[:0]
		rec = binary.AppendUvarint(rec, uint64(len(k)))
		rec = append(rec, k...)
		rec = binary.AppendUvarint(rec, uint64(len(v)))
		rec = append(rec, v...)
		if err := w.add(rec, k); err != nil {
			return rows, fmt.Errorf("write row %d: %w", rows, err)
		}
		rows++
		if rows%10_000_000 == 0 {
			fmt.Printf("[stroot-export] %dM rows, bucket %02x, %.0fK rows/s\n",
				rows/1_000_000, wBucket, float64(rows)/time.Since(t0).Seconds()/1000)
		}
	}
	if err != nil {
		return rows, fmt.Errorf("scan: %w", err)
	}
	return rows, finishBucket()
}

// runStoRootMerge — WEEKLY incremental update: merges the existing sr.*.seg
// history with the CURRENT MDBX DatcStoRoot rows (the delta a continuation
// build wrote since the last export/merge) into fresh segments. Both inputs
// stream in key order (2-way merge; on a duplicate key40 the MDBX side wins),
// so memory stays flat. Buckets swap one at a time — each swapped bucket is
// complete for its prefix, so a kill mid-swap just leaves some buckets one
// week older; rerun to finish. After a verified merge the MDBX table can be
// dropped again (drop-table) to keep the DB lean.
//
//	n42-datc stroot-merge --out D:/n42-datc-... [--frame-kb 32] [--spot 2000]
func runStoRootMerge(args []string) {
	fs := flag.NewFlagSet("stroot-merge", flag.ExitOnError)
	out := fs.String("out", "", "DATC dir (MDBX with DatcStoRoot delta + leafseg dir with sr.*.seg)")
	frameKB := fs.Int("frame-kb", 32, "uncompressed frame-size target (KiB)")
	mapGB := fs.Int("map.gb", 2048, "MDBX map GB")
	spot := fs.Int("spot", 2000, "post-merge random floor A/B checks against (segments ∪ table) (0 = skip)")
	_ = fs.Parse(args)
	if *out == "" {
		die("--out required")
	}
	segd := segDirOf(*out)

	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	db, err := mdbxkv.NewMDBX(log.New()).Path(*out).Label(kv.ChainDB).
		MapSize(datasize.ByteSize(*mapGB) * datasize.GB).Accede().Readonly().Open(context.Background())
	if err != nil {
		die("open: %v", err)
	}
	defer db.Close()
	tx, err := db.BeginRo(context.Background())
	if err != nil {
		die("begin: %v", err)
	}
	defer tx.Rollback()

	t0 := time.Now()
	rows, err := mergeStoRoot(tx, *out, segd, *frameKB)
	if err != nil {
		die("merge: %v", err)
	}
	fmt.Printf("[stroot-merge] done: %d rows in %s → %s\n", rows, time.Since(t0).Truncate(time.Second), segd)
	if *spot > 0 {
		spotCheckStoRoot(tx, segd, *out, *spot, rows)
	}
}

// mergeStoRoot 2-way-merges the existing sr segments with the MDBX delta into
// fresh segments (written as .merge tmp files, then swapped bucket by bucket).
func mergeStoRoot(tx kv.Tx, outDir, segd string, frameKB int) (uint64, error) {
	oldSet, hasOld, err := openLeafSegSet(outDir, segTabStoRoot, newFrameLRU())
	if err != nil {
		return 0, fmt.Errorf("open sr segments: %w", err)
	}
	var oldCur leafCur
	if hasOld {
		defer oldSet.Close() // idempotent; the swap phase closes it earlier
		oldCur = oldSet.Cursor()
	}
	mdbxCur, err := tx.Cursor(tDatcStoRoot)
	if err != nil {
		return 0, fmt.Errorf("cursor %s (nothing to merge?): %w", tDatcStoRoot, err)
	}
	defer mdbxCur.Close()

	oldTarget := segFrameRawTarget
	segFrameRawTarget = frameKB << 10
	defer func() { segFrameRawTarget = oldTarget }()
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault))
	if err != nil {
		return 0, err
	}
	defer enc.Close()

	var ok, ov []byte
	if oldCur != nil {
		if ok, ov, err = oldCur.Seek(nil); err != nil {
			return 0, err
		}
	}
	mk, mv, err := mdbxCur.First()
	if err != nil {
		return 0, err
	}

	var (
		w          *segFrameWriter
		wBucket    = -1
		wTmp, wDst string
		rows       uint64
		rec        []byte
		swaps      [][2]string // {tmp, dst} — applied after the old set's handles close
	)
	finishBucket := func() error {
		if w == nil {
			return nil
		}
		if err := w.finish(); err != nil {
			return fmt.Errorf("finish bucket %02x: %w", wBucket, err)
		}
		w = nil
		// Swap deferred: the OLD segment file is still open (oldSet handle) and
		// Windows cannot delete/rename-over an open file. Collect and apply
		// after all buckets are written and the old set is closed.
		swaps = append(swaps, [2]string{wTmp, wDst})
		return nil
	}
	emit := func(k, v []byte) error {
		if len(k) != 40 {
			return fmt.Errorf("unexpected key length %d (want 40)", len(k))
		}
		bucket := segBucketOf(segTabStoRoot, k)
		if bucket != wBucket {
			if err := finishBucket(); err != nil {
				return err
			}
			wDst = filepath.Join(segd, segFileName(segTabStoRoot, bucket)+".seg")
			wTmp = wDst + ".merge"
			var e error
			if w, e = newSegFrameWriter(wTmp, enc); e != nil {
				return fmt.Errorf("create %s: %w", wTmp, e)
			}
			wBucket = bucket
		}
		rec = rec[:0]
		rec = binary.AppendUvarint(rec, uint64(len(k)))
		rec = append(rec, k...)
		rec = binary.AppendUvarint(rec, uint64(len(v)))
		rec = append(rec, v...)
		rows++
		return w.add(rec, k)
	}

	for ok != nil || mk != nil {
		var c int
		switch {
		case ok == nil:
			c = 1
		case mk == nil:
			c = -1
		default:
			c = bytes.Compare(ok, mk)
		}
		switch {
		case c < 0: // old only
			if err := emit(ok, ov); err != nil {
				return rows, err
			}
			if ok, ov, err = oldCur.Next(); err != nil {
				return rows, err
			}
		case c > 0: // delta only
			if err := emit(mk, mv); err != nil {
				return rows, err
			}
			if mk, mv, err = mdbxCur.Next(); err != nil {
				return rows, err
			}
		default: // duplicate key: MDBX (newer write) wins
			if err := emit(mk, mv); err != nil {
				return rows, err
			}
			if ok, ov, err = oldCur.Next(); err != nil {
				return rows, err
			}
			if mk, mv, err = mdbxCur.Next(); err != nil {
				return rows, err
			}
		}
	}
	if err := finishBucket(); err != nil {
		return rows, err
	}
	if hasOld {
		oldSet.Close() // release Windows file handles before rename-over
	}
	for _, sw := range swaps {
		if err := os.Remove(sw[1]); err != nil && !os.IsNotExist(err) {
			return rows, err
		}
		if err := os.Rename(sw[0], sw[1]); err != nil {
			return rows, err
		}
	}
	return rows, nil
}

// spotCheckStoRoot compares floor(≤N) answers between the MDBX table and the
// freshly written segments for `n` random (existing-addrHash, random-block)
// probes plus a few absent addrHashes. Any divergence is fatal.
func spotCheckStoRoot(tx kv.Tx, segd, outDir string, n int, totalRows uint64) {
	head := uint64(1)
	if mv, _ := tx.GetOne(tDatcMeta, []byte("head")); len(mv) >= 8 {
		head = binary.BigEndian.Uint64(mv)
	}
	set, ok, err := openLeafSegSet(outDir, segTabStoRoot, newFrameLRU())
	if err != nil || !ok {
		die("spot: open sr segments: %v (ok=%v)", err, ok)
	}
	defer set.Close()
	segCur := set.Cursor()

	mc, err := tx.Cursor(tDatcStoRoot)
	if err != nil {
		die("spot: mdbx cursor: %v", err)
	}
	defer mc.Close()

	rng := rand.New(rand.NewSource(42))
	probe := make([]byte, 32)
	var checked, misses int
	t0 := time.Now()
	for i := 0; i < n; i++ {
		rng.Read(probe)
		// Land on a real addrHash by seeking the random point (absent-addr
		// probes come from the raw random key itself every 8th round).
		ah := probe
		if i%8 != 0 {
			k, _, err := mc.Seek(probe)
			if err != nil || k == nil {
				continue
			}
			ah = append([]byte{}, k[:32]...)
		}
		at := rng.Uint64() % (head + 1)
		mr, mh, mhit := stoRootFloorScan(mc, ah, at)
		sr, sh, shit := stoRootFloorScan(segCur, ah, at)
		if mr != sr || mh != sh || mhit != shit {
			die("spot MISMATCH ah=%x at=%d: mdbx=(%x,%v,%v) seg=(%x,%v,%v)",
				ah, at, mr[:8], mh, mhit, sr[:8], sh, shit)
		}
		checked++
		if !mhit {
			misses++
		}
	}
	fmt.Printf("[stroot-export] spot-check OK: %d/%d probes identical (%d floor-misses) in %s  [%d rows total]\n",
		checked, n, misses, time.Since(t0).Truncate(time.Millisecond), totalRows)
}

// stoRootSegHint prints once when storageRootAt routes through segments.
var stoRootSegHint bool

func openStoRootCursor(q *querier) (leafCur, bool) {
	if q.segSR != nil {
		if !stoRootSegHint {
			stoRootSegHint = true
			fmt.Fprintf(os.Stderr, "[stroot] storage-root history served from sr.*.seg segments\n")
		}
		return q.segSR.Cursor(), true
	}
	if c, err := q.tx.Cursor(tDatcStoRoot); err == nil {
		return c, true
	}
	return nil, false
}
