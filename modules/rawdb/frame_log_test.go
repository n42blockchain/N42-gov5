// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package rawdb

import (
	"bytes"
	"context"
	"encoding/binary"
	"hash/crc32"
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
		rolls0, pre0 := frameLogRolls.Load(), frameLogRollsPre.Load()
		for i := 0; i < 12; i++ {
			writeLogFrames(t, i, i+1) // ~350-390 bytes each: 5 a segment
		}
		cur := settleFrameLog(t)
		if rolls, pre := frameLogRolls.Load()-rolls0, frameLogRollsPre.Load()-pre0; rolls < 2 || pre == 0 {
			t.Fatalf("keep=%d: %d rolls, %d of them on a preallocated segment", keep, rolls, pre)
		}
		segs, err := frameLogSegments(dir)
		if err != nil {
			t.Fatal(err)
		}
		// The next segment, preallocated in the background, is not one yet.
		for len(segs) > 0 && segs[len(segs)-1] > cur {
			segs = segs[:len(segs)-1]
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
		if keep > 0 {
			// A crash before the background deletion: the dropped segment
			// is still on disk, below the index position. Its record of
			// frame 11 carries other data, which must never be indexed.
			h, raw := frameLogTestFrame(11)
			stale := append(frameLogTestRecord(h, bytes.Repeat([]byte{0xbd}, len(raw))), make([]byte, 64)...)
			if err := os.WriteFile(filepath.Join(dir, frameLogSegmentName(0)), stale, 0o644); err != nil {
				t.Fatal(err)
			}
		}
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

// settleFrameLog waits for the open frame log's background preallocation
// and deletions and returns its current segment.
func settleFrameLog(t *testing.T) uint32 {
	t.Helper()
	h := frameStore.Load()
	if h == nil || h.log == nil {
		t.Fatal("no frame log open")
	}
	l := h.log
	l.mu.Lock()
	if pre := l.pre; pre != nil {
		<-pre.done
	}
	seg := l.seg
	l.mu.Unlock()
	l.bg.Wait()
	return seg
}

// frameLogTestRecord is one encoded log record.
func frameLogTestRecord(h types.Hash, data []byte) []byte {
	rec := make([]byte, frameLogHeaderSize, frameLogHeaderSize+len(data))
	binary.BigEndian.PutUint32(rec, frameLogMagic)
	binary.LittleEndian.PutUint32(rec[4:], uint32(len(data)))
	copy(rec[8:], h[:])
	binary.LittleEndian.PutUint32(rec[8+types.HashLength:], crc32.Checksum(data, frameLogCRC))
	return append(rec, data...)
}

// A crash after the next segment was preallocated in the background leaves
// an empty (or, where preallocation writes zeros, zero-filled) segment after
// the last one, and the index may be behind with a torn record in the last
// written segment. A reopen indexes every complete record, ignores the torn
// one, appends to the new segment from offset 0, and every frame reads back
// (also after another reopen).
func TestFrameLogRecoversPreallocatedNextSegment(t *testing.T) {
	for _, zeroFilled := range []bool{false, true} {
		db := newFrameLogTestStore(t)
		restore := SetFrameStore(db)
		dir := t.TempDir()
		closeLog, err := OpenFrameLog(dir, 0)
		if err != nil {
			t.Fatal(err)
		}
		writeLogFrames(t, 0, 6)
		closeLog()
		if err := db.Update(context.Background(), func(tx kv.RwTx) error {
			return tx.ClearBucket(modules.FrameLogIndex)
		}); err != nil {
			t.Fatal(err)
		}
		f, err := os.OpenFile(filepath.Join(dir, frameLogSegmentName(0)), os.O_WRONLY|os.O_APPEND, 0)
		if err != nil {
			t.Fatal(err)
		}
		h, raw := frameLogTestFrame(6)
		if _, err := f.Write(frameLogTestRecord(h, raw)[:frameLogHeaderSize+10]); err != nil {
			t.Fatal(err)
		}
		f.Close()
		var next []byte
		if zeroFilled {
			next = make([]byte, 4096)
		}
		nextPath := filepath.Join(dir, frameLogSegmentName(1))
		if err := os.WriteFile(nextPath, next, 0o644); err != nil {
			t.Fatal(err)
		}
		closeLog, err = OpenFrameLog(dir, 0)
		if err != nil {
			t.Fatal(err)
		}
		if st, err := os.Stat(nextPath); err != nil || st.Size() != 0 {
			t.Fatalf("zeroFilled=%v: the preallocated segment was not cut to its records: %v %v", zeroFilled, st, err)
		}
		mustReadFrames(t, db, 0, 6, true)
		writeLogFrames(t, 6, 9)
		mustReadFrames(t, db, 0, 9, true)
		if st, err := os.Stat(nextPath); err != nil || st.Size() == 0 {
			t.Fatalf("zeroFilled=%v: appends did not go to the preallocated segment", zeroFilled)
		}
		closeLog()
		closeLog, err = OpenFrameLog(dir, 0)
		if err != nil {
			t.Fatal(err)
		}
		mustReadFrames(t, db, 0, 9, true)
		closeLog()
		restore()
	}
}

// The next segment is preallocated in the background once the current one
// is half full and taken by the roll; one still unused at close is removed,
// so a reopen appends to the current segment as before.
func TestFrameLogPreallocatesNextSegment(t *testing.T) {
	prev := frameLogSegmentBytes
	frameLogSegmentBytes = 2000
	defer func() { frameLogSegmentBytes = prev }()
	db := newFrameLogTestStore(t)
	defer SetFrameStore(db)()
	dir := t.TempDir()
	closeLog, err := OpenFrameLog(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	writeLogFrames(t, 0, 1)
	settleFrameLog(t)
	if _, err := os.Stat(filepath.Join(dir, frameLogSegmentName(1))); !os.IsNotExist(err) {
		t.Fatalf("next segment created before the current one is half full: %v", err)
	}
	writeLogFrames(t, 1, 3) // over half of 2000 bytes
	settleFrameLog(t)
	nextPath := filepath.Join(dir, frameLogSegmentName(1))
	if st, err := os.Stat(nextPath); err != nil || st.Size() != 0 {
		t.Fatalf("next segment not preallocated (empty) at half: %v %v", st, err)
	}
	closeLog()
	if _, err := os.Stat(nextPath); !os.IsNotExist(err) {
		t.Fatalf("unused preallocated segment kept at close: %v", err)
	}
	closeLog, err = OpenFrameLog(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer closeLog()
	pre0 := frameLogRollsPre.Load()
	for i := 3; i < 12; i++ {
		writeLogFrames(t, i, i+1)
	}
	if cur := settleFrameLog(t); cur < 1 {
		t.Fatalf("no roll: segment %d", cur)
	}
	if frameLogRollsPre.Load() == pre0 {
		t.Fatal("no roll took the preallocated segment")
	}
	// A write's split: it waited for no one, appended and indexed.
	var times FrameWriteTimes
	h, raw := frameLogTestFrame(12)
	if err := WriteFramesToStoreTimed(context.Background(), []types.Hash{h}, [][]byte{raw}, &times); err != nil {
		t.Fatal(err)
	}
	if times.IO <= 0 || times.Index <= 0 || times.LockWait < 0 {
		t.Fatalf("write split not recorded: %+v", times)
	}
	if us, rolls, _ := FrameLogRollStats(); rolls == 0 || us < 0 {
		t.Fatalf("roll stats: %d us, %d rolls", us, rolls)
	}
	mustReadFrames(t, db, 0, 12, true)
}
