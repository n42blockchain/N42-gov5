// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package main

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/klauspost/compress/zstd"
)

// TestReframeKeepsRows rewrites a node-record segment at a much smaller frame
// size, into a symlink view and then in place, and checks that the rows, the
// cursor contract (Seek / Prev across frame borders, the floor pattern the
// record reader uses) and the source archive all stay what they were.
func TestReframeKeepsRows(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "mdbx.dat"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	w, err := newLeafSpillWriter(src)
	if err != nil {
		t.Fatal(err)
	}
	type row struct{ k, v []byte }
	var ref []row
	rng := uint64(99)
	next := func() uint64 { rng = rng*6364136223846793005 + 1442695040888963407; return rng >> 11 }
	// Level-3 node records of two paths in one bucket: pathLen | nibbles | epoch.
	for _, path := range [][]byte{{3, 0xa, 0x1, 0x2}, {3, 0xa, 0x1, 0x7}} {
		for e := uint32(0); e < 4000; e++ {
			k := binary.BigEndian.AppendUint32(append([]byte{}, path...), e*3)
			v := make([]byte, 41)
			for j := range v {
				v[j] = byte(next())
			}
			if err := w.add(segTabNodeA, k, v); err != nil {
				t.Fatal(err)
			}
			ref = append(ref, row{k, v})
		}
	}
	if err := w.close(); err != nil {
		t.Fatal(err)
	}
	if err := finalizeLeafSegments(src); err != nil {
		t.Fatal(err)
	}
	names, _ := filepath.Glob(filepath.Join(src, leafSegDir, "na.*.seg"))
	if len(names) != 1 {
		t.Fatalf("want one na segment, got %v", names)
	}
	before, err := os.ReadFile(names[0])
	if err != nil {
		t.Fatal(err)
	}
	srcDigest, err := digestSegment(names[0])
	if err != nil {
		t.Fatal(err)
	}
	if srcDigest.rows != uint64(len(ref)) {
		t.Fatalf("source rows = %d, want %d", srcDigest.rows, len(ref))
	}

	check := func(dir string, minFrames int) {
		t.Helper()
		set, ok, err := openLeafSegSet(dir, segTabNodeA, newFrameLRU())
		if err != nil || !ok {
			t.Fatalf("open %s: ok=%v err=%v", dir, ok, err)
		}
		defer set.Close()
		frames := 0
		for _, b := range set.ids {
			n, err := set.frameCount(b)
			if err != nil {
				t.Fatal(err)
			}
			frames += n
		}
		if frames < minFrames {
			t.Fatalf("%s: %d frames, want >= %d", dir, frames, minFrames)
		}
		c := set.Cursor()
		k, v, err := c.Seek([]byte{0})
		for i := range ref {
			if err != nil || k == nil || !bytes.Equal(k, ref[i].k) || !bytes.Equal(v, ref[i].v) {
				t.Fatalf("%s: row %d mismatch (err=%v)", dir, i, err)
			}
			k, v, err = c.Next()
		}
		if k != nil {
			t.Fatalf("%s: extra rows", dir)
		}
		// Floor lookups: Seek(path | epoch+1) then Prev, as floorRecordBefore does.
		for i := 0; i < 2000; i++ {
			r := ref[int(next()%uint64(len(ref)))]
			seek := append([]byte{}, r.k...)
			binary.BigEndian.PutUint32(seek[4:], binary.BigEndian.Uint32(r.k[4:])+1)
			fc := set.Cursor()
			fk, _, err := fc.Seek(seek)
			if err != nil {
				t.Fatal(err)
			}
			if fk == nil {
				fk, _, err = fc.Last()
			} else {
				fk, _, err = fc.Prev()
			}
			if err != nil || !bytes.Equal(fk, r.k) {
				t.Fatalf("%s: floor of %x = %x (err=%v)", dir, seek, fk, err)
			}
		}
	}
	check(src, 1)

	enc, err := zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1))
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()

	// Into a view: the source segment must stay byte-identical.
	view := filepath.Join(t.TempDir(), "view")
	if err := linkArchiveView(src, view); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(view, leafSegDir, filepath.Base(names[0]))
	dg, err := reframeSegment(enc, names[0], dst, 1<<10)
	if err != nil {
		t.Fatal(err)
	}
	if dg != srcDigest {
		t.Fatalf("view digest %+v != source %+v", dg, srcDigest)
	}
	if fi, err := os.Lstat(dst); err != nil || fi.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("reframed segment is still a symlink (err=%v)", err)
	}
	after, err := os.ReadFile(names[0])
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("source segment changed (err=%v)", err)
	}
	if err := linkArchiveView(src, view); err != nil { // a rerun keeps the reframed segment
		t.Fatal(err)
	}
	check(view, 100)

	// In place.
	if _, err := reframeSegment(enc, names[0], names[0], 2<<10); err != nil {
		t.Fatal(err)
	}
	if back, err := digestSegment(names[0]); err != nil || back != srcDigest {
		t.Fatalf("in-place digest %+v != %+v (err=%v)", back, srcDigest, err)
	}
	check(src, 50)
}

