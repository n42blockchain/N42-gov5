package mptproof

import (
	"encoding/binary"
	"fmt"
	"math/bits"

	"github.com/n42blockchain/N42/internal/mpttrie"
)

// DecodeRethBranchNodeCompact decodes a row from reth's AccountsTrie
// or StoragesTrie MDBX table.
//
// Format (from reth_codecs::Compact for alloy_trie::BranchNodeCompact,
// in C:/Users/10200/.cargo/.../reth-codecs-0.3.1/src/alloy/trie.rs):
//
//	[state_mask:u16 LE][tree_mask:u16 LE][hash_mask:u16 LE]
//	[optional root_hash:32 B]
//	[N × 32 B child hashes, ordered by popcount(hash_mask) bit position]
//
// root_hash is present iff the payload past the 6-byte mask header
// contains EXACTLY popcount(hash_mask)+1 hashes. Otherwise just N
// hashes follow.
//
// The shape mirrors N42's own `mpttrie.DecodeBranch` (which uses
// BIG-endian masks for the MarshalTrieNode format). We map straight
// onto the existing `BranchNode` struct so downstream code does not
// need to distinguish the two formats.
func DecodeRethBranchNodeCompact(raw []byte) (*mpttrie.BranchNode, error) {
	if len(raw) < 6 {
		return nil, fmt.Errorf("reth branch too short: %d bytes", len(raw))
	}
	bn := &mpttrie.BranchNode{
		HasState: binary.LittleEndian.Uint16(raw[0:2]),
		HasTree:  binary.LittleEndian.Uint16(raw[2:4]),
		HasHash:  binary.LittleEndian.Uint16(raw[4:6]),
	}
	payload := raw[6:]
	expectedHashes := bits.OnesCount16(bn.HasHash)
	totalHashBytes := len(payload)
	if totalHashBytes%32 != 0 {
		return nil, fmt.Errorf("reth branch payload not multiple of 32: %d bytes", totalHashBytes)
	}
	hashCount := totalHashBytes / 32
	if hashCount == expectedHashes+1 {
		bn.HasRoot = true
		copy(bn.RootHash[:], payload[:32])
		payload = payload[32:]
	} else if hashCount != expectedHashes {
		return nil, fmt.Errorf("reth branch hash count mismatch: have %d, expect %d (or %d with rootHash)",
			hashCount, expectedHashes, expectedHashes+1)
	}
	bn.Hashes = make([][32]byte, expectedHashes)
	for i := 0; i < expectedHashes; i++ {
		copy(bn.Hashes[i][:], payload[i*32:(i+1)*32])
	}
	return bn, nil
}

// StoredNibbles in reth's tables are encoded as a 65-byte fixed array:
// 64 bytes of nibbles (one per byte, 0..15) + 1 byte length. Padding
// bytes past the length are unused.
//
// StoredNibblesSubKey in StoragesTrie uses the SAME format (per
// crates/trie/common/src/nibbles.rs's StoredNibblesSubKey).
//
// DecodeStoredNibbles strips the trailing length byte and returns the
// nibble slice limited to that length.
func DecodeStoredNibbles(raw []byte) []byte {
	if len(raw) == 0 {
		return nil
	}
	// Length byte is the LAST byte of the fixed 65-byte encoding;
	// the leading bytes are the nibble path padded with zeros.
	if len(raw) >= 65 {
		l := int(raw[64])
		if l > 64 {
			l = 64
		}
		out := make([]byte, l)
		copy(out, raw[:l])
		return out
	}
	// Shorter encodings (v2 packed?) fall back to copy-as-is.
	out := make([]byte, len(raw))
	copy(out, raw)
	return out
}

// EncodeStoredNibbles produces a 65-byte StoredNibbles key matching
// reth's expected layout for AccountsTrie / StoragesTrie SubKey
// lookups.
func EncodeStoredNibbles(nibbles []byte) []byte {
	if len(nibbles) > 64 {
		nibbles = nibbles[:64]
	}
	out := make([]byte, 65)
	copy(out, nibbles)
	out[64] = byte(len(nibbles))
	return out
}
