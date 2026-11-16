// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package rawdb

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
)

// leanTestWrite is one frame log write: frames w*16 .. w*16+n, with an
// empty entry in the middle (skipped by both paths).
func leanTestWrite(w, n int) ([]types.Hash, [][]byte) {
	var hs []types.Hash
	var raws [][]byte
	for i := 0; i < n; i++ {
		h, raw := frameLogTestFrame(w*16 + i)
		if i == n/2 {
			raw = nil
		}
		hs, raws = append(hs, h), append(raws, raw)
	}
	return hs, raws
}

// frameLogSnapshot is everything a frame log leaves: its segment files up to
// the current one and every index row (the position included).
type frameLogSnapshot struct {
	files map[string][]byte
	index map[string][]byte
}

func snapshotFrameLog(t *testing.T, l *frameLog, db kv.RwDB) frameLogSnapshot {
	t.Helper()
	s := frameLogSnapshot{files: map[string][]byte{}, index: map[string][]byte{}}
	l.mu.Lock()
	cur := l.seg
	l.mu.Unlock()
	for seg := uint32(0); seg <= cur; seg++ {
		b, err := os.ReadFile(filepath.Join(l.dir, frameLogSegmentName(seg)))
		if err != nil {
			t.Fatal(err)
		}
		s.files[frameLogSegmentName(seg)] = b
	}
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		return tx.ForEach(modules.FrameLogIndex, nil, func(k, v []byte) error {
			s.index[string(k)] = append([]byte(nil), v...)
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	return s
}

func (s frameLogSnapshot) equal(t *testing.T, o frameLogSnapshot) {
	t.Helper()
	if len(s.files) != len(o.files) {
		t.Fatalf("segments: %d vs %d", len(s.files), len(o.files))
	}
	for name, b := range s.files {
		if !bytes.Equal(b, o.files[name]) {
			t.Fatalf("segment %s differs (%d vs %d bytes)", name, len(b), len(o.files[name]))
		}
	}
	if len(s.index) != len(o.index) {
		t.Fatalf("index rows: %d vs %d", len(s.index), len(o.index))
	}
	for k, v := range s.index {
		if !bytes.Equal(v, o.index[k]) {
			t.Fatalf("index row %x differs", k)
		}
	}
}

// runFrameLogWrites writes the sequence serially with the switch set as
// given and returns what the log left.
func runFrameLogWrites(t *testing.T, lean bool, seq [][2]int) frameLogSnapshot {
	t.Helper()
	defer SetFrameLogLeanLockForTest(lean)()
	db := newFrameLogTestStore(t)
	l, err := openFrameLog(t.TempDir(), db, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer l.close()
	for _, w := range seq {
		hs, raws := leanTestWrite(w[0], w[1])
		if err := l.write(context.Background(), hs, raws, &FrameWriteTimes{}); err != nil {
			t.Fatal(err)
		}
	}
	return snapshotFrameLog(t, l, db)
}

// The same writes leave byte-identical segment files and index rows with the
// switch off and on, across segment rolls.
func TestFrameLogLeanLockByteIdentical(t *testing.T) {
	prev := frameLogSegmentBytes
	frameLogSegmentBytes = 3000
	defer func() { frameLogSegmentBytes = prev }()
	seq := [][2]int{{0, 3}, {1, 5}, {2, 1}, {3, 7}, {4, 4}, {5, 9}, {6, 2}, {7, 6}}
	off := runFrameLogWrites(t, false, seq)
	on := runFrameLogWrites(t, true, seq)
	if len(off.files) < 3 {
		t.Fatalf("only %d segments: the sequence must roll", len(off.files))
	}
	off.equal(t, on)
}

// Concurrent writers with the switch on: whatever order the mutex gave
// them, the log holds exactly what the copying path writes for that order
// serially, and a reopen reads every frame back.
func TestFrameLogLeanLockConcurrentWriters(t *testing.T) {
	prev := frameLogSegmentBytes
	frameLogSegmentBytes = 4000
	defer func() { frameLogSegmentBytes = prev }()
	defer SetFrameLogLeanLockForTest(true)()
	db := newFrameLogTestStore(t)
	dir := t.TempDir()
	l, err := openFrameLog(dir, db, 0)
	if err != nil {
		t.Fatal(err)
	}
	const writers = 8
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			hs, raws := leanTestWrite(w, 3+w)
			if err := l.write(context.Background(), hs, raws, nil); err != nil {
				t.Error(err)
			}
		}(w)
	}
	wg.Wait()
	got := snapshotFrameLog(t, l, db)
	// The order the writes took: by the (segment, offset) of their first
	// frame.
	type at struct {
		w   int
		seg uint32
		off uint64
	}
	var order []at
	for w := 0; w < writers; w++ {
		h, _ := frameLogTestFrame(w * 16)
		v := got.index[string(h[:])]
		if len(v) != frameLogLocSize {
			t.Fatalf("writer %d: no index row", w)
		}
		seg, off := decodeLocForTest(v)
		order = append(order, at{w, seg, off})
	}
	sort.Slice(order, func(i, j int) bool {
		if order[i].seg != order[j].seg {
			return order[i].seg < order[j].seg
		}
		return order[i].off < order[j].off
	})
	var seq [][2]int
	for _, o := range order {
		seq = append(seq, [2]int{o.w, 3 + o.w})
	}
	got.equal(t, runFrameLogWrites(t, false, seq))

	// Reopen and read every frame back.
	l.close()
	l2, err := openFrameLog(dir, db, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer l2.close()
	for w := 0; w < writers; w++ {
		hs, raws := leanTestWrite(w, 3+w)
		back, err := l2.read(hs)
		if err != nil {
			t.Fatal(err)
		}
		for i := range hs {
			if !bytes.Equal(back[i], raws[i]) {
				t.Fatalf("writer %d frame %d: read %d bytes, want %d", w, i, len(back[i]), len(raws[i]))
			}
		}
	}
}

func decodeLocForTest(v []byte) (uint32, uint64) {
	var seg uint32
	var off uint64
	for i := 3; i >= 0; i-- {
		seg = seg<<8 | uint32(v[i])
	}
	for i := 11; i >= 4; i-- {
		off = off<<8 | uint64(v[i])
	}
	return seg, off
}

// pwritev with more slices than one call takes, and a resumed short write
// (a slice split across two calls), writes the same bytes as one buffer.
func TestFrameLogWriteVecChunks(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "v"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var iov [][]byte
	var want []byte
	for i := 0; i < 2500; i++ {
		b := []byte(fmt.Sprintf("<%d>", i))
		iov = append(iov, b)
		want = append(want, b...)
	}
	if err := frameLogWriteVec(f, iov, 7); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got[7:], want) {
		t.Fatal("vectored write differs from the concatenation")
	}
}

// An oversized frame fails before anything is written, as in the copying
// path.
func TestFrameLogLeanLockRejectsOversizedFrame(t *testing.T) {
	if _, err := buildFrameLogRecords([]types.Hash{{1}}, [][]byte{make([]byte, frameLogMaxFrame+1)}); err == nil {
		t.Fatal("oversized frame accepted")
	}
}
