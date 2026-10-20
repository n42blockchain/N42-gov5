package recsplit

import (
	"bytes"
	"context"
	"encoding/binary"
	"math/rand"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/log/v3"
)

// TestCheapBuildByteIdentical builds the same keys with CheapBuild on and
// off and requires byte-identical index files, across the shapes in use
// (txlookup's sealer leaf 4 / bucket 100 Enums+LFP, the freezer builder's
// leaf 8 / bucket 2000) plus plain offsets, the etl path and NoValues.
func TestCheapBuildByteIdentical(t *testing.T) {
	for _, tc := range []struct {
		name               string
		enums, lfp, inMem  bool
		noValues           bool
		keys, bucket, leaf int
	}{
		{"sealer", true, true, true, false, 300_000, 100, 4},
		{"freezer", true, true, true, false, 60_000, 2000, 8},
		{"leaf5", true, true, true, false, 40_000, 100, 5},
		{"offsets", false, false, true, false, 30_000, 100, 4},
		{"etl", true, true, false, false, 30_000, 100, 4},
		{"novalues", false, false, true, true, 30_000, 100, 4},
		{"small", true, true, true, false, 7, 100, 4},
		{"one", true, true, true, false, 1, 100, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			build := func(cheap bool) []byte {
				prev := CheapBuild
				CheapBuild = cheap
				defer func() { CheapBuild = prev }()
				dir := t.TempDir()
				idx := filepath.Join(dir, "x.idx")
				rs, err := NewRecSplit(RecSplitArgs{
					KeyCount: tc.keys, BucketSize: tc.bucket, LeafSize: uint16(tc.leaf),
					Enums: tc.enums, LessFalsePositives: tc.lfp, NoValues: tc.noValues,
					InMemory: tc.inMem, Salt: 0x5eed, IndexFile: idx, TmpDir: dir,
				}, log.New())
				if err != nil {
					t.Fatal(err)
				}
				defer rs.Close()
				rs.DisableFsync()
				var buf [8]byte
				for i := 0; i < tc.keys; i++ {
					binary.BigEndian.PutUint64(buf[:], uint64(i))
					h := crypto.Keccak256(buf[:])
					if err := rs.AddKey(h, uint64(i)*3); err != nil {
						t.Fatal(err)
					}
				}
				if err := rs.Build(context.Background()); err != nil {
					t.Fatal(err)
				}
				b, err := os.ReadFile(idx)
				if err != nil {
					t.Fatal(err)
				}
				return b
			}
			off := build(false)
			on := build(true)
			on2 := build(true) // pooled buffers on a second build
			if !bytes.Equal(off, on) || !bytes.Equal(on, on2) {
				t.Fatalf("index differs: off %d bytes, on %d, on2 %d", len(off), len(on), len(on2))
			}
		})
	}
}

// TestSortMemBucketMatchesSortFunc checks the inlined sort against the
// library sort, including duplicate fingerprints and fully equal entries.
func TestSortMemBucketMatchesSortFunc(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	cmp := func(a, b memBucketKey) int {
		if memKeyLess(&a, &b) {
			return -1
		}
		if memKeyLess(&b, &a) {
			return 1
		}
		return 0
	}
	for it := 0; it < 3000; it++ {
		n := r.Intn(400)
		s := make([]memBucketKey, n)
		mod := uint64(1) << 63
		if it%3 == 1 {
			mod = uint64(n/4 + 1) // many duplicate fps
		}
		for i := range s {
			s[i] = memBucketKey{fp: r.Uint64() % mod, val: uint64(r.Intn(n + 1))}
			if it%5 == 2 {
				s[i].fp = uint64(i) // already sorted
			}
		}
		want := slices.Clone(s)
		slices.SortFunc(want, cmp)
		sortMemBucket(s)
		if !slices.Equal(s, want) {
			t.Fatalf("iteration %d (n=%d): order differs", it, n)
		}
	}
}

// TestFanout2ThresholdMatchesDivision checks the binary-split classifier
// against remap16/unit over every (m, unit) a fanout-2 split can see.
func TestFanout2ThresholdMatchesDivision(t *testing.T) {
	r := rand.New(rand.NewSource(2))
	for _, leaf := range []uint16{4, 5, 8, 16, 24} {
		rs := &RecSplit{leafSize: leaf}
		rs.primaryAggrBound = leaf * uint16(max(2, int(0.35*float64(leaf)+1.5)))
		for m := uint16(2); m < 4000; m++ {
			fanout, unit := splitParams(m, leaf, rs.primaryAggrBound, rs.primaryAggrBound*2)
			if fanout != 2 {
				continue
			}
			bound := uint64(unit) << 48
			for k := 0; k < 200; k++ {
				h := r.Uint64()
				if k < 2 {
					h = uint64(k) * ^uint64(0)
				}
				want := remap16(h, m)/unit == 0
				got := (h&mask48)*uint64(m) < bound
				if want != got {
					t.Fatalf("leaf %d m %d unit %d h %x: division %v threshold %v", leaf, m, unit, h, want, got)
				}
			}
		}
	}
}

// TestMemBucketSorterMatchesSortFunc checks the distribution sort the same way.
func TestMemBucketSorterMatchesSortFunc(t *testing.T) {
	r := rand.New(rand.NewSource(3))
	var ms memBucketSorter
	for it := 0; it < 3000; it++ {
		n := r.Intn(1500)
		s := make([]memBucketKey, n)
		for i := range s {
			s[i] = memBucketKey{fp: r.Uint64(), val: uint64(i)}
			switch it % 4 {
			case 1:
				s[i].fp >>= 60 // all in a few slots, many duplicates
			case 2:
				s[i].fp = ^uint64(0) - uint64(r.Intn(3))
			}
		}
		want := slices.Clone(s)
		slices.SortFunc(want, func(a, b memBucketKey) int {
			if memKeyLess(&a, &b) {
				return -1
			}
			if memKeyLess(&b, &a) {
				return 1
			}
			return 0
		})
		ms.sort(s)
		if !slices.Equal(s, want) {
			t.Fatalf("iteration %d (n=%d): order differs", it, n)
		}
	}
}
