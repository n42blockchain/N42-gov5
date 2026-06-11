// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"sort"
	"testing"
)

// TestLeafSegCursor exercises spill → finalize → cursor against a reference
// sorted slice, covering cross-frame and cross-bucket boundaries and the
// floor-jump pattern asOfLeaves uses (Seek(key|n+1) then Prev / Last).
func TestLeafSegCursor(t *testing.T) {
	dir := t.TempDir()
	w, err := newLeafSpillWriter(dir)
	if err != nil {
		t.Fatal(err)
	}

	type row struct{ k, v []byte }
	var ref []row
	rng := uint64(12345)
	next := func() uint64 { rng = rng*6364136223846793005 + 1442695040888963407; return rng >> 11 }

	// ~50K rows across many buckets and several blocks per key — enough for
	// multiple frames per bucket at the 256 KiB frame target? Frame target is
	// large; force multiple frames with bulky values.
	for blk := uint64(0); blk < 8; blk++ {
		for i := 0; i < 6000; i++ {
			key := make([]byte, 32)
			binary.BigEndian.PutUint64(key[0:], next()%97) // few buckets, dense
			binary.BigEndian.PutUint64(key[8:], next()%4000)
			k := append(append([]byte{}, key...), 0, 0, 0, 0, 0, 0, 0, byte(blk))
			v := make([]byte, 40+int(next()%80))
			for j := range v {
				v[j] = byte(next())
			}
			if err := w.add(leafTableA, k, v); err != nil {
				t.Fatal(err)
			}
			ref = append(ref, row{k: k, v: v})
		}
	}
	if err := w.close(); err != nil {
		t.Fatal(err)
	}
	if err := finalizeLeafSegments(dir); err != nil {
		t.Fatal(err)
	}
	sort.SliceStable(ref, func(i, j int) bool { return bytes.Compare(ref[i].k, ref[j].k) < 0 })

	set, ok, err := openLeafSegSet(dir, leafTableA, newFrameLRU())
	if err != nil || !ok {
		t.Fatalf("open: ok=%v err=%v", ok, err)
	}

	// Full forward scan == reference.
	c := set.Cursor()
	k, v, err := c.Seek([]byte{0})
	for i := range ref {
		if err != nil {
			t.Fatal(err)
		}
		if k == nil {
			t.Fatalf("scan ended early at %d/%d", i, len(ref))
		}
		if !bytes.Equal(k, ref[i].k) || !bytes.Equal(v, ref[i].v) {
			t.Fatalf("row %d mismatch", i)
		}
		k, v, err = c.Next()
	}
	if k != nil {
		t.Fatal("scan has extra rows")
	}

	// Random Seek = first ref row >= probe; then Prev returns the row before.
	for trial := 0; trial < 2000; trial++ {
		probe := make([]byte, 40)
		for j := range probe {
			probe[j] = byte(next())
		}
		probe[0] = byte(next() % 120) // stay near populated buckets
		idx := sort.Search(len(ref), func(i int) bool { return bytes.Compare(ref[i].k, probe) >= 0 })
		k, v, err := c.Seek(probe)
		if err != nil {
			t.Fatal(err)
		}
		if idx == len(ref) {
			if k != nil {
				t.Fatalf("trial %d: Seek past end returned %x", trial, k[:8])
			}
			// asOfLeaves falls to Last() here.
			lk, lv, err := c.Last()
			if err != nil || !bytes.Equal(lk, ref[len(ref)-1].k) || !bytes.Equal(lv, ref[len(ref)-1].v) {
				t.Fatalf("trial %d: Last mismatch", trial)
			}
			continue
		}
		if !bytes.Equal(k, ref[idx].k) || !bytes.Equal(v, ref[idx].v) {
			t.Fatalf("trial %d: Seek mismatch", trial)
		}
		pk, pv, err := c.Prev()
		if err != nil {
			t.Fatal(err)
		}
		if idx == 0 {
			if pk != nil {
				t.Fatalf("trial %d: Prev before first returned %x", trial, pk[:8])
			}
		} else if !bytes.Equal(pk, ref[idx-1].k) || !bytes.Equal(pv, ref[idx-1].v) {
			t.Fatalf("trial %d: Prev mismatch", trial)
		}
	}

	// Multiple frames actually exercised?
	frames := 0
	for b := 0; b < 256; b++ {
		if set.buckets[b] != nil {
			frames += len(set.buckets[b].frames)
		}
	}
	if frames < 8 {
		t.Fatalf("want several frames, got %d (raise row count)", frames)
	}
	set.Close() // release file handles so TempDir cleanup works on Windows
	_ = os.RemoveAll(dir)
	_ = fmt.Sprintf
}