// TestNodeSegmentsUseSmallFrames pins the finalize default: node records get
// nodeFrameRaw frames, leaf tables keep leafFrameRaw.
func TestNodeSegmentsUseSmallFrames(t *testing.T) {
	if got := segFrameRawFor(segTabNodeA); got != nodeFrameRaw {
		t.Fatalf("na frame target = %d", got)
	}
	if got := segFrameRawFor(segTabLeafS); got != leafFrameRaw {
		t.Fatalf("s frame target = %d", got)
	}
	for name, want := range map[string]int{"na.030f01.seg": segTabNodeA, "a.1f.zspill": segTabLeafA, "cs.0012.seg": segTabChgS} {
		if got, ok := segTableOfName(name); !ok || got != want {
			t.Fatalf("segTableOfName(%q) = %d,%v", name, got, ok)
		}
	}
	if _, ok := segTableOfName("zz.00.seg"); ok {
		t.Fatal("unknown table accepted")
	}
}

// TestSegFilesAreShared: two readers of one archive share one frame index per
// segment, segments open on first use, and the last Close releases the file.
func TestSegFilesAreShared(t *testing.T) {
	dir := t.TempDir()
	w, err := newLeafSpillWriter(dir)
	if err != nil {
		t.Fatal(err)
	}
	for b := 0; b < 4; b++ {
		for i := 0; i < 50; i++ {
			k := make([]byte, 36)
			k[0], k[1], k[35] = byte(b), byte(i), 1
			if err := w.add(leafTableA, k, []byte{byte(i), 1, 2, 3}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := w.close(); err != nil {
		t.Fatal(err)
	}
	if err := finalizeLeafSegments(dir); err != nil {
		t.Fatal(err)
	}
	registered := func() int {
		segFiles.mu.Lock()
		defer segFiles.mu.Unlock()
		return len(segFiles.m)
	}
	base := registered()
	s1, ok, err := openLeafSegSet(dir, leafTableA, newFrameLRU())
	if err != nil || !ok {
		t.Fatalf("open: ok=%v err=%v", ok, err)
	}
	s2, _, err := openLeafSegSet(dir, leafTableA, newFrameLRU())
	if err != nil {
		t.Fatal(err)
	}
	if got := registered(); got != base {
		t.Fatalf("opening a set loaded %d segments eagerly", got-base)
	}
	seek := make([]byte, 36)
	seek[0] = 2
	for _, s := range []*leafSegSet{s1, s2} {
		if k, _, err := s.Cursor().Seek(seek); err != nil || k == nil || k[0] != 2 {
			t.Fatalf("seek: k=%x err=%v", k, err)
		}
	}
	if got := registered(); got != base+1 {
		t.Fatalf("two readers of one segment hold %d index copies, want 1", got-base)
	}
	if s1.open[2].sf != s2.open[2].sf {
		t.Fatal("readers do not share the segment")
	}
	s1.Close()
	if got := registered(); got != base+1 {
		t.Fatal("segment released while a reader still holds it")
	}
	if k, _, err := s2.Cursor().Seek(seek); err != nil || k == nil {
		t.Fatalf("seek after the other reader closed: k=%x err=%v", k, err)
	}
	s2.Close()
	if got := registered(); got != base {
		t.Fatalf("%d segments still registered after the last Close", got-base)
	}
}
