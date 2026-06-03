// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// witness_reorder.go — bounded in-order reorder buffer for the witness-replay
// aggregator. Parallel workers finish blocks out of order; the aggregator must
// emit them in block order (the output cdat is append-only by block). This
// buffers out-of-order results and drains contiguous runs from the head.
//
// Backed by a fixed-size RING (slot = blockNum % size) instead of a
// map[uint64]WitnessResult: adjacent blocks sit in adjacent slots (cache-
// friendly drain), there is no per-entry map allocation/rehash, and draining a
// run is a tight index loop. A drained slot is zeroed so the blob slices it held
// are released to the GC immediately. A small overflow map is a correctness
// safety net for the (normally impossible, given the bounded channel
// look-ahead) case of a result arriving more than `size` blocks ahead of the
// head; it stays empty in steady state.

package ethel

type reorderBuffer struct {
	ring     []WitnessResult
	present  []bool
	size     uint64
	overflow map[uint64]WitnessResult // nil/empty in steady state
	count    int
}

func newReorderBuffer(size int) *reorderBuffer {
	if size < 1 {
		size = 1
	}
	return &reorderBuffer{
		ring:    make([]WitnessResult, size),
		present: make([]bool, size),
		size:    uint64(size),
	}
}

// put stores a result for later in-order draining.
func (rb *reorderBuffer) put(r WitnessResult) {
	slot := r.BlockNum % rb.size
	switch {
	case !rb.present[slot]:
		rb.ring[slot] = r
		rb.present[slot] = true
		rb.count++
	case rb.ring[slot].BlockNum == r.BlockNum:
		rb.ring[slot] = r // duplicate delivery — overwrite, no count change
	default:
		// Slot already holds a different un-drained block (window too small):
		// fall back to the overflow map so correctness is preserved.
		if rb.overflow == nil {
			rb.overflow = make(map[uint64]WitnessResult)
		}
		if _, dup := rb.overflow[r.BlockNum]; !dup {
			rb.count++
		}
		rb.overflow[r.BlockNum] = r
	}
}

// take removes and returns the result for blockNum (ok=false if not present).
// The returned slot is zeroed so its blob slices become unreferenced.
func (rb *reorderBuffer) take(blockNum uint64) (WitnessResult, bool) {
	slot := blockNum % rb.size
	if rb.present[slot] && rb.ring[slot].BlockNum == blockNum {
		r := rb.ring[slot]
		rb.ring[slot] = WitnessResult{}
		rb.present[slot] = false
		rb.count--
		return r, true
	}
	if rb.overflow != nil {
		if r, ok := rb.overflow[blockNum]; ok {
			delete(rb.overflow, blockNum)
			rb.count--
			return r, true
		}
	}
	return WitnessResult{}, false
}

// len returns the number of buffered (not-yet-drained) results.
func (rb *reorderBuffer) len() int { return rb.count }

// overflowLen reports how many entries spilled to the overflow map (0 in steady
// state; >0 means the ring window was undersized for the actual look-ahead).
func (rb *reorderBuffer) overflowLen() int { return len(rb.overflow) }
