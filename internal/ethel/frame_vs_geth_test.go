// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// frame_vs_geth_test.go — the comparison that decides whether framed bodyc is
// competitive with the raw geth ancient store on RANDOM reads, which is the
// access pattern that drove consumers to geth in the first place.

package ethel

import (
	"encoding/binary"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

const gethAncientDir = `D:\geth\geth\chaindata\ancient\chain`

// TestFramedVsGethRandomRead reads the same scattered block numbers from
// (a) the geth ancient store, (b) legacy whole-segment bodyc and (c) framed
// bodyc, and reports per-read latency for each.
func TestFramedVsGethRandomRead(t *testing.T) {
	if _, err := os.Stat(filepath.Join(frameExpDir, "bodyc.cidx")); err != nil {
		t.Skip("bodyc freezer not present")
	}
	if _, err := os.Stat(gethAncientDir); err != nil {
		t.Skip("geth ancient not present")
	}

	src := loadOneSegment(t, 25_000_000)
	segBase := uint64(25_000_000/HeaderSegmentSize) * HeaderSegmentSize

	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault))
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()

	legacyDir := filepath.Join(t.TempDir(), "legacy")
	framedDir := filepath.Join(t.TempDir(), "framed")
	writeOneSegmentStore(t, legacyDir, encodeBodySegment(src, 1, enc))
	writeOneSegmentStore(t, framedDir, encodeBodySegmentFramed(src, 1, enc, bodyFrameSize))

	// Scattered probes across the whole segment, fixed seed for repeatability.
	rng := rand.New(rand.NewSource(20260831))
	const probeCount = 200
	probes := make([]uint64, probeCount)
	for i := range probes {
		probes[i] = uint64(rng.Intn(HeaderSegmentSize))
	}

	// (a) geth ancient — each item is independently snappy-compressed.
	fz, err := freezer.NewReadOnly(gethAncientDir)
	if err != nil {
		t.Fatalf("open geth ancient: %v", err)
	}
	t0 := time.Now()
	var gethBytes int
	for _, p := range probes {
		b, err := fz.Ancient(freezer.TableBodies, segBase+p)
		if err != nil {
			t.Fatalf("geth read %d: %v", segBase+p, err)
		}
		gethBytes += len(b)
	}
	gethDur := time.Since(t0)
	fz.Close()

	// (b) legacy bodyc and (c) framed bodyc.
	run := func(dir string) time.Duration {
		r, err := OpenBodyCompact(dir)
		if err != nil {
			t.Fatalf("open %s: %v", dir, err)
		}
		defer r.Close()
		start := time.Now()
		for _, p := range probes {
			if _, err := r.ReadBody(p); err != nil {
				t.Fatalf("%s read %d: %v", dir, p, err)
			}
		}
		return time.Since(start)
	}
	legacyDur := run(legacyDir)
	framedDur := run(framedDir)

	per := func(d time.Duration) time.Duration { return d / probeCount }
	t.Logf("%d scattered random reads inside one segment:", probeCount)
	t.Logf("  geth ancient   %10s total  %10s/read   (%d body bytes returned)",
		gethDur.Round(time.Millisecond), per(gethDur).Round(time.Microsecond), gethBytes)
	t.Logf("  bodyc legacy   %10s total  %10s/read",
		legacyDur.Round(time.Millisecond), per(legacyDur).Round(time.Microsecond))
	t.Logf("  bodyc framed   %10s total  %10s/read",
		framedDur.Round(time.Millisecond), per(framedDur).Round(time.Microsecond))
	t.Logf("  framed vs legacy: %.1fx faster;  geth vs framed: %.1fx faster",
		float64(legacyDur)/float64(framedDur), float64(framedDur)/float64(gethDur))
}

