package history

import (
	"encoding/binary"
	"fmt"
)

// GroupedHistory holds one (subKey, changes) pair inside a group blob.
// Used to collapse "address+slot → history" into "address → list of
// (slot, history)" so the shared addr is stored once per page, not
// once per entry.
type GroupedHistory struct {
	SubKey  []byte
	Changes []Change
}

// PackGrouped serialises a list of (subKey, changes) entries inside
// one group blob.
//
// Layout:
//
//	[numEntries:varint]
//	for each entry:
//	   [subKey:subKeyLen B]   (caller asserts a fixed sub-key length)
//	   [packedHistoryLen:varint]
//	   [packedHistory:N B]    (per PackHistory format)
//
// SubKey length is implicit — callers fix it (e.g., 32B for storage slots).
//
// Entries should be sorted by subKey ascending.
func PackGrouped(dst []byte, subKeyLen int, entries []GroupedHistory) []byte {
	var buf [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(buf[:], uint64(len(entries)))
	dst = append(dst, buf[:n]...)

	for _, e := range entries {
		if len(e.SubKey) != subKeyLen {
			// Caller bug; encode with the wrong length and reader will fail.
		}
		dst = append(dst, e.SubKey...)
		history := PackHistory(nil, e.Changes)
		n = binary.PutUvarint(buf[:], uint64(len(history)))
		dst = append(dst, buf[:n]...)
		dst = append(dst, history...)
	}
	return dst
}

// UnpackGrouped deserialises a group blob. The returned SubKey and
// per-Change Value slices alias data — caller must copy if needed
// past the lifetime of data.
func UnpackGrouped(data []byte, subKeyLen int) ([]GroupedHistory, error) {
	if len(data) == 0 {
		return nil, nil
	}
	count, n := binary.Uvarint(data)
	if n <= 0 {
		return nil, fmt.Errorf("grouped: bad count varint")
	}
	pos := n

	out := make([]GroupedHistory, 0, count)
	for i := uint64(0); i < count; i++ {
		if pos+subKeyLen > len(data) {
			return nil, fmt.Errorf("grouped: truncated subKey at entry %d", i)
		}
		subKey := data[pos : pos+subKeyLen]
		pos += subKeyLen

		blobLen, n := binary.Uvarint(data[pos:])
		if n <= 0 {
			return nil, fmt.Errorf("grouped: bad blobLen at entry %d", i)
		}
		pos += n
		if pos+int(blobLen) > len(data) {
			return nil, fmt.Errorf("grouped: truncated history blob at entry %d (need %d, have %d)",
				i, blobLen, len(data)-pos)
		}
		changes, err := UnpackHistory(data[pos : pos+int(blobLen)])
		if err != nil {
			return nil, fmt.Errorf("grouped: entry %d unpack: %w", i, err)
		}
		pos += int(blobLen)
		out = append(out, GroupedHistory{SubKey: subKey, Changes: changes})
	}
	return out, nil
}

// AsOfGrouped is the analogue of AsOf for a grouped blob: find the
// SubKey, then return its value at the start of `block`. Linear scan
// across sub-entries (typical addresses have <1000 touched slots, so
// linear scan is cache-friendly compared to binary search overhead).
//
// Returns (value, found, err). `found=false` means either the subKey
// is not present in the group, or the subKey is present but had no
// recorded change at or before `block`.
func AsOfGrouped(data []byte, subKeyLen int, subKey []byte, block uint64) ([]byte, bool, error) {
	if len(data) == 0 || len(subKey) != subKeyLen {
		return nil, false, nil
	}
	count, n := binary.Uvarint(data)
	if n <= 0 {
		return nil, false, fmt.Errorf("grouped: bad count varint")
	}
	pos := n
	for i := uint64(0); i < count; i++ {
		if pos+subKeyLen > len(data) {
			return nil, false, fmt.Errorf("grouped: truncated subKey at entry %d", i)
		}
		curKey := data[pos : pos+subKeyLen]
		pos += subKeyLen
		blobLen, n := binary.Uvarint(data[pos:])
		if n <= 0 {
			return nil, false, fmt.Errorf("grouped: bad blobLen at entry %d", i)
		}
		pos += n
		if pos+int(blobLen) > len(data) {
			return nil, false, fmt.Errorf("grouped: truncated history blob at entry %d", i)
		}
		cmp := compareKey(curKey, subKey)
		if cmp == 0 {
			return AsOf(data[pos:pos+int(blobLen)], block)
		}
		pos += int(blobLen)
		if cmp > 0 {
			// Sorted; past our subKey → not present.
			return nil, false, nil
		}
	}
	return nil, false, nil
}
