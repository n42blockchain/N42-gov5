// Package coldstore implements the RFC-v2 cold-tier static archive for
// world state (RFC: docs/ethel/state-storage-tiered-design.md).
//
// File set per domain per step:
//
//	<step>.<domain>.kv     compressed value pages (zstd, 64 entries/page)
//	<step>.<domain>.idx    sparse byte-offset index (per-page first-key)
//
// v1 keeps the design intentionally minimal:
//   - keys sorted (MDBX cursor.First/Next already yields sorted order)
//   - per-page entries = 64
//   - page format = zstd(concat of varint-len-prefixed [key][value])
//   - sparse index format = (firstKey:keyLen B || pageByteOff:8B) repeated
//   - lookup = binary search index → decompress page → linear scan
//
// No MPHF, no Bloom/cuckoo filter, no value dictionary, no inline
// commitment trie. Those land in v2 once the v1 reader+verifier are
// proven. Trade-off: ~1 ms per cold lookup (binary search + page
// decompress) instead of ~10 µs (MPHF + offset). For tier-0 cold data
// — by definition the rarely-queried bottom of the stack — that's the
// right operating point.
//
// Compression scheme C (value tagged 4-class) is applied inside each
// page BEFORE zstd:
//
//	tag bit 0-1:  size class
//	  0: 1B data        (followed by 1 byte)
//	  1: 2..15B data    (low nibble = length-1)
//	  2: 16..32B data   (low nibble = length-16)
//	  3: reserved (future dict)
//	tag bit 4-7:  reserved / class-specific
package coldstore

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// Magic markers at the start of each file.
const (
	KVMagic  uint32 = 0x4B564B58 // "KVKX"
	IdxMagic uint32 = 0x49445858 // "IDXX"
	Version  uint32 = 1
)

// FileHeader is the 16-byte header shared by .kv and .idx files.
type FileHeader struct {
	Magic    uint32
	Version  uint32
	KeyLen   uint16 // fixed key length (e.g. 52 for storage)
	PageSize uint16 // entries per page (e.g. 64)
	Reserved uint32 // = 0
}

const HeaderSize = 16

func (h FileHeader) Marshal() []byte {
	b := make([]byte, HeaderSize)
	binary.LittleEndian.PutUint32(b[0:4], h.Magic)
	binary.LittleEndian.PutUint32(b[4:8], h.Version)
	binary.LittleEndian.PutUint16(b[8:10], h.KeyLen)
	binary.LittleEndian.PutUint16(b[10:12], h.PageSize)
	binary.LittleEndian.PutUint32(b[12:16], h.Reserved)
	return b
}

func UnmarshalHeader(b []byte) (FileHeader, error) {
	if len(b) < HeaderSize {
		return FileHeader{}, errors.New("coldstore: header truncated")
	}
	return FileHeader{
		Magic:    binary.LittleEndian.Uint32(b[0:4]),
		Version:  binary.LittleEndian.Uint32(b[4:8]),
		KeyLen:   binary.LittleEndian.Uint16(b[8:10]),
		PageSize: binary.LittleEndian.Uint16(b[10:12]),
		Reserved: binary.LittleEndian.Uint32(b[12:16]),
	}, nil
}

// ---------- Scheme-C value encoding ----------
//
// Bits 6-7 of the tag byte select the size class. The remaining bits
// pack the length:
//
//	class 0  (tag & 0xC0 == 0x00):  L=1, data is the tag itself? no — emit 1B tag + 1B data
//	class 1  (tag & 0xC0 == 0x40):  L=1..14, low 4 bits = L (1..14, 15=>extended)
//	class 2  (tag & 0xC0 == 0x80):  L=16..32, low 5 bits = L-16 (0..16, 16=>32)
//	class 3  (tag & 0xC0 == 0xC0):  reserved (future dict-id)
//
// Empty values (len 0) are encoded as a single tag byte 0x00 — caller
// is expected NOT to store explicit zeros (those are pruned) so an
// 0-byte value rarely shows up. We support it for safety.

// EncodeValue appends the scheme-C representation of v to dst and returns dst.
func EncodeValue(dst, v []byte) []byte {
	L := len(v)
	switch {
	case L == 0:
		return append(dst, 0x00)
	case L == 1:
		// class 0: tag = 0x01 (signals "1 byte follows"); data = v[0]
		return append(append(dst, 0x01), v[0])
	case L <= 14:
		// class 1: tag = 0x40 | L  (L in [2..14])
		return append(append(dst, byte(0x40|L)), v...)
	case L == 15:
		// edge: 15 doesn't fit cleanly; emit tag=0x4F + 15B data
		return append(append(dst, 0x4F), v...)
	case L <= 32:
		// class 2: tag = 0x80 | (L-16)
		return append(append(dst, byte(0x80|(L-16))), v...)
	default:
		// over 32: shouldn't happen for storage (uint256 capped at 32).
		// Caller should reject; we encode as full 32B truncated for safety.
		return append(append(dst, 0x80|16), v[:32]...)
	}
}

// DecodeValue decodes one scheme-C value at data[pos]. Returns (value, newPos).
func DecodeValue(data []byte, pos int) ([]byte, int, error) {
	if pos >= len(data) {
		return nil, pos, fmt.Errorf("coldstore: truncated tag")
	}
	tag := data[pos]
	pos++
	switch {
	case tag == 0x00:
		return nil, pos, nil
	case tag == 0x01:
		if pos >= len(data) {
			return nil, pos, fmt.Errorf("coldstore: truncated 1B value")
		}
		return data[pos : pos+1 : pos+1], pos + 1, nil
	case tag&0xC0 == 0x40:
		L := int(tag & 0x0F)
		if L == 0 || L > 15 {
			return nil, pos, fmt.Errorf("coldstore: invalid class-1 len %d", L)
		}
		if pos+L > len(data) {
			return nil, pos, fmt.Errorf("coldstore: truncated class-1 value (need %d, have %d)", L, len(data)-pos)
		}
		return data[pos : pos+L : pos+L], pos + L, nil
	case tag&0xC0 == 0x80:
		L := int(tag&0x1F) + 16
		if L > 32 {
			return nil, pos, fmt.Errorf("coldstore: invalid class-2 len %d", L)
		}
		if pos+L > len(data) {
			return nil, pos, fmt.Errorf("coldstore: truncated class-2 value (need %d, have %d)", L, len(data)-pos)
		}
		return data[pos : pos+L : pos+L], pos + L, nil
	default:
		return nil, pos, fmt.Errorf("coldstore: unknown tag 0x%02x", tag)
	}
}
