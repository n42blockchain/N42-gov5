// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package recsplit

import (
	"context"
	"math"
	"os"
	"slices"
	"sync"

	"github.com/spaolacci/murmur3"
)

// CheapBuild selects the cheaper construction paths of an in-memory build
// (N42_BG_CHEAP, default on; "0" restores the original code). Every cheap
// path computes exactly what the original computes, so the index bytes are
// identical either way; TestCheapBuildByteIdentical asserts it. Tests may
// flip it; it is not meant to change while a build runs.
//
//   - AddKey hashes with murmur3.Sum128WithSeed instead of the streaming
//     Hash128 (Reset/Write/Sum128 through an interface, plus a tail copy):
//     the same MurmurHash3 x64-128 of the same bytes under the same seed.
//   - A binary split (fanout 2, every split of a leaf-4 / bucket-100 build)
//     classifies a key by comparing (h & mask48) * m with unit << 48 rather
//     than dividing remap16(h, m) by unit: floor(y / 2^48) < unit exactly
//     when y < unit * 2^48, so the side chosen is the same.
//   - Each bucket's keys are ordered by an inlined insertion/quick sort on
//     (fp, val) instead of slices.SortFunc through a comparison closure. The
//     order is a total order, so any correct sort yields the same sequence.
//   - The per-bucket unary code buffer and the large key slices are reused
//     across buckets and across builds instead of reallocated.
var CheapBuild = os.Getenv("N42_BG_CHEAP") != "0"

// memKeyPool holds []memBucketKey buffers between builds. A sealer segment
// is ~1.1M keys, so each build allocated two ~27 MB slices (the keys and the
// bucket-sorted copy) and dropped them a few hundred milliseconds later.
var memKeyPool sync.Pool

// offsetPool holds the []uint64 offset buffers of Enums builds.
var offsetPool sync.Pool

func getMemKeys(n int) []memBucketKey {
	if v, ok := memKeyPool.Get().(*[]memBucketKey); ok && cap(*v) >= n {
		return (*v)[:0]
	}
	return make([]memBucketKey, 0, n)
}

func putMemKeys(s []memBucketKey) {
	if cap(s) == 0 {
		return
	}
	s = s[:0]
	memKeyPool.Put(&s)
}

func getOffsets(n int) []uint64 {
	if v, ok := offsetPool.Get().(*[]uint64); ok && cap(*v) >= n {
		return (*v)[:0]
	}
	return make([]uint64, 0, n)
}

func putOffsets(s []uint64) {
	if cap(s) == 0 {
		return
	}
	s = s[:0]
	offsetPool.Put(&s)
}

func memKeyLess(a, b *memBucketKey) bool {
	if a.fp != b.fp {
		return a.fp < b.fp
	}
	return a.val < b.val
}

// sortMemBucket orders one bucket's keys by (fp, val), the order
// slices.SortFunc with loadMemBuckets' comparison produces. Buckets hold
// ~BucketSize keys with uniformly random fingerprints, so a median-of-three
// quicksort over an insertion sort is fast and never degenerates in
// practice; the depth limit falls back to the library sort anyway.
func sortMemBucket(s []memBucketKey) {
	quickSortMem(s, 2*bitsLen(len(s)))
}

// memBucketSorter orders buckets by a distribution pass on the top bits of
// the fingerprint followed by an insertion sort, linear in expectation for
// uniformly random fingerprints. The insertion sort compares (fp, val), so
// the result is the same total order whatever the distribution looks like;
// a skewed bucket only costs time, and a large one goes to sortMemBucket.
type memBucketSorter struct {
	tmp    []memBucketKey
	counts []uint32
}

func (ms *memBucketSorter) sort(s []memBucketKey) {
	n := len(s)
	if n <= 16 || n > 1<<10 {
		sortMemBucket(s)
		return
	}
	shift := uint(64 - bitsLen(n))
	slots := 1 << bitsLen(n) // fp>>shift < slots, about 1-2 slots a key
	if cap(ms.counts) < slots+1 {
		ms.counts = make([]uint32, slots+1)
	}
	counts := ms.counts[:slots+1]
	clear(counts)
	for i := range s {
		counts[(s[i].fp>>shift)+1]++
	}
	for i := 1; i <= slots; i++ {
		counts[i] += counts[i-1]
	}
	if cap(ms.tmp) < n {
		ms.tmp = make([]memBucketKey, n)
	}
	tmp := ms.tmp[:n]
	for i := range s {
		b := s[i].fp >> shift
		tmp[counts[b]] = s[i]
		counts[b]++
	}
	copy(s, tmp)
	for i := 1; i < n; i++ {
		x := s[i]
		j := i
		for j > 0 && memKeyLess(&x, &s[j-1]) {
			s[j] = s[j-1]
			j--
		}
		s[j] = x
	}
}

