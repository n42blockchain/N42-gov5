// Package mptbuild builds a reth-compatible MPT (AccountsTrie /
// StoragesTrie) from a source key-value cursor stream. It targets
// MDBX as the persistence backend so the resulting trie supports
// per-block updates during catch-up sync and 12-second live sync,
// plus MVCC-based 256-block reorg-safe diff layers.
//
// Pipeline (3 passes):
//
//	1. Scan source cursor → derive (hashed_key, leaf_value) via
//	   Extractor → ETL.Collect (external sort by hashed nibble path).
//	2. ETL.Load sorted → drive HashBuilder via GenStructStep →
//	   HashCollector writes branches into a second ETL collector.
//	3. ETL.Load branches sorted by nibble path → AppendDup into
//	   target MDBX bucket (page fill ≈ 95% via sorted-insert path).
//
// The output MDBX layout matches reth conceptually but is N42-owned;
// we write standard BranchNodeCompact (state_mask, tree_mask,
// hash_mask, hashes, optional root_hash) via lib/trie.MarshalTrieNode.
package mptbuild

import (
	"golang.org/x/crypto/sha3"
)

// Extractor converts a raw (k, v) cursor pair into a hashed nibble
// path (with 0x10 terminator) and the leaf value bytes that go into
// the MPT.
//
// Implementations:
//
//	AccountExtractor   — accounts: keccak(k) → 32B → 64-nibble path. value = v as-is.
//	StorageExtractor   — storage:  reth PlainStorageState DupSort
//	                     k = addr20, v = slot32 || u256_value.
//	                     composite hash = keccak(addr) || keccak(slot)
//	                     → 64B → 128-nibble path. value = v[32:].
type Extractor interface {
	// Extract takes the cursor (k, v). nibbleBuf is a scratch slice
	// the implementation may grow; it returns the nibble path
	// (terminated with 0x10) and the leaf value.
	Extract(k, v []byte, nibbleBuf []byte) (nibbles []byte, value []byte, err error)
	// Name returns a short label for logging.
	Name() string
}

// AccountExtractor: source = PlainAccountState. k = addr (20 B).
type AccountExtractor struct {
	hasher hashState
}

func NewAccountExtractor() *AccountExtractor {
	return &AccountExtractor{hasher: newKeccak()}
}

func (a *AccountExtractor) Name() string { return "account" }

func (a *AccountExtractor) Extract(k, v []byte, nibbleBuf []byte) (nibbles []byte, value []byte, err error) {
	a.hasher.Reset()
	a.hasher.Write(k)
	hashed := a.hasher.Sum(nil) // 32 B
	nibbleBuf = nibbleBuf[:0]
	for _, b := range hashed {
		nibbleBuf = append(nibbleBuf, b>>4, b&0x0f)
	}
	nibbleBuf = append(nibbleBuf, 0x10) // leaf terminator
	return nibbleBuf, v, nil
}

// StorageExtractor: source = PlainStorageState DupSort.
//
//	k = addr (20 B)
//	v = slot (32 B) || u256_value (variable, can be empty for zero)
//
// composite key = keccak(addr) || keccak(slot)  (64 B → 128 nibbles).
// leaf value     = v[32:]  (the slot's U256 bytes).
type StorageExtractor struct {
	hashAddr hashState
	hashSlot hashState
}

func NewStorageExtractor() *StorageExtractor {
	return &StorageExtractor{hashAddr: newKeccak(), hashSlot: newKeccak()}
}

func (s *StorageExtractor) Name() string { return "storage" }

func (s *StorageExtractor) Extract(k, v []byte, nibbleBuf []byte) (nibbles []byte, value []byte, err error) {
	if len(v) < 32 {
		return nil, nil, errShortStorageValue
	}
	s.hashAddr.Reset()
	s.hashAddr.Write(k)
	hashedAddr := s.hashAddr.Sum(nil) // 32 B

	s.hashSlot.Reset()
	s.hashSlot.Write(v[:32])
	hashedSlot := s.hashSlot.Sum(nil) // 32 B

	nibbleBuf = nibbleBuf[:0]
	for _, b := range hashedAddr {
		nibbleBuf = append(nibbleBuf, b>>4, b&0x0f)
	}
	for _, b := range hashedSlot {
		nibbleBuf = append(nibbleBuf, b>>4, b&0x0f)
	}
	nibbleBuf = append(nibbleBuf, 0x10)
	return nibbleBuf, v[32:], nil
}

// hashState is the small set of keccak operations we need; abstracted
// so tests can inject a mock hasher if useful.
type hashState interface {
	Reset()
	Write([]byte) (int, error)
	Sum([]byte) []byte
}

func newKeccak() hashState {
	return sha3.NewLegacyKeccak256().(hashState)
}
