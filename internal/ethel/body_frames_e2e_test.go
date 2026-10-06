// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package ethel

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
)

// writeOneSegmentStore lays down a minimal bodyc store (one segment) with the
// given payload, so a real BodyCompactReader can be pointed at it.
func writeOneSegmentStore(t *testing.T, dir string, payload []byte) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	dat := make([]byte, 0, 4+len(payload))
	var sz [4]byte
	binary.LittleEndian.PutUint32(sz[:], uint32(len(payload)))
	dat = append(dat, sz[:]...)
	dat = append(dat, payload...)
	if err := os.WriteFile(filepath.Join(dir, "bodyc.0000.cdat"), dat, 0o644); err != nil {
		t.Fatal(err)
	}
	// One idx entry: fileNum 0, offset 0.
	var entry [8]byte
	if err := os.WriteFile(filepath.Join(dir, "bodyc.cidx"), entry[:], 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestFramedReaderEndToEnd writes a real framed segment, reads it back through
// BodyCompactReader, and compares both correctness and random-read latency
// against the same data in the legacy layout.
func TestFramedReaderEndToEnd(t *testing.T) {
	if _, err := os.Stat(filepath.Join(frameExpDir, "bodyc.cidx")); err != nil {
		t.Skip("bodyc freezer not present")
	}
	src := loadOneSegment(t, 25_000_000)

	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault))
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()

	legacyDir := filepath.Join(t.TempDir(), "legacy")
	framedDir := filepath.Join(t.TempDir(), "framed")
	writeOneSegmentStore(t, legacyDir, encodeBodySegment(src, 1, enc))
	writeOneSegmentStore(t, framedDir, encodeBodySegmentFramed(src, 1, enc, bodyFrameSize))

	// Probe blocks spread across the segment so consecutive reads land in
	// different frames — the pattern that used to re-decode 8192 blocks.
	probes := []uint64{0, 300, 1500, 4096, 6000, 8191, 100, 7000, 2048, 5000}

	for _, tc := range []struct {
		name string
		dir  string
	}{{"legacy", legacyDir}, {"framed", framedDir}} {
		r, err := OpenBodyCompact(tc.dir)
		if err != nil {
			t.Fatalf("%s: open: %v", tc.name, err)
		}

		// Correctness: every probe must match the source block.
		for _, p := range probes {
			got, err := r.ReadBody(p)
			if err != nil {
				t.Fatalf("%s: read %d: %v", tc.name, p, err)
			}
			if len(got.Txs) != len(src[p].Txs) {
				t.Fatalf("%s: block %d has %d txs, want %d", tc.name, p, len(got.Txs), len(src[p].Txs))
			}
		}

		// Latency: reopen so nothing is cached, then walk the probes.
		r.Close()
		r, err = OpenBodyCompact(tc.dir)
		if err != nil {
			t.Fatal(err)
		}
		t0 := time.Now()
		for _, p := range probes {
			if _, err := r.ReadBody(p); err != nil {
				t.Fatalf("%s: read %d: %v", tc.name, p, err)
			}
		}
		d := time.Since(t0)
		r.Close()
		t.Logf("%-7s %d scattered random reads: %s total, %s/read",
			tc.name, len(probes), d.Round(time.Millisecond), (d / time.Duration(len(probes))).Round(time.Millisecond))
	}
}
