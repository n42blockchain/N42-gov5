// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// frame_take_test.go — measures the destructive sequential read (TakeBody):
// decode one frame, hand blocks out one at a time, release each as it goes,
// move to the next frame when the current one is exhausted.
//
// The point is resident memory. A legacy segment must materialise all 8192
// blocks before the first one can be handed out, and every undelivered block
// stays alive until the next segment replaces it. Framed + release keeps only
// the live frame.

package ethel

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
)

// TestTakeBodyReleasesEagerly verifies the destructive read actually lets go:
// during a sequential sweep the reader must never retain more than the live
// frame. Heap sampling cannot show this (HeapAlloc counts allocation, not
// liveness, and a sweep allocates GBs regardless), so this inspects the
// reader's own retention directly.
func TestTakeBodyReleasesEagerly(t *testing.T) {
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
	src = nil

	// retained counts blocks the reader still points at.
	retained := func(r *BodyCompactReader) int {
		n := 0
		for _, b := range r.cachedBlocks {
			if b != nil {
				n++
			}
		}
		for _, c := range r.frameCache {
			for _, b := range c.blocks {
				if b != nil {
					n++
				}
			}
		}
		return n
	}

	sweep := func(dir string) (maxRetained, maxFrames int) {
		r, err := OpenBodyCompact(dir)
		if err != nil {
			t.Fatalf("open %s: %v", dir, err)
		}
		defer r.Close()
		for i := uint64(0); i < HeaderSegmentSize; i++ {
			if _, err := r.TakeBodyNoAhead(i); err != nil {
				t.Fatalf("%s take %d: %v", dir, i, err)
			}
			if n := retained(r); n > maxRetained {
				maxRetained = n
			}
			if len(r.frameCache) > maxFrames {
				maxFrames = len(r.frameCache)
			}
		}
		return
	}

	lRet, _ := sweep(legacyDir)
	fRet, fFrames := sweep(framedDir)

	t.Logf("sequential destructive sweep of %d blocks — blocks still held by the reader:", HeaderSegmentSize)
	t.Logf("  legacy  max retained %5d blocks", lRet)
	t.Logf("  framed  max retained %5d blocks (max %d frame(s) cached)", fRet, fFrames)
	t.Logf("  retention reduction: %.1fx", float64(lRet)/float64(fRet))

	if fRet > bodyFrameSize {
		t.Errorf("framed reader retained %d blocks, want <= one frame (%d)", fRet, bodyFrameSize)
	}
	if fFrames > 1 {
		t.Errorf("framed reader kept %d frames during a sequential sweep, want 1", fFrames)
	}
}

func TestTakeBodyResidentMemory(t *testing.T) {
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

	// Drop the source blocks before measuring. They are a fully decoded
	// segment in their own right (~GBs) and would otherwise dominate every
	// heap sample, hiding exactly the difference under test.
	wantTxs := 0
	for _, b := range src {
		wantTxs += len(b.Txs)
	}
	src = nil
	runtime.GC()
	runtime.GC()

	// Sweep the whole segment with TakeBodyNoAhead (no read-ahead, so the
	// numbers describe this reader alone), sampling the heap as we go.
	sweep := func(dir string) (peakMB, endMB float64, dur time.Duration, txs int) {
		r, err := OpenBodyCompact(dir)
		if err != nil {
			t.Fatalf("open %s: %v", dir, err)
		}
		defer r.Close()

		runtime.GC()
		var base runtime.MemStats
		runtime.ReadMemStats(&base)

		var peak uint64
		start := time.Now()
		for i := uint64(0); i < HeaderSegmentSize; i++ {
			b, err := r.TakeBodyNoAhead(i)
			if err != nil {
				t.Fatalf("%s take %d: %v", dir, i, err)
			}
			txs += len(b.Txs)
			// Drop our own reference immediately — the reader's retention is
			// what is under test, not the caller's.
			b = nil
			_ = b
			if i%512 == 0 {
				var m runtime.MemStats
				runtime.ReadMemStats(&m)
				if m.HeapAlloc > peak {
					peak = m.HeapAlloc
				}
			}
		}
		d := time.Since(start)
		runtime.GC()
		var end runtime.MemStats
		runtime.ReadMemStats(&end)
		return float64(peak) / 1024 / 1024, float64(end.HeapAlloc) / 1024 / 1024, d, txs
	}

	lPeak, lEnd, lDur, lTx := sweep(legacyDir)
	fPeak, fEnd, fDur, fTx := sweep(framedDir)

	if lTx != fTx || lTx != wantTxs {
		t.Fatalf("tx count mismatch: legacy %d framed %d want %d", lTx, fTx, wantTxs)
	}
	t.Logf("full-segment destructive sweep (%d blocks, %d txs):", HeaderSegmentSize, lTx)
	t.Logf("  legacy  peak heap %8.1f MB  after-GC %7.1f MB  %s", lPeak, lEnd, lDur.Round(time.Millisecond))
	t.Logf("  framed  peak heap %8.1f MB  after-GC %7.1f MB  %s", fPeak, fEnd, fDur.Round(time.Millisecond))
	t.Logf("  peak reduction: %.2fx", lPeak/fPeak)
}

