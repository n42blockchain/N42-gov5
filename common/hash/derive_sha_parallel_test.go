// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.

package hash

import (
	"bytes"
	"fmt"
	"testing"
)

// varList is a DerivableList whose leaf values vary in length with the index,
// so a run covers both the short values a trie INLINES into its parent and the
// long ones it references by hash.
type varList struct {
	n    int
	kind int
}

func (l varList) Len() int { return l.n }
func (l varList) EncodeIndex(i int, w *bytes.Buffer) {
	var size int
	switch l.kind {
	case 0: // always short: 1-3 bytes, inlined by the parent node
		size = 1 + i%3
	case 1: // always long: ~110 bytes, the shape of a signed transfer
		size = 110
	default: // mixed, straddling the 32-byte inline boundary
		size = 1 + (i*7)%64
	}
	for j := 0; j < size; j++ {
		w.WriteByte(byte(i*31 + j*17))
	}
}

// TestDeriveShaErigonParallelEncodeMatchesSequential pins the only thing the
// parallel encode is allowed to change: nothing. The same list must produce the
// same root whether the values were encoded inline by the trie walk or ahead of
// it on the worker pool.
//
// The sizes bracket every place the RLP(index) key changes shape -- 1 byte for
// 0 and 1..127, two for 128..255, three above -- because that is where the
// nibble ordering and the trie's structure change, and a bug that only appears
// at a length boundary would otherwise pass.
func TestDeriveShaErigonParallelEncodeMatchesSequential(t *testing.T) {
	sizes := []int{
		1, 2, 3, 16, 126, 127, 128, 129, 130, 254, 255, 256, 257, 258,
		511, 512, 513, 1023, 2047, 2048, 2049, 4096, 11000, 22857,
	}
	orig := parallelEncodeMinLeaves
	t.Cleanup(func() { parallelEncodeMinLeaves = orig })

	for _, kind := range []int{0, 1, 2} {
		for _, n := range sizes {
			list := varList{n: n, kind: kind}

			parallelEncodeMinLeaves = 1 << 30 // force the inline encode
			want := DeriveShaErigon(list)

			parallelEncodeMinLeaves = 1 // force the worker pool
			got := DeriveShaErigon(list)

			if got != want {
				t.Fatalf("kind=%d n=%d: parallel encode gave %s, sequential %s",
					kind, n, got.Hex(), want.Hex())
			}
		}
	}
}

// TestEncodeValuesParallelMatchesInline checks the values themselves, not just
// the root they produce, so a failure says which index diverged.
func TestEncodeValuesParallelMatchesInline(t *testing.T) {
	orig := parallelEncodeMinLeaves
	t.Cleanup(func() { parallelEncodeMinLeaves = orig })
	parallelEncodeMinLeaves = 1

	for _, kind := range []int{0, 1, 2} {
		for _, n := range []int{3, 129, 257, 5000} {
			list := varList{n: n, kind: kind}
			got := encodeValuesParallel(list, n)
			if got == nil {
				t.Fatalf("kind=%d n=%d: expected the parallel path", kind, n)
			}
			var buf bytes.Buffer
			for i := 0; i < n; i++ {
				buf.Reset()
				list.EncodeIndex(i, &buf)
				if !bytes.Equal(got[i], buf.Bytes()) {
					t.Fatalf("kind=%d n=%d index %d: parallel %x, inline %x",
						kind, n, i, got[i], buf.Bytes())
				}
			}
		}
	}
}

// TestDeriveShaErigonParallelBelowThreshold confirms small lists still take the
// inline path, so the pool is never paid for on a list too small to repay it.
func TestDeriveShaErigonParallelBelowThreshold(t *testing.T) {
	if v := encodeValuesParallel(varList{n: 100, kind: 1}, 100); v != nil {
		t.Fatalf("a 100-leaf list should encode inline, got a %d-value slice", len(v))
	}
}

func BenchmarkDeriveShaErigonEncodePath(b *testing.B) {
	orig := parallelEncodeMinLeaves
	b.Cleanup(func() { parallelEncodeMinLeaves = orig })
	for _, n := range []int{22857} {
		list := varList{n: n, kind: 1}
		for _, mode := range []struct {
			name string
			thr  int
		}{{"inline", 1 << 30}, {"parallel", 1}} {
			b.Run(fmt.Sprintf("%s/txs=%d", mode.name, n), func(b *testing.B) {
				parallelEncodeMinLeaves = mode.thr
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					_ = DeriveShaErigon(list)
				}
				b.StopTimer()
				b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/float64(n)/1000, "us/tx")
			})
		}
	}
}
