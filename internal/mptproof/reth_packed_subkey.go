package mptproof

import "fmt"

// PackNibblesV2 produces the reth v2 PackedStoredNibblesSubKey
// encoding: 32 bytes packed nibbles (2 nibbles per byte, big-endian
// within the byte) + 1 byte length. Total 33 bytes fixed.
//
// For nibble paths shorter than 64 nibbles, trailing nibbles are
// zero-padded. Memcmp ordering on the 33-byte buffer matches
// nibble-lexicographic ordering, since the length byte sits AFTER
// the data (so a longer prefix-match sorts after a shorter one
// only when an actual nibble differs).
//
// Saving vs reth v1 (65-byte): 32 bytes per row.
// On D:\reth2k StoragesTrie (138.5 M rows): 32 × 138.5 M ≈ 4.4 GB.
func PackNibblesV2(nibbles []byte) ([]byte, error) {
	if len(nibbles) > 64 {
		return nil, fmt.Errorf("PackNibblesV2: %d nibbles > 64 (max)", len(nibbles))
	}
	out := make([]byte, 33)
	for i, n := range nibbles {
		if n > 15 {
			return nil, fmt.Errorf("PackNibblesV2: nibble[%d]=%d > 15", i, n)
		}
		if i%2 == 0 {
			out[i/2] |= n << 4
		} else {
			out[i/2] |= n & 0x0f
		}
	}
	out[32] = byte(len(nibbles))
	return out, nil
}

// UnpackNibblesV2 reverses PackNibblesV2: returns the original
// nibble path (length determined by the trailing length byte). The
// 33-byte input is the fixed-size packed format.
func UnpackNibblesV2(packed []byte) ([]byte, error) {
	if len(packed) != 33 {
		return nil, fmt.Errorf("UnpackNibblesV2: input must be 33 bytes, got %d", len(packed))
	}
	n := int(packed[32])
	if n > 64 {
		return nil, fmt.Errorf("UnpackNibblesV2: length byte %d > 64", n)
	}
	out := make([]byte, n)
	for i := 0; i < n; i++ {
		if i%2 == 0 {
			out[i] = (packed[i/2] >> 4) & 0x0f
		} else {
			out[i] = packed[i/2] & 0x0f
		}
	}
	return out, nil
}

// PackedSize returns the byte size of the packed form for a given
// nibble count. Always 33 (the reth v2 convention is fixed-size
// for DupSort comparator compatibility).
//
// For the variable-length variant (future RB-5d), use VariableSize
// which returns ceil(N/2) + 1.
func PackedSize(nibbleCount int) int {
	return 33
}

// VariableSize returns the byte size of an idealised variable-length
// packed subkey for a given nibble count: ceil(N/2) bytes of packed
// nibbles + 1 byte length. Used by size projections; actual variable
// encoding requires a custom DupSort comparator (RB-5d).
func VariableSize(nibbleCount int) int {
	return (nibbleCount+1)/2 + 1
}
