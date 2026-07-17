// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package mobileverify

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// Sparse signer mask (design §6.1): uvarint count, then count uvarint
// deltas over the SORTED index sequence — first delta is the first
// index, each subsequent delta is (gap - 1), so the sequence is
// strictly ascending and duplicate-free by construction. A dense
// bitmap over a 10⁷–10⁸ registry would be megabytes per block while
// real per-block cohorts are sparse; this stays proportional to the
// cohort. A future dense/Roaring variant gets a format tag byte; the
// current format is implicitly tag-free v1.

// ErrBadMask reports a mask that fails structural validation.
var ErrBadMask = errors.New("mobileverify: malformed signer mask")

// EncodeMask encodes a strictly-ascending index set. The input must be
// sorted ascending with no duplicates — the collector produces exactly
// that; enforcing it here keeps every encoded mask canonical (one and
// only one encoding per set).
func EncodeMask(sorted []MobileIndex) ([]byte, error) {
	out := make([]byte, 0, 1+len(sorted)*2)
	out = binary.AppendUvarint(out, uint64(len(sorted)))
	prev := uint64(0)
	for i, idx := range sorted {
		v := uint64(idx)
		if i == 0 {
			out = binary.AppendUvarint(out, v)
		} else {
			if v <= prev {
				return nil, fmt.Errorf("%w: indices not strictly ascending (%d after %d)", ErrBadMask, v, prev)
			}
			out = binary.AppendUvarint(out, v-prev-1)
		}
		prev = v
	}
	return out, nil
}

// DecodeMask decodes and validates a mask, returning the ascending
// index set. maxIndex bounds the largest acceptable index (the
// registry size at verification time); pass the registry Count()-1, or
// a negative-free sentinel via count==0 handling below.
func DecodeMask(data []byte, registryCount int) ([]MobileIndex, error) {
	count, n := binary.Uvarint(data)
	if n <= 0 {
		return nil, fmt.Errorf("%w: bad count", ErrBadMask)
	}
	p := data[n:]
	// Every entry costs at least one byte: a count exceeding the
	// remaining bytes (or the registry) is corrupt — reject before
	// allocating.
	if count > uint64(len(p)) || count > uint64(registryCount) {
		return nil, fmt.Errorf("%w: count %d implausible (bytes=%d registry=%d)", ErrBadMask, count, len(p), registryCount)
	}
	out := make([]MobileIndex, 0, count)
	cur := uint64(0)
	for i := uint64(0); i < count; i++ {
		d, n := binary.Uvarint(p)
		if n <= 0 {
			return nil, fmt.Errorf("%w: truncated at entry %d", ErrBadMask, i)
		}
		p = p[n:]
		if i == 0 {
			cur = d
		} else {
			// step = d + 1 must not overflow, and cur must strictly
			// increase. Unchecked `cur += d + 1` lets d == MaxUint64 wrap
			// the step to 0 (duplicate index) or wrap cur past the modulus,
			// breaking the ascending/duplicate-free invariant Verify and
			// MergeCerts rely on — a duplicated index lets one key forge an
			// N-signer aggregate (BLS is linear).
			if d == ^uint64(0) {
				return nil, fmt.Errorf("%w: delta overflow at entry %d", ErrBadMask, i)
			}
			next := cur + d + 1
			if next < cur {
				return nil, fmt.Errorf("%w: index overflow at entry %d", ErrBadMask, i)
			}
			cur = next
		}
		if cur >= uint64(registryCount) {
			return nil, fmt.Errorf("%w: index %d beyond registry size %d", ErrBadMask, cur, registryCount)
		}
		out = append(out, MobileIndex(cur))
	}
	if len(p) != 0 {
		return nil, fmt.Errorf("%w: %d trailing bytes", ErrBadMask, len(p))
	}
	return out, nil
}