func bitsLen(n int) int {
	l := 0
	for n > 0 {
		l++
		n >>= 1
	}
	return l
}

func quickSortMem(s []memBucketKey, depth int) {
	for len(s) > 16 {
		if depth == 0 {
			slices.SortFunc(s, func(a, b memBucketKey) int {
				if memKeyLess(&a, &b) {
					return -1
				}
				if memKeyLess(&b, &a) {
					return 1
				}
				return 0
			})
			return
		}
		depth--
		// Median of three to s[0].
		m, last := len(s)/2, len(s)-1
		if memKeyLess(&s[m], &s[0]) {
			s[m], s[0] = s[0], s[m]
		}
		if memKeyLess(&s[last], &s[0]) {
			s[last], s[0] = s[0], s[last]
		}
		if memKeyLess(&s[last], &s[m]) {
			s[last], s[m] = s[m], s[last]
		}
		s[0], s[m] = s[m], s[0]
		pivot := s[0]
		// Hoare partition around pivot.
		i, j := 1, last
		for {
			for i <= j && memKeyLess(&s[i], &pivot) {
				i++
			}
			for i <= j && memKeyLess(&pivot, &s[j]) {
				j--
			}
			if i >= j {
				break
			}
			s[i], s[j] = s[j], s[i]
			i++
			j--
		}
		s[0], s[j] = s[j], s[0]
		// Recurse into the smaller side, loop on the larger.
		if j < len(s)-1-j {
			quickSortMem(s[:j], depth)
			s = s[j+1:]
		} else {
			quickSortMem(s[j+1:], depth)
			s = s[:j]
		}
	}
	for i := 1; i < len(s); i++ {
		x := s[i]
		j := i
		for j > 0 && memKeyLess(&x, &s[j-1]) {
			s[j] = s[j-1]
			j--
		}
		s[j] = x
	}
}

// addKeyMem is AddKey for an in-memory build under CheapBuild: the same
// fields, without the streaming hasher and without encoding the etl key
// buffers an in-memory build never reads.
func (rs *RecSplit) addKeyMem(key []byte, offset uint64) error {
	hi, lo := murmur3.Sum128WithSeed(key, rs.salt)
	if offset > rs.maxOffset {
		rs.maxOffset = offset
	}
	if rs.keysAdded > 0 {
		delta := offset - rs.prevOffset
		if rs.keysAdded == 1 || delta < rs.minDelta {
			rs.minDelta = delta
		}
	}
	bucket := remap(hi, rs.bucketCount)
	if rs.enums {
		rs.memOffsets = append(rs.memOffsets, offset)
		rs.memKeys = append(rs.memKeys, memBucketKey{bucket: bucket, fp: lo, val: rs.keysAdded})
		if rs.lessFalsePositives {
			if err := rs.existenceW.WriteByte(byte(hi)); err != nil {
				return err
			}
		}
	} else {
		rs.memKeys = append(rs.memKeys, memBucketKey{bucket: bucket, fp: lo, val: offset})
	}
	rs.keysAdded++
	rs.prevOffset = offset
	return nil
}

// loadMemBucketsCheap is loadMemBuckets under CheapBuild: the same counting
// sort and the same per-bucket order, with pooled buffers and sortMemBucket.
func (rs *RecSplit) loadMemBucketsCheap(ctx context.Context, keys []memBucketKey) error {
	starts := make([]uint32, rs.bucketCount+1)
	for i := range keys {
		starts[keys[i].bucket+1]++
	}
	for b := uint64(1); b <= rs.bucketCount; b++ {
		starts[b] += starts[b-1]
	}
	sorted := getMemKeys(len(keys))[:len(keys)]
	defer putMemKeys(sorted)
	next := make([]uint32, rs.bucketCount)
	copy(next, starts[:rs.bucketCount])
	for i := range keys {
		b := keys[i].bucket
		sorted[next[b]] = keys[i]
		next[b]++
	}
	putMemKeys(keys)
	var sorter memBucketSorter
	for b := uint64(0); b < rs.bucketCount; b++ {
		lo, hi := starts[b], starts[b+1]
		if lo == hi {
			continue
		}
		if b&1023 == 0 && ctx.Err() != nil {
			return ctx.Err()
		}
		part := sorted[lo:hi]
		sorter.sort(part)
		if rs.currentBucketIdx != math.MaxUint64 {
			if err := rs.recsplitCurrentBucket(); err != nil {
				return err
			}
		}
		rs.currentBucketIdx = b
		for _, k := range part {
			rs.currentBucket = append(rs.currentBucket, k.fp)
			rs.currentBucketOffs = append(rs.currentBucketOffs, k.val)
		}
	}
	return nil
}
