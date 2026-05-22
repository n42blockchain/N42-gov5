package mptproof

import (
	"bytes"
	"context"
	"fmt"

	"github.com/n42blockchain/N42/internal/mpttrie"
)

// BuildRethStorageProof assembles an EIP-1186-shaped proof for a
// single storage slot, against reth's per-contract StoragesTrie.
//
// Returns the proof bytes (one entry per MPT node, root-first) plus
// the recorded storage-trie root for that contract (read from the
// implicit depth-0 branch composed of the 16 depth-1 rows under
// `addrHash`).
//
// Pre-condition: `walk` was produced by WalkRethStorage with the
// same (addrHash, slotHashedKey) pair. `slotValue` is the raw
// BE-stripped U256 bytes from PlainStorageState; `slotHashedKey`
// is keccak(slot) (32 B).
func BuildRethStorageProof(
	r *RethTrieReader,
	addrHash []byte,
	slotHashedKey []byte,
	slotValue []byte,
	walk *mpttrie.WalkResult,
) (proof [][]byte, storageRoot [32]byte, err error) {

	if len(addrHash) != 32 {
		return nil, [32]byte{}, fmt.Errorf("BuildRethStorageProof: addrHash 32B required")
	}
	if walk == nil || len(walk.Hops) == 0 {
		return nil, [32]byte{}, fmt.Errorf("BuildRethStorageProof: empty walk")
	}

	slotNibbles := nibblesOf(slotHashedKey)

	// 1) Implicit depth-0 root. Reth doesn't persist the empty-
	//    prefix branch; it's composed from the 16 depth-1 children
	//    keyed at single-nibble prefixes.
	rootHashes := make([][32]byte, 16)
	var rootState uint16
	for nib := 0; nib < 16; nib++ {
		b, ok, gerr := r.StorageBranchAt(addrHash, []byte{byte(nib)})
		if gerr != nil {
			return nil, [32]byte{}, fmt.Errorf("read depth-1 nib %x: %w", nib, gerr)
		}
		if !ok {
			continue
		}
		// Hash that depth-1 branch's RLP — that's the child slot
		// the depth-0 root would carry.
		brRLP, eerr := encodeStandardBranchRLP(b)
		if eerr != nil {
			return nil, [32]byte{}, fmt.Errorf("encode depth-1 nib %x: %w", nib, eerr)
		}
		rootHashes[nib] = keccak256(brRLP)
		rootState |= uint16(1) << uint16(nib)
	}
	rootBranch := &mpttrie.BranchNode{
		HasState: rootState,
		HasTree:  rootState,
		HasHash:  rootState,
		Hashes:   make([][32]byte, 0, 16),
	}
	for nib := 0; nib < 16; nib++ {
		if rootState&(uint16(1)<<uint16(nib)) != 0 {
			rootBranch.Hashes = append(rootBranch.Hashes, rootHashes[nib])
		}
	}
	rootRLP, err := encodeStandardBranchRLP(rootBranch)
	if err != nil {
		return nil, [32]byte{}, fmt.Errorf("encode root: %w", err)
	}
	storageRoot = keccak256(rootRLP)

	proof = make([][]byte, 0, len(walk.Hops)+2)
	proof = append(proof, rootRLP)

	// 2) Each walk hop's branch RLP, in order. Inline siblings
	//    (state set, hash NOT set) are reconstructed from the
	//    plain-state source via the StorageReader callback.
	storReader := NewRethBackedReader(r.src)
	for hi, hop := range walk.Hops {
		brRLP, eerr := encodeRethBranchRLPInlineAware(
			hop.Branch, hop.PrefixDepth, slotNibbles, slotValue, addrHash,
			storReader, r)
		if eerr != nil {
			return nil, [32]byte{}, fmt.Errorf("encode hop %d (depth %d): %w",
				hi, hop.PrefixDepth, eerr)
		}
		proof = append(proof, brRLP)
	}

	// 3) Leaf node — only when the walk's deepest hop's target slot
	//    references a leaf via a 32B HASH (hash_mask bit set). When
	//    hash_mask bit is clear the leaf is INLINE inside the
	//    deepest branch's RLP and no separate node is emitted.
	//
	// Ethereum standard storage-trie leaf: value field is the
	// RLP-encoded BE-stripped U256 (one level of RLP). The outer
	// leaf list wraps this RLP byte string AGAIN — net double-RLP.
	if walk.Outcome == mpttrie.LandedOnLeaf && walk.LeafDepth > 0 {
		deepest := walk.Hops[len(walk.Hops)-1]
		if deepest.Branch.HasHash&(uint16(1)<<deepest.TargetNibble) != 0 {
			remainder := append([]byte{}, slotNibbles[walk.LeafDepth:]...)
			remainder = append(remainder, 0x10)
			innerRLP := encodeBytes(slotValue)
			proof = append(proof, encodeLeafRLP(remainder, innerRLP))
		}
	}

	return proof, storageRoot, nil
}