// TestParallelReadersRetention models what openParallelReplayInput actually
// does: several independent readers, each sweeping its own range, each with its
// own cache. This is where whole-segment caching hurts most — N readers pin N
// segments — and it is also where framing must not starve the workers pulling
// blocks out, so both retention and throughput are measured.
func TestParallelReadersRetention(t *testing.T) {
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
	src = nil

	const readers = 8
	retained := func(r *BodyCompactReader) int {
		n := 0
		for _, b := range r.cachedBlocks {
			if b != nil {
				n++
			}
		}
		for _, c := range r.frameCache {
			for _, b := range c.blocks {
				if b != nil {
					n++
				}
			}
		}
		return n
	}

	// Each reader walks a disjoint slice of the segment, as parallel replay
	// inputs do; retention is sampled across all of them simultaneously.
	sweep := func(dir string) (peakTotal int, dur time.Duration) {
		rs := make([]*BodyCompactReader, readers)
		for i := range rs {
			r, err := OpenBodyCompact(dir)
			if err != nil {
				t.Fatalf("open %s: %v", dir, err)
			}
			rs[i] = r
		}
		defer func() {
			for _, r := range rs {
				r.Close()
			}
		}()

		per := HeaderSegmentSize / readers
		start := time.Now()
		for step := 0; step < per; step++ {
			for i, r := range rs {
				blk := uint64(i*per + step)
				if _, err := r.TakeBodyNoAhead(blk); err != nil {
					t.Fatalf("%s reader %d block %d: %v", dir, i, blk, err)
				}
			}
			if step%64 == 0 {
				total := 0
				for _, r := range rs {
					total += retained(r)
				}
				if total > peakTotal {
					peakTotal = total
				}
			}
		}
		return peakTotal, time.Since(start)
	}

	lPeak, lDur := sweep(legacyDir)
	fPeak, fDur := sweep(framedDir)

	t.Logf("%d parallel readers, disjoint ranges, destructive reads:", readers)
	t.Logf("  legacy  peak blocks held across all readers: %6d   sweep %s", lPeak, lDur.Round(time.Millisecond))
	t.Logf("  framed  peak blocks held across all readers: %6d   sweep %s", fPeak, fDur.Round(time.Millisecond))
	t.Logf("  retention reduction %.1fx;  throughput ratio %.2fx (>1 means framed is faster)",
		float64(lPeak)/float64(fPeak), float64(lDur)/float64(fDur))

	if fPeak > readers*bodyFrameSize {
		t.Errorf("framed peak %d exceeds %d readers x one frame (%d)", fPeak, readers, bodyFrameSize)
	}
}
