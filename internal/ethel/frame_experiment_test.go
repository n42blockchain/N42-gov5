// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// frame_experiment_test.go — quantifies what sub-segment framing would cost in
// compression ratio and buy in random-read latency.
//
// Today a segment is the unit of decompression: reading one block decompresses
// and decodes all 8192 in its segment (measured 2.8-4.0 s cold). Framing fixes
// that, but shorter columns compress worse — these tests measure both sides on
// real mainnet data before a size is chosen.
//
//	go test -tags nosqlite,noboltdb -run 'TestFrame' -v ./internal/ethel/
//
// Skipped unless the real bodyc freezer is present.
package ethel

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
)

const frameExpDir = `D:\n42-eth1\chain\freezer`

// loadOneSegment returns every block of the segment containing `probe`.
func loadOneSegment(t *testing.T, probe uint64) []*DecodedBlock {
	t.Helper()
	if _, err := os.Stat(filepath.Join(frameExpDir, "bodyc.cidx")); err != nil {
		t.Skip("bodyc freezer not present")
	}
	r, err := OpenBodyCompact(frameExpDir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { r.Close() })

	segNum := probe / HeaderSegmentSize
	base := segNum * HeaderSegmentSize
	blocks := make([]*DecodedBlock, 0, HeaderSegmentSize)
	t0 := time.Now()
	for i := uint64(0); i < HeaderSegmentSize; i++ {
		b, err := r.ReadBody(base + i)
		if err != nil {
			t.Fatalf("read block %d: %v", base+i, err)
		}
		blocks = append(blocks, b)
	}
	t.Logf("segment %d (blocks %d..%d) loaded in %s",
		segNum, base, base+HeaderSegmentSize-1, time.Since(t0).Round(time.Millisecond))
	return blocks
}

// TestFrameSizeTradeoff reports the compressed size at each candidate frame
// size against today's whole-segment baseline.
func TestFrameSizeTradeoff(t *testing.T) {
	blocks := loadOneSegment(t, 25_000_000)

	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault))
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()

	var baseline int
	for _, F := range []int{HeaderSegmentSize, 2048, 1024, 512, 256, 128, 64} {
		total, frames := 0, 0
		for i := 0; i < len(blocks); i += F {
			end := i + F
			if end > len(blocks) {
				end = len(blocks)
			}
			total += len(encodeBodySegment(blocks[i:end], 1, enc))
			frames++
		}
		if F == HeaderSegmentSize {
			baseline = total
			t.Logf("F=%-5d frames=%-4d bytes=%-12d (baseline)", F, frames, total)
			continue
		}
		t.Logf("F=%-5d frames=%-4d bytes=%-12d %+6.2f%% vs baseline, %dx fewer blocks per random read",
			F, frames, total, float64(total-baseline)/float64(baseline)*100, HeaderSegmentSize/F)
	}
}

// TestFrameDecodeLatency checks that framing actually delivers the latency the
// size numbers imply. DATC's leaf segments showed naive framing can be SLOWER
// (4.5x at p50) when every frame allocates its own decoder, so this reuses one
// decoder with DecodeAll — the shape a real reader would use.
func TestFrameDecodeLatency(t *testing.T) {
	blocks := loadOneSegment(t, 25_000_000)

	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault))
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()
	dec, err := zstd.NewReader(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Close()

	const iters = 20
	for _, F := range []int{HeaderSegmentSize, 1024, 256, 64} {
		mid := len(blocks) / 2
		start := (mid / F) * F
		end := start + F
		if end > len(blocks) {
			end = len(blocks)
		}
		payload := encodeBodySegment(blocks[start:end], 1, enc)

		var best, total time.Duration
		for i := 0; i < iters; i++ {
			t0 := time.Now()
			raw, err := dec.DecodeAll(payload, nil)
			if err != nil {
				t.Fatalf("F=%d decode: %v", F, err)
			}
			if _, err := decodeBodySegment(raw); err != nil {
				t.Fatalf("F=%d parse: %v", F, err)
			}
			d := time.Since(t0)
			total += d
			if best == 0 || d < best {
				best = d
			}
		}
		t.Logf("F=%-5d payload=%-11d decode+parse best=%-11s avg=%s",
			F, len(payload), best.Round(time.Microsecond), (total / iters).Round(time.Microsecond))
	}
}
