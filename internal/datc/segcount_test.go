// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package datc

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// buildStoLeafSegs writes a tiny storage leaf-history segment set directly
// (same spill/finalize path the real builder uses) so runSegCount has
// something real to scan.
func buildStoLeafSegs(t *testing.T, dir string, contracts, keysPerContract, blocks int) {
	t.Helper()
	w, err := newLeafSpillWriter(dir)
	if err != nil {
		t.Fatal(err)
	}
	for c := 0; c < contracts; c++ {
		var dom [32]byte
		dom[0] = byte(c + 1)
		for key := 0; key < keysPerContract; key++ {
			var slot [32]byte
			binary.BigEndian.PutUint64(slot[24:], uint64(key))
			for blk := 0; blk < blocks; blk++ {
				k := append(append([]byte{}, dom[:]...), slot[:]...)
				var bb [4]byte
				binary.BigEndian.PutUint32(bb[:], uint32(blk))
				k = append(k, bb[:]...)
				v := []byte{byte(blk), byte(key)}
				if err := w.add(leafTableS, k, v); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	if err := w.close(); err != nil {
		t.Fatal(err)
	}
	if err := finalizeLeafSegments(dir); err != nil {
		t.Fatal(err)
	}
}

// TestRunSegCountHappyPath exercises runSegCount's scan + map-write path on a
// synthetic storage leaf-history set (no flags, no die()).
func TestRunSegCountHappyPath(t *testing.T) {
	dir := t.TempDir()
	buildStoLeafSegs(t, dir, 5, 40, 3)

	mapPath := filepath.Join(dir, "sto.map")
	runSegCount([]string{
		"-out", dir,
		"-map", mapPath,
		"-fold-width", "8",
		"-fold-target", "4",
		"-blocks", "3",
	})

	b, err := os.ReadFile(mapPath)
	if err != nil {
		t.Fatalf("expected map file to be written: %v", err)
	}
	if len(b) == 0 {
		t.Fatalf("expected non-empty contract map")
	}
}

// TestRunSegCountMissingOutExits covers the --out-required die() path via the
// standard subprocess re-exec pattern.
func TestRunSegCountHelper(t *testing.T) {
	if os.Getenv("DATC_HELPER") != "segcount-noout" {
		t.Skip("run via TestRunSegCountMissingOutExits")
	}
	runSegCount(nil)
}

func TestRunSegCountMissingOutExits(t *testing.T) {
	code, out := runHelperSubprocess(t, "^TestRunSegCountHelper$", "DATC_HELPER=segcount-noout")
	if code != 1 {
		t.Fatalf("expected exit 1, got %d (output: %s)", code, out)
	}
	if !contains(out, "--out required") {
		t.Fatalf("expected --out required message, got %q", out)
	}
}