// encodeRethBranchRLPInlineAware: like encodeStandardBranchRLP but
// reconstructs inline child RLPs (state set, hash clear) by reading
// the actual storage leaf at that nibble path from reth's
// PlainStorageState. Each such slot becomes an inline leaf node
// (< 32 B) directly embedded into the branch's RLP — matching the
// standard EIP-1186 wire format.
func encodeRethBranchRLPInlineAware(
	b *mpttrie.BranchNode,
	prefixDepth int,
	targetNibbles []byte,
	targetValue []byte,
	addrHash []byte,
	storReader *RethBackedReader,
	r *RethTrieReader,
) ([]byte, error) {
	_ = r
	slots := [17][]byte{}
	emptyEnc := []byte{0x80}
	for i := byte(0); i < 16; i++ {
		if !b.HasChild(i) {
			slots[i] = emptyEnc
			continue
		}
		if b.HasHash&(uint16(1)<<i) != 0 {
			// Stored hash reference.
			h, _ := b.ChildHash(i)
			buf := make([]byte, 33)
			buf[0] = 0xa0
			copy(buf[1:], h[:])
			slots[i] = buf
			continue
		}
		// State set, hash NOT set → inline. The child is a leaf at
		// nibble path [prefixDepth..0..i..i..end]. For our case
		// (target found via LandedOnLeaf), branches on the proof
		// path are at most one nibble above a leaf — so the inline
		// child is itself a leaf whose value lives in
		// PlainStorageState. Build the slot prefix and query.
		childPath := append([]byte{}, targetNibbles[:prefixDepth]...)
		childPath = append(childPath, i)

		// If this is exactly the TARGET nibble at this depth, we
		// already have the value (no PlainStorageState lookup).
		if prefixDepth < len(targetNibbles) && i == targetNibbles[prefixDepth] {
			remainder := append([]byte{}, targetNibbles[prefixDepth+1:]...)
			remainder = append(remainder, 0x10)
			slots[i] = encodeLeafRLP(remainder, targetValue)
			continue
		}

		// Sibling inline — enumerate USDC's storage under childPath
		// to find the unique slot. childPath length = prefixDepth+1
		// nibbles of the composite slot-hash. The full slot-hash
		// is 64 nibbles; if there's exactly one match below
		// childPath, it's the inline leaf.
		val, slotKey, ok, lerr := findUniqueStorageBelowPrefix(
			storReader, addrHash, childPath)
		if lerr != nil {
			return nil, fmt.Errorf("inline sibling resolve at nib %x: %w", i, lerr)
		}
		if !ok {
			return nil, fmt.Errorf("inline sibling at nib %x: no unique leaf below prefix %x",
				i, childPath)
		}
		remainder := append([]byte{}, slotKey[prefixDepth+1:]...)
		remainder = append(remainder, 0x10)
		slots[i] = encodeLeafRLP(remainder, val)
	}
	slots[16] = emptyEnc

	var payload bytes.Buffer
	for _, s := range slots {
		payload.Write(s)
	}
	return encodeList(payload.Bytes()), nil
}

