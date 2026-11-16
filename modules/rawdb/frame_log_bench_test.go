// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package rawdb

import (
	"context"
	"crypto/rand"
	"hash/crc32"
	"sync"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
)

// A full block's frames: 650 frames, ~34 MB.
const benchBlockFrames, benchFrameBytes = 650, 52 << 10

func benchBlock(b *testing.B, seed byte) ([]types.Hash, [][]byte) {
	b.Helper()
	hs := make([]types.Hash, benchBlockFrames)
	raws := make([][]byte, benchBlockFrames)
	for i := range raws {
		raws[i] = make([]byte, benchFrameBytes)
		_, _ = rand.Read(raws[i])
		hs[i] = types.Hash{seed, byte(i), byte(i >> 8), 0xb0}
	}
	return hs, raws
}

// The parts of today's Enc on their own: CRC-32C over the block, and the
// copy into the reused write buffer.
func BenchmarkFrameEncodeParts(b *testing.B) {
	_, raws := benchBlock(b, 1)
	b.Run("crc32c", func(b *testing.B) {
		b.SetBytes(int64(benchBlockFrames * benchFrameBytes))
		for i := 0; i < b.N; i++ {
			for _, r := range raws {
				_ = crc32.Checksum(r, frameLogCRC)
			}
		}
	})
	b.Run("copy", func(b *testing.B) {
		b.SetBytes(int64(benchBlockFrames * benchFrameBytes))
		var buf []byte
		for i := 0; i < b.N; i++ {
			buf = buf[:0]
			for _, r := range raws {
				buf = append(buf, make([]byte, frameLogHeaderSize)...)
				buf = append(buf, r...)
			}
		}
	})
	b.Run("copy-fresh", func(b *testing.B) {
		// A buffer that is not reused (or regrows): page faults on every
		// write.
		b.SetBytes(int64(benchBlockFrames * benchFrameBytes))
		for i := 0; i < b.N; i++ {
			var buf []byte
			for _, r := range raws {
				buf = append(buf, make([]byte, frameLogHeaderSize)...)
				buf = append(buf, r...)
			}
		}
	})
}

// benchFrameLogWrite writes one block per iteration on each of writers
// goroutines and reports the mean mutex hold (Whole - LockWait) and encode.
func benchFrameLogWrite(b *testing.B, lean bool, writers int) {
	defer SetFrameLogLeanLockForTest(lean)()
	db := newFrameLogTestStore(b)
	l, err := openFrameLog(b.TempDir(), db, 0)
	if err != nil {
		b.Fatal(err)
	}
	defer l.close()
	blocks := make([][2]interface{}, writers)
	for w := range blocks {
		hs, raws := benchBlock(b, byte(w))
		blocks[w] = [2]interface{}{hs, raws}
	}
	var mu sync.Mutex
	var hold, enc time.Duration
	var n int
	b.SetBytes(int64(writers * benchBlockFrames * benchFrameBytes))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var wg sync.WaitGroup
		for w := 0; w < writers; w++ {
			wg.Add(1)
			go func(w int) {
				defer wg.Done()
				var t FrameWriteTimes
				if err := l.write(context.Background(), blocks[w][0].([]types.Hash), blocks[w][1].([][]byte), &t); err != nil {
					b.Error(err)
				}
				mu.Lock()
				hold += t.Whole - t.LockWait - t.Encode*boolInt(lean)
				enc += t.Encode
				n++
				mu.Unlock()
			}(w)
		}
		wg.Wait()
	}
	b.StopTimer()
	b.ReportMetric(float64(hold.Microseconds())/float64(n), "holdUs/write")
	b.ReportMetric(float64(enc.Microseconds())/float64(n), "encUs/write")
}

func boolInt(v bool) time.Duration {
	if v {
		return 1
	}
	return 0
}

func BenchmarkFrameLogWrite(b *testing.B) {
	for _, lean := range []bool{false, true} {
		for _, writers := range []int{1, 2} {
			name := "copy"
			if lean {
				name = "lean"
			}
			b.Run(name+"/writers="+string(rune('0'+writers)), func(b *testing.B) { benchFrameLogWrite(b, lean, writers) })
		}
	}
}
