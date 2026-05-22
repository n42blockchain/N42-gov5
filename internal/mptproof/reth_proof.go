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
	//    prefix branch for the dense case; it's composed from the
	//    16 depth-1 children keyed at single-nibble prefixes.
	//
	// Sparse-trie note: if reth instead stores an extension/leaf at
	// the empty prefix (small storage tries with a single common
	// prefix), the implicit composition is WRONG. Probe first and
	// fail loud so we know to add the sparse path. USDC and most
	// production contracts hit the dense path.
	if root, exists, perr := r.StorageBranchAt(addrHash, nil); perr == nil && exists {
		return nil, [32]byte{}, fmt.Errorf("sparse-trie path not implemented: reth stores a depth-0 row at empty prefix (state=0x%04x); implicit-root reconstruction would be wrong",
			root.HasState)
	}
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

	// 3) Sub-trie / leaf reconstruction at the deepest hop's slot.
	//
	// Reth doesn't persist sub-tries whose encoded RLP > 32 B as
	// their own row when they are reachable via a hash reference
	// from a parent branch — instead the parent stores the keccak
	// directly and the sub-trie's leaves come from HashedStorages.
	// For multi-leaf prefixes (most production cases) we have to
	// enumerate the matching leaves and rebuild the sub-trie path
	// using N42's existing expandSubtreeProofPath logic.
	if walk.Outcome == mpttrie.LandedOnLeaf && walk.LeafDepth > 0 {
		deepest := walk.Hops[len(walk.Hops)-1]
		if deepest.Branch.HasHash&(uint16(1)<<deepest.TargetNibble) != 0 {
			subLeaves, lerr := enumerateUSDCStorageSubLeaves(
				r.src, addrHash, slotNibbles[:walk.LeafDepth])
			if lerr != nil {
				return nil, [32]byte{}, fmt.Errorf("enumerate sub-leaves: %w", lerr)
			}
			if len(subLeaves) == 0 {
				return nil, [32]byte{}, fmt.Errorf("no leaves under prefix %x — proof structurally impossible",
					slotNibbles[:walk.LeafDepth])
			}
			keyTail := append([]byte{}, slotNibbles[walk.LeafDepth:]...)
			keyTail = append(keyTail, 0x10)
			expanded, eerr := expandSubtreeProofPath(subLeaves, keyTail)
			if eerr != nil {
				return nil, [32]byte{}, fmt.Errorf("expandSubtreeProofPath: %w", eerr)
			}
			proof = append(proof, expanded...)
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
//
// NOTE (RB-4 limitation): when called from encodeRethBranchRLPInlineAware
// for inline-sibling reconstruction, MULTIPLE matches are possible
// in reality — an inline sibling can itself be a multi-leaf subtree
// (< 32 B encoded). The current path errors on that case. The
// expandSubtreeProofPath logic in BuildRethStorageProof's tail handles
// that for the TARGET slot; the encodeRethBranchRLPInlineAware path
// for sibling inlines still uses this stricter single-leaf finder.
// USDC's tested slots happen to have unique inline siblings; deeper
// or different contracts may need the same expand-then-embed logic
// extended to siblings.
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

// enumerateUSDCStorageSubLeaves returns all reth HashedStorages
// slots for the given account whose hashed-slot nibble path starts
// with `slotPrefix`. Each subLeaf's effectiveKey is the remaining
// nibbles (relative to slotPrefix) plus a 0x10 terminator —
// exactly the shape expandSubtreeProofPath consumes.
//
// addrHash: 32-byte keccak(addr) (used as DupSort key).
// slotPrefix: 0..64 nibbles, each byte 0..15 (one nibble per byte).
func enumerateUSDCStorageSubLeaves(src *RethHashedLeafSource, addrHash, slotPrefix []byte) ([]subLeaf, error) {
	tx, err := src.db.BeginRo(context.Background())
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	c, err := tx.CursorDupSort(rethHashedStoragesTable)
	if err != nil {
		return nil, err
	}
	defer c.Close()

	// Build byte-aligned lower bound for SeekBothRange.
	seek := make([]byte, (len(slotPrefix)+1)/2)
	for i, n := range slotPrefix {
		if i%2 == 0 {
			seek[i/2] |= n << 4
		} else {
			seek[i/2] |= n & 0x0f
		}
	}

	v, err := c.SeekBothRange(addrHash, seek)
	if err != nil {
		return nil, err
	}
	var out []subLeaf
	for ; v != nil; _, v, err = c.NextDup() {
		if err != nil {
			return nil, err
		}
		if len(v) < 32 {
			continue
		}
		slotHash := v[:32]
		slotNib := nibblesOf(slotHash)
		if !hasNibblePrefix(slotNib, slotPrefix) {
			if compareNibbles(slotNib, slotPrefix) > 0 {
				break
			}
			continue
		}
		val := make([]byte, len(v)-32)
		copy(val, v[32:])
		// Ethereum storage-trie leaf convention: value field is the
		// RLP-encoded BE-stripped U256. The leaf node's outer list
		// wraps this byte-string AGAIN via encodeBytes. Net effect
		// = double-RLP. We pre-encode here so the existing
		// expandSubtreeProofPath / encodeLeafRLP code (which calls
		// encodeBytes once on `value`) produces the correct final
		// double-wrap.
		val = encodeBytes(stripLeadingZeros(val))
		// effectiveKey = remaining nibbles + 0x10 terminator.
		eff := make([]byte, len(slotNib)-len(slotPrefix)+1)
		copy(eff, slotNib[len(slotPrefix):])
		eff[len(eff)-1] = 0x10
		out = append(out, subLeaf{effectiveKey: eff, value: val})
	}
	return out, nil
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
//
// LIMITATION: this is a STRUCTURAL link-presence check, not a
// cryptographic proof verifier. It does NOT validate that proof[i+1]
// sits at the CORRECT slot (the target nibble's slot) within
// proof[i]'s branch RLP — an adversary could craft proof[i] with
// the child hash at the wrong slot and still pass this check. For
// EXTERNAL verification (proofs received from a third party), use
// the EIP-1186 full-decode verifier at mptproof.VerifyStandardProof.
// VerifyProofChain is intended only as an internal sanity check on
// proofs we built ourselves.
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

