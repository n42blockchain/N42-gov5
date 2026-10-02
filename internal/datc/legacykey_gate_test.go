// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// legacykey_gate_test.go covers the `len(k) != stoDomainLen+32+blkLen` skip
// gates in censusBucket (deriveplan.go), contractRungs/deriveStages
// (derivestages.go) and runSegCount's scan loop (segcount.go): a leftover
// 40-byte legacy addrHash+incarnation storage key (pre-cleanup wire shape,
// see CLAUDE.md "Storage keys carry NO incarnation") must be skipped rather
// than misread as a dom+slot+block key.
package datc

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// legacykeySeg writes a tiny storage leaf-history segment set for one domain
// with `good` valid dom(32)+slot(32)+block(4) rows, plus one legacy-length
// (40-byte, addrHash+incarnation) row that must be skipped by every gate
// under test. It returns the finalized leafseg dir and the domain used.
func legacykeySeg(t *testing.T, good int) (dir string, dom [32]byte) {
	t.Helper()
	dir = t.TempDir()
	w, err := newLeafSpillWriter(dir)
	if err != nil {
		t.Fatal(err)
	}
	dom[0] = 0x7
	for i := 0; i < good; i++ {
		var slot [32]byte
		binary.BigEndian.PutUint64(slot[24:], uint64(i))
		var bb [4]byte
		binary.BigEndian.PutUint32(bb[:], uint32(10*(i+1)))
		k := append(append(append([]byte{}, dom[:]...), slot[:]...), bb[:]...)
		if err := w.add(leafTableS, k, []byte{byte(i)}); err != nil {
			t.Fatal(err)
		}
	}
	// Legacy 40-byte key: addrHash(32) + incarnation(8), no block suffix.
	legacy := append(append([]byte{}, dom[:]...), make([]byte, 8)...)
	if len(legacy) != 40 {
		t.Fatalf("legacy key must be 40 bytes, got %d", len(legacy))
	}
	if err := w.add(leafTableS, legacy, []byte{0xFF}); err != nil {
		t.Fatal(err)
	}
	if err := w.close(); err != nil {
		t.Fatal(err)
	}
	if err := finalizeLeafSegments(dir); err != nil {
		t.Fatal(err)
	}
	return dir, dom
}

// TestCensusBucketSkipsLegacyKey covers deriveplan.go:84's length gate.
func TestCensusBucketSkipsLegacyKey(t *testing.T) {
	dir, dom := legacykeySeg(t, 3)
	names, err := filepath.Glob(filepath.Join(dir, leafSegDir, "s.*.seg"))
	if err != nil || len(names) == 0 {
		t.Fatalf("expected at least one storage leaf segment file, got %v (%v)", names, err)
	}
	var total []contractCensus
	for _, name := range names {
		cs, err := censusBucket(name, 1, 0)
		if err != nil {
			t.Fatalf("censusBucket(%s): %v", name, err)
		}
		total = append(total, cs...)
	}
	var found bool
	for _, c := range total {
		if c.dom != dom {
			continue
		}
		found = true
		if c.rows != 3 || c.keys != 3 {
			t.Fatalf("expected the legacy key to be skipped (rows=3 keys=3), got rows=%d keys=%d", c.rows, c.keys)
		}
	}
	if !found {
		t.Fatalf("expected a census entry for the test domain")
	}
}

// TestContractRungsSkipsLegacyKey covers derivestages.go:47's length gate.
func TestContractRungsSkipsLegacyKey(t *testing.T) {
	dir, dom := legacykeySeg(t, 5)
	set, ok, err := openLeafSegSet(dir, leafTableS, newFrameLRU())
	if err != nil || !ok {
		t.Fatalf("openLeafSegSet: ok=%v err=%v", ok, err)
	}
	defer set.Close()
	_, keys, err := contractRungs(set, dom[:], 1)
	if err != nil {
		t.Fatalf("contractRungs: %v", err)
	}
	if keys != 5 {
		t.Fatalf("expected the legacy key to be skipped (keys=5), got keys=%d", keys)
	}
}

// TestDeriveStagesSkipsLegacyKey covers derivestages.go:125's length gate: a
// depth-1 contract (the stage loop body never runs) still has to walk the
// gate without faulting on the legacy row.
func TestDeriveStagesSkipsLegacyKey(t *testing.T) {
	dir, dom := legacykeySeg(t, 4)
	set, ok, err := openLeafSegSet(dir, leafTableS, newFrameLRU())
	if err != nil || !ok {
		t.Fatalf("openLeafSegSet: ok=%v err=%v", ok, err)
	}
	defer set.Close()
	c := deriveContract{dom: dom[:], depth: 1}
	st := &deriveStats{}
	noopEmit := func(table int, k, v []byte) error { return nil }
	out, err := deriveStages(set, c, []uint64{1000}, nil, noopEmit, st)
	if err != nil {
		t.Fatalf("deriveStages: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("expected one depth slot for depth=1, got %d", len(out))
	}
}

// TestRunSegCountSkipsLegacyKey covers segcount.go:76/88's length gate: the
// legacy row must not be counted into rows/keys, nor crash the 4-byte
// big-endian block extraction that assumes a well-formed key.
func TestRunSegCountSkipsLegacyKey(t *testing.T) {
	dir, _ := legacykeySeg(t, 6)
	mapPath := filepath.Join(dir, "sto.map")
	out := captureStdout(t, func() {
		runSegCount([]string{
			"-out", dir,
			"-map", mapPath,
			"-fold-width", "2", // 6 distinct keys over width 2 forces depth > 0
			"-fold-target", "1",
			"-blocks", "60",
		})
	})
	if !contains(out, "rows=6 distinctKeys=6") {
		t.Fatalf("expected the legacy row to be excluded from rows/distinctKeys, got %q", out)
	}
	b, err := os.ReadFile(mapPath)
	if err != nil {
		t.Fatalf("expected map file to be written: %v", err)
	}
	if len(b) == 0 {
		t.Fatalf("expected non-empty contract map")
	}
}