// findUniqueStorageBelowPrefix scans reth's HashedStorages for a
// contract and returns the single storage slot whose nibblized
// hashed-slot key starts with `prefixNibbles`. Returns (value,
// fullSlotHashNibbles64, true, nil) on exactly one match; (nil,
// nil, false, nil) on zero matches; error on multiple matches.
func findUniqueStorageBelowPrefix(
	storReader *RethBackedReader,
	addrHash []byte,
	prefixNibbles []byte,
) ([]byte, []byte, bool, error) {
	tx, err := storReader.src.db.BeginRo(context.Background())
	if err != nil {
		return nil, nil, false, err
	}
	defer tx.Rollback()
	c, err := tx.CursorDupSort(rethHashedStoragesTable)
	if err != nil {
		return nil, nil, false, err
	}
	defer c.Close()

	// Build a byte-aligned lower bound: pack pairs of nibbles into
	// bytes; if odd length, pad the trailing nibble with 0.
	seek := make([]byte, (len(prefixNibbles)+1)/2)
	for i, n := range prefixNibbles {
		if i%2 == 0 {
			seek[i/2] |= n << 4
		} else {
			seek[i/2] |= n & 0x0f
		}
	}

	v, err := c.SeekBothRange(addrHash, seek)
	if err != nil {
		return nil, nil, false, err
	}
	var match []byte
	var matchSlotNibbles []byte
	for ; v != nil; _, v, err = c.NextDup() {
		if err != nil {
			return nil, nil, false, err
		}
		if len(v) < 32 {
			continue
		}
		slotHash := v[:32]
		slotNib := nibblesOf(slotHash)
		if !hasNibblePrefix(slotNib, prefixNibbles) {
			// SeekBothRange landed past the prefix range.
			if compareNibbles(slotNib, prefixNibbles) > 0 {
				break
			}
			continue
		}
		if match != nil {
			return nil, nil, false, fmt.Errorf("findUniqueStorageBelowPrefix: multiple matches for prefix %x (expected unique inline leaf)",
				prefixNibbles)
		}
		match = make([]byte, len(v)-32)
		copy(match, v[32:])
		matchSlotNibbles = slotNib
	}
	if match == nil {
		return nil, nil, false, nil
	}
	return match, matchSlotNibbles, true, nil
}

// stripLeadingZeros returns b with leading 0x00 bytes removed. For
// U256 BE byte arrays this canonicalises to the minimal-length BE
// representation (Ethereum's storage-trie value convention).
func stripLeadingZeros(b []byte) []byte {
	i := 0
	for i < len(b) && b[i] == 0 {
		i++
	}
	return b[i:]
}

// hasNibblePrefix returns true iff `nib` starts with `prefix`.
func hasNibblePrefix(nib, prefix []byte) bool {
	if len(nib) < len(prefix) {
		return false
	}
	for i, p := range prefix {
		if nib[i] != p {
			return false
		}
	}
	return true
}

// compareNibbles: byte-lexicographic compare of nibble paths.
func compareNibbles(a, b []byte) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	if len(a) < len(b) {
		return -1
	}
	if len(a) > len(b) {
		return 1
	}
	return 0
}

// VerifyProofChain checks that keccak(proof[i+1]) appears inside
// proof[i] as either a 32-byte hash reference (0xa0||hash) or as
// inline RLP (when child RLP is < 32 bytes). Returns the first
// mismatch index, or -1 on success.
func VerifyProofChain(proof [][]byte) int {
	for i := 0; i+1 < len(proof); i++ {
		curHash := keccak256(proof[i+1])
		var marker [33]byte
		marker[0] = 0xa0
		copy(marker[1:], curHash[:])
		if bytes.Contains(proof[i], marker[:]) {
			continue
		}
		if bytes.Contains(proof[i], proof[i+1]) {
			continue
		}
		return i
	}
	return -1
}