// TestFramedVsGethCrossSegment is the access pattern that actually matters:
// reads scattered across MANY segments, where whole-segment caching never gets
// a second hit and every read pays a full decompress. This is what pushed
// consumers onto the geth ancient store.
func TestFramedVsGethCrossSegment(t *testing.T) {
	if _, err := os.Stat(filepath.Join(frameExpDir, "bodyc.cidx")); err != nil {
		t.Skip("bodyc freezer not present")
	}
	if _, err := os.Stat(gethAncientDir); err != nil {
		t.Skip("geth ancient not present")
	}

	// Build a multi-segment store so cross-segment reads are possible.
	const segCount = 4
	firstSeg := uint64(25_000_000 / HeaderSegmentSize)
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault))
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()

	src := make([][]*DecodedBlock, segCount)
	for i := 0; i < segCount; i++ {
		src[i] = loadOneSegment(t, (firstSeg+uint64(i))*HeaderSegmentSize)
	}

	writeMulti := func(dir string, framed bool) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		var dat []byte
		var idx []byte
		for i := 0; i < segCount; i++ {
			var payload []byte
			if framed {
				payload = encodeBodySegmentFramed(src[i], 1, enc, bodyFrameSize)
			} else {
				payload = encodeBodySegment(src[i], 1, enc)
			}
			off := uint32(len(dat))
			var sz [4]byte
			binary.LittleEndian.PutUint32(sz[:], uint32(len(payload)))
			dat = append(dat, sz[:]...)
			dat = append(dat, payload...)
			var e [8]byte // fileNum 0 (LE u16) + offset (LE u32) per decodeBodyIdx
			binary.LittleEndian.PutUint16(e[0:2], 0)
			binary.LittleEndian.PutUint32(e[4:8], off)
			idx = append(idx, e[:]...)
		}
		if err := os.WriteFile(filepath.Join(dir, "bodyc.0000.cdat"), dat, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "bodyc.cidx"), idx, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	legacyDir := filepath.Join(t.TempDir(), "legacy")
	framedDir := filepath.Join(t.TempDir(), "framed")
	writeMulti(legacyDir, false)
	writeMulti(framedDir, true)

	// Probes that hop between segments on every read.
	rng := rand.New(rand.NewSource(831))
	const probeCount = 60
	probes := make([]uint64, probeCount)
	for i := range probes {
		seg := uint64(i % segCount)
		probes[i] = seg*HeaderSegmentSize + uint64(rng.Intn(HeaderSegmentSize))
	}

	fz, err := freezer.NewReadOnly(gethAncientDir)
	if err != nil {
		t.Fatalf("open geth: %v", err)
	}
	t0 := time.Now()
	for _, p := range probes {
		if _, err := fz.Ancient(freezer.TableBodies, firstSeg*HeaderSegmentSize+p); err != nil {
			t.Fatalf("geth read: %v", err)
		}
	}
	gethDur := time.Since(t0)
	fz.Close()

	run := func(dir string) time.Duration {
		r, err := OpenBodyCompact(dir)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		defer r.Close()
		start := time.Now()
		for _, p := range probes {
			if _, err := r.ReadBody(p); err != nil {
				t.Fatalf("read %d: %v", p, err)
			}
		}
		return time.Since(start)
	}
	legacyDur := run(legacyDir)
	framedDur := run(framedDir)

	per := func(d time.Duration) time.Duration { return d / probeCount }
	t.Logf("%d reads hopping across %d segments:", probeCount, segCount)
	t.Logf("  geth ancient   %10s total  %10s/read", gethDur.Round(time.Millisecond), per(gethDur).Round(time.Microsecond))
	t.Logf("  bodyc legacy   %10s total  %10s/read", legacyDur.Round(time.Millisecond), per(legacyDur).Round(time.Microsecond))
	t.Logf("  bodyc framed   %10s total  %10s/read", framedDur.Round(time.Millisecond), per(framedDur).Round(time.Microsecond))
	t.Logf("  framed vs legacy: %.1fx faster;  geth vs framed: %.1fx faster",
		float64(legacyDur)/float64(framedDur), float64(framedDur)/float64(gethDur))
}
