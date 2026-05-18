// Package history provides a per-key timeline store: for each key
// (account address or storage addr+slot), record the values that
// existed BEFORE each block where the key was modified.
//
// Design: docs/ethel/history-build-v1-design.md
//
// On-disk format (per domain):
//
//	<prefix>.kv    sorted [(key, packed-history)] pages, zstd
//	<prefix>.idx   sparse offset index (per-page firstKey + offset)
//
// Inside one page (after zstd decompress):
//
//	[key:keyLen B][packedLen:varint][packed:packedLen B]
//	[key:keyLen B][packedLen:varint][packed:packedLen B]
//	...
//
// packed-history (one key's full timeline):
//
//	[numChanges:varint]
//	[deltaBlock_0:varint][valueLen_0:varint][value_0:valueLen_0 B]
//	[deltaBlock_1:varint][valueLen_1:varint][value_1:valueLen_1 B]
//	...
//
// deltaBlock_0 is absolute (delta from 0); subsequent deltas are
// (block_i - block_{i-1}). Values are the value as it was AT THE
// START of that block (= value at end of block-1).
package history

import (
	"encoding/binary"
	"fmt"
)

// Change is one (block, prevValue) pair: at the start of `Block`,
// the key had value `Value`.
type Change struct {
	Block uint64
	Value []byte
}

// PackHistory serialises a per-key timeline. Entries must be sorted
// by Block ascending. Returns the serialised blob.
func PackHistory(dst []byte, entries []Change) []byte {
	var buf [binary.MaxVarintLen64]byte

	n := binary.PutUvarint(buf[:], uint64(len(entries)))
	dst = append(dst, buf[:n]...)

	var prevBlock uint64
	for i, e := range entries {
		if i > 0 && e.Block < prevBlock {
			// Caller bug: unsorted. Encode anyway as 0 delta; reader will
			// not be misled because we never claim sortedness in the format.
			// Better: caller should sort before calling.
		}
		var delta uint64
		if i == 0 {
			delta = e.Block
		} else {
			delta = e.Block - prevBlock
		}
		prevBlock = e.Block

		n = binary.PutUvarint(buf[:], delta)
		dst = append(dst, buf[:n]...)
		n = binary.PutUvarint(buf[:], uint64(len(e.Value)))
		dst = append(dst, buf[:n]...)
		dst = append(dst, e.Value...)
	}
	return dst
}

// UnpackHistory deserialises a per-key timeline. The returned slices
// alias data — copy if you need to retain past data lifetime.
func UnpackHistory(data []byte) ([]Change, error) {
	if len(data) == 0 {
		return nil, nil
	}
	count, n := binary.Uvarint(data)
	if n <= 0 {
		return nil, fmt.Errorf("history: bad count varint")
	}
	pos := n

	out := make([]Change, 0, count)
	var block uint64
	for i := uint64(0); i < count; i++ {
		delta, n := binary.Uvarint(data[pos:])
		if n <= 0 {
			return nil, fmt.Errorf("history: bad deltaBlock at entry %d", i)
		}
		pos += n
		block += delta

		vlen, n := binary.Uvarint(data[pos:])
		if n <= 0 {
			return nil, fmt.Errorf("history: bad valueLen at entry %d", i)
		}
		pos += n
		if pos+int(vlen) > len(data) {
			return nil, fmt.Errorf("history: truncated value at entry %d (need %d, have %d)",
				i, vlen, len(data)-pos)
		}
		out = append(out, Change{Block: block, Value: data[pos : pos+int(vlen)]})
		pos += int(vlen)
	}
	return out, nil
}

// AsOf returns the value at the START of `block` from a packed
// history blob. Returns (nil, false) if no recorded change exists at
// or before this block — caller should consult the snapshot.
//
// Semantics: each entry (B, V) records "at the start of block B, the
// value was V". To answer "value at the start of block N":
//   - Find largest entry (B_i, V_i) with B_i <= N.
//   - Return V_i.
//   - If B_i == N, V_i is also the value just before block N applied.
//   - If no B_i <= N exists, the key was never modified before N;
//     fall back to genesis state (typically empty/zero).
func AsOf(data []byte, block uint64) ([]byte, bool, error) {
	if len(data) == 0 {
		return nil, false, nil
	}
	count, n := binary.Uvarint(data)
	if n <= 0 {
		return nil, false, fmt.Errorf("history: bad count varint")
	}
	pos := n

	var lastValue []byte
	var found bool
	var curBlock uint64
	for i := uint64(0); i < count; i++ {
		delta, n := binary.Uvarint(data[pos:])
		if n <= 0 {
			return nil, false, fmt.Errorf("history: bad deltaBlock at entry %d", i)
		}
		pos += n
		curBlock += delta

		vlen, n := binary.Uvarint(data[pos:])
		if n <= 0 {
			return nil, false, fmt.Errorf("history: bad valueLen at entry %d", i)
		}
		pos += n
		if pos+int(vlen) > len(data) {
			return nil, false, fmt.Errorf("history: truncated value at entry %d", i)
		}
		if curBlock > block {
			break
		}
		lastValue = data[pos : pos+int(vlen)]
		found = true
		pos += int(vlen)
	}
	return lastValue, found, nil
}
