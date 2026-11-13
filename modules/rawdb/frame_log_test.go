// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package rawdb

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	log2 "github.com/n42blockchain/N42/lib/log/v3"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	"github.com/n42blockchain/N42/modules"
)

func newFrameLogTestStore(t *testing.T) kv.RwDB {
	t.Helper()
	db := mdbx.NewMDBX(log2.New()).InMem(t.TempDir()).
		WithTableCfg(func(c kv.TableCfg) kv.TableCfg { return FrameLogIndexTables(FrameStoreTables(c)) }).MustOpen()
	t.Cleanup(db.Close)
	return db
}

func frameLogTestFrame(i int) (types.Hash, []byte) {
	return types.Hash{byte(i), byte(i >> 8), 0xf1}, bytes.Repeat([]byte{byte(i), byte(i >> 8), 7}, 100+i)
}

func mustReadFrames(t *testing.T, db kv.RwDB, from, to int, want bool) {
	t.Helper()
	for i := from; i < to; i++ {
		h, raw := frameLogTestFrame(i)
		got, err := readFrameStore(db, []types.Hash{h})
		if err != nil {
			t.Fatal(err)
		}
		if want && !bytes.Equal(got[0], raw) {
			t.Fatalf("frame %d: read %d bytes, want %d", i, len(got[0]), len(raw))
		}
		if !want && got[0] != nil {
			t.Fatalf("frame %d: still readable", i)
		}
	}
}

func writeLogFrames(t *testing.T, from, to int) {
	t.Helper()
	var hs []types.Hash
	var raws [][]byte
	for i := from; i < to; i++ {
		h, raw := frameLogTestFrame(i)
		hs, raws = append(hs, h), append(raws, raw)
	}
	if err := WriteFramesToStore(context.Background(), hs, raws); err != nil {
		t.Fatal(err)
	}
}

// Frames written through the log read back, frames stored in the MDBX table
// before the log was opened stay readable, and a reopen keeps both.
func TestFrameLogWriteReadReopen(t *testing.T) {
	db := newFrameLogTestStore(t)
	defer SetFrameStore(db)()
	writeLogFrames(t, 0, 3) // into the MDBX table: no log yet
	dir := t.TempDir()
	closeLog, err := OpenFrameLog(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	writeLogFrames(t, 3, 10)
	// An entry without data is skipped, as in the table store.
	h, _ := frameLogTestFrame(99)
	if err := WriteFramesToStore(context.Background(), []types.Hash{h}, [][]byte{nil}); err != nil {
		t.Fatal(err)
	}
	mustReadFrames(t, db, 0, 10, true)
	mustReadFrames(t, db, 99, 100, false)
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		h3, _ := frameLogTestFrame(3)
		v, err := tx.GetOne(modules.Frames, h3[:])
		if err == nil && len(v) != 0 {
			t.Fatal("a log frame was also written to the Frames table")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	closeLog()
	closeLog, err = OpenFrameLog(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer closeLog()
	writeLogFrames(t, 10, 12)
	mustReadFrames(t, db, 0, 12, true)
}

// The index is derived: with its rows and position lost, and a torn record
// at the end of the log, a reopen indexes every complete record again, cuts
// the tail and appends after it.
func TestFrameLogRecoversIndexAndTornTail(t *testing.T) {
	db := newFrameLogTestStore(t)
	defer SetFrameStore(db)()
	dir := t.TempDir()
	closeLog, err := OpenFrameLog(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	writeLogFrames(t, 0, 6)
	closeLog()
	// Lose the index, as after a crash before its unsynced commit.
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		return tx.ClearBucket(modules.FrameLogIndex)
	}); err != nil {
		t.Fatal(err)
	}
	// A torn append: a full header promising more data than was written.
	seg := filepath.Join(dir, frameLogSegmentName(0))
	st, err := os.Stat(seg)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(seg, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	torn := make([]byte, frameLogHeaderSize+10)
	torn[0], torn[1], torn[2], torn[3] = 0x4e, 0x34, 0x32, 0x46
	torn[4] = 200
	if _, err := f.Write(torn); err != nil {
		t.Fatal(err)
	}
	f.Close()
	closeLog, err = OpenFrameLog(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer closeLog()
	if now, _ := os.Stat(seg); now.Size() != st.Size() {
		t.Fatalf("torn tail not cut: size %d, want %d", now.Size(), st.Size())
	}
	mustReadFrames(t, db, 0, 6, true)
	writeLogFrames(t, 6, 9)
	mustReadFrames(t, db, 0, 9, true)
}

// Segments roll at the size limit; with the bench-only retention the oldest
// are deleted and their frames read as not held.
func TestFrameLogRollAndKeepSegments(t *testing.T) {
	prev := frameLogSegmentBytes
	frameLogSegmentBytes = 2000
	defer func() { frameLogSegmentBytes = prev }()
	for _, keep := range []int{0, 2} {
		db := newFrameLogTestStore(t)
		restore := SetFrameStore(db)
		dir := t.TempDir()
		closeLog, err := OpenFrameLog(dir, keep)
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 12; i++ {
			writeLogFrames(t, i, i+1) // ~350-390 bytes each: 5 a segment
		}
		segs, err := frameLogSegments(dir)
		if err != nil {
			t.Fatal(err)
		}
		if keep == 0 {
			if len(segs) < 3 {
				t.Fatalf("segments = %v, want a roll", segs)
			}
			mustReadFrames(t, db, 0, 12, true)
		} else {
			if len(segs) != keep {
				t.Fatalf("keep=%d: segments = %v", keep, segs)
			}
			mustReadFrames(t, db, 0, 1, false)
			mustReadFrames(t, db, 11, 12, true)
		}
		closeLog()
		// Reopen over removed segments: appends continue and stay readable.
		closeLog, err = OpenFrameLog(dir, keep)
		if err != nil {
			t.Fatal(err)
		}
		writeLogFrames(t, 12, 14)
		mustReadFrames(t, db, 11, 14, true)
		closeLog()
		restore()
	}
}
