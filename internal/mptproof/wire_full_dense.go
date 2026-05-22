package mptproof

import (
	"fmt"

	"github.com/n42blockchain/N42/internal/mpttrie"
)

// fullProofBytesDense emits an EIP-1186 proof entirely from dense
// branch data — no sub-tree rebuild, no LeafSource calls during
// branch hops. Per-branch cost is one MDBX point lookup + slot
// concatenation. Total proof time for a USDC account or storage walk
// should be sub-millisecond once page cache is warm.
//
// Pre-conditions:
//   - dense.Has() returned true
//   - walk was computed against the compact AccountsTrie/StoragesTrie
//     in the same env (its Hops + PrefixDepth + TargetNibble describe
//     the same trie structure)
//
// Leaf emission:
//   - When the deepest hop's target slot is a 33-byte hash ref, the
//     leaf is a separate node — emit leafRLP(remainder, leafValue)
//     after the last branch.
//   - When the target slot is inline RLP < 32 bytes, that inline
//     IS the leaf's encoding; no extra emit needed.
//   - Currently no extension-in-path expansion (will add if needed
//     for sparse paths; mainnet's HPH produces compact branches that
//     normally don't insert extensions for found leaves).
func (g *Generator) fullProofBytesDense(hashedKey, leafValue []byte,
	leafFound bool, walk *mptwalk, dense *mpttrie.DenseReader) (ProofBytes, error) {

	keyNibbles := nibblesOf(hashedKey)

	// Single-leaf trie (root IS the leaf). Caller should never have a
	// non-empty walk in that case, but keep parity with the legacy
	// path's contract.
	if leafFound && len(walk.Hops) == 0 {
		return ProofBytes{encodeLeafRLP(keyNibbles, leafValue)}, nil
	}
	if len(walk.Hops) == 0 {
		return nil, fmt.Errorf("dense proof: empty walk")
	}

	out := make(ProofBytes, 0, len(walk.Hops)+1)
	for hi, hop := range walk.Hops {
		branchPath := keyNibbles[:hop.PrefixDepth]
		branch, ok, err := dense.Get(branchPath)
		if err != nil {
			return nil, fmt.Errorf("dense.Get(hop %d, path %x): %w", hi, branchPath, err)
		}
		if !ok {
			return nil, fmt.Errorf("dense missing at hop %d path %x", hi, branchPath)
		}
		rlp := encodeBranchRLPFromDense(branch)
		out = append(out, rlp)
	}

	if !leafFound {
		return out, nil
	}

	// Leaf emission. The deepest branch's target slot is the
	// reference to whatever sits at depth+1: a leaf RLP (inline or
	// hashed) or, in sparse paths, an extension+leaf chain.
	deepest := walk.Hops[len(walk.Hops)-1]
	deepBranch, ok, err := dense.Get(keyNibbles[:deepest.PrefixDepth])
	if err != nil || !ok {
		return nil, fmt.Errorf("dense.Get deepest: ok=%v err=%v", ok, err)
	}
	targetSlot := deepBranch.Slots[deepest.TargetNibble]
	if len(targetSlot) != 33 || targetSlot[0] != 0xa0 {
		// Inline leaf already encoded inside the last branch RLP.
		return out, nil
	}

	// Effective leaf depth = the nibble depth just past the deepest
	// branch's target slot. walk.LeafDepth is only populated when
	// Outcome == LandedOnLeaf; for NoBranchAtPath (slot's hash points
	// at an under-threshold subtree not persisted as its own branch),
	// LeafDepth stays 0. Derive from the deepest hop directly so we
	// don't accidentally key off the zero default and prefix-scan the
	// entire unified storage trie.
	effLeafDepth := deepest.PrefixDepth + 1

	// 33B hash ref. Try direct leaf at effLeafDepth: does keccak of
	// leafRLP(remainder, leafValue) match the stored hash? If yes,
	// emit a single leaf RLP. If no, fall back to the legacy
	// target-subtree-expansion path (handles intermediate extensions
	// when keccak space is sparse — rare in real MPTs but possible).
	remainder := keyNibbles[effLeafDepth:]
	directLeaf := encodeLeafRLP(remainder, leafValue)
	directHash := keccak256(directLeaf)
	var storedHash [32]byte
	copy(storedHash[:], targetSlot[1:])
	if directHash == storedHash {
		out = append(out, directLeaf)
		return out, nil
	}

	// Fall back: expand the subtree below `keyNibbles[:effLeafDepth]`
	// using the LeafSource (same algorithm as fullProofBytes). Costs
	// one prefix scan over LeafSource — acceptable here because this
	// path triggers only on sparse-keccak edge cases.
	if g.leaves == nil {
		return nil, fmt.Errorf("dense leaf at hop deepest needs expansion but Generator.leaves is nil")
	}
	prefix := keyNibbles[:effLeafDepth]
	var builder subtreeRootBuilder
	if isAccountWalk(hashedKey) {
		builder = accountSubtreeBuilder{src: g.leaves}
	} else {
		builder = storageSubtreeBuilder{src: g.leaves}
	}
	expandLeaves, err := builder.subLeavesByPrefix(prefix)
	if err != nil {
		return nil, fmt.Errorf("dense expand prefix %x: %w", prefix, err)
	}
	expanded, err := expandSubtreeProofPath(expandLeaves, keyNibbles[effLeafDepth:])
	if err != nil {
		return nil, fmt.Errorf("dense expand: %w", err)
	}
	out = append(out, expanded...)
	return out, nil
}

// isAccountWalk distinguishes 32-byte hashedAddr (account) from
// 64-byte composite (storage) hashed keys.
func isAccountWalk(hashedKey []byte) bool { return len(hashedKey) == 32 }

// encodeBranchRLPFromDense builds the standard MPT branch RLP from a
// dense branch's per-slot bytes. Each slot is either:
//   - 33 B 0xa0||hash (parent-facing hash reference) — pass through
//   - 1..32 B inline RLP — pass through
//   - nil (empty slot) — emit 0x80
//
// The 17th item (value slot, always empty for branches in our trie)
// is emitted as 0x80.
func encodeBranchRLPFromDense(b mpttrie.DenseBranch) []byte {
	payloadLen := 17 // start with 17 × 1 = empty slot bytes
	for i := 0; i < 16; i++ {
		if b.Slots[i] != nil {
			payloadLen += len(b.Slots[i]) - 1 // we counted 1 byte already
		}
	}
	var hdr []byte
	if payloadLen < 56 {
		hdr = []byte{0xc0 + byte(payloadLen)}
	} else {
		// Long-list prefix 0xf7 + sizeOfLength bytes + length bytes
		lb := encodeBigEndianLen(payloadLen)
		hdr = make([]byte, 1+len(lb))
		hdr[0] = 0xf7 + byte(len(lb))
		copy(hdr[1:], lb)
	}
	out := make([]byte, 0, len(hdr)+payloadLen)
	out = append(out, hdr...)
	for i := 0; i < 16; i++ {
		if b.Slots[i] == nil {
			out = append(out, 0x80)
		} else {
			out = append(out, b.Slots[i]...)
		}
	}
	out = append(out, 0x80) // value slot
	return out
}

func encodeBigEndianLen(n int) []byte {
	switch {
	case n <= 0xff:
		return []byte{byte(n)}
	case n <= 0xffff:
		return []byte{byte(n >> 8), byte(n)}
	case n <= 0xffffff:
		return []byte{byte(n >> 16), byte(n >> 8), byte(n)}
	default:
		return []byte{byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)}
	}
}

// mptwalk aliases mpttrie.WalkResult locally for shorter signatures.
type mptwalk = mpttrie.WalkResult

// fullProofBytesDenseV2 is the G2 plain-key-referencing variant of
// fullProofBytesDense. The dense V2 reader expands LeafMarker slots
// on-the-fly via base.AccountLeafByPrefix / StorageLeafByPrefix
// (each reconstruction is one keccak-keyed cursor seek + one
// encodeLeafRLP + one keccak — sub-µs per slot when reth's hashed
// tables are page-cached).
func (g *Generator) fullProofBytesDenseV2(hashedKey, leafValue []byte,
	leafFound bool, walk *mptwalk, dense *mpttrie.DenseReader,
	isStorage bool, base mpttrie.SingleLeafLookup) (ProofBytes, error) {

	keyNibbles := nibblesOf(hashedKey)

	if leafFound && len(walk.Hops) == 0 {
		return ProofBytes{encodeLeafRLP(keyNibbles, leafValue)}, nil
	}
	if len(walk.Hops) == 0 {
		return nil, fmt.Errorf("dense V2 proof: empty walk")
	}

	out := make(ProofBytes, 0, len(walk.Hops)+1)
	for hi, hop := range walk.Hops {
		branchPath := keyNibbles[:hop.PrefixDepth]
		branch, ok, err := dense.GetV2(branchPath, isStorage, base)
		if err != nil {
			return nil, fmt.Errorf("dense.GetV2(hop %d, path %x): %w", hi, branchPath, err)
		}
		if !ok {
			return nil, fmt.Errorf("dense V2 missing at hop %d path %x", hi, branchPath)
		}
		out = append(out, encodeBranchRLPFromDense(branch))
	}

	if !leafFound {
		return out, nil
	}

	// Leaf-tail handling — same logic as the V1 path. After GetV2,
	// the deepest branch's slots are FULLY expanded (markers became
	// 33B hashes), so the discrimination is identical.
	deepest := walk.Hops[len(walk.Hops)-1]
	deepBranch, ok, err := dense.GetV2(keyNibbles[:deepest.PrefixDepth], isStorage, base)
	if err != nil || !ok {
		return nil, fmt.Errorf("dense V2 deepest: ok=%v err=%v", ok, err)
	}
	targetSlot := deepBranch.Slots[deepest.TargetNibble]
	if len(targetSlot) != 33 || targetSlot[0] != 0xa0 {
		return out, nil // inline leaf already in last branch RLP
	}

	// Derive effLeafDepth from the deepest hop directly — same
	// reasoning as the V1 path (walk.LeafDepth is unreliable when
	// Outcome != LandedOnLeaf).
	effLeafDepth := deepest.PrefixDepth + 1

	remainder := keyNibbles[effLeafDepth:]
	directLeaf := encodeLeafRLP(remainder, leafValue)
	directHash := keccak256(directLeaf)
	var storedHash [32]byte
	copy(storedHash[:], targetSlot[1:])
	if directHash == storedHash {
		out = append(out, directLeaf)
		return out, nil
	}

	// Sparse-keccak extension fallback (same as V1 path).
	if g.leaves == nil {
		return nil, fmt.Errorf("dense V2 leaf at hop deepest needs expansion but Generator.leaves is nil")
	}
	prefix := keyNibbles[:effLeafDepth]
	var builder subtreeRootBuilder
	if isAccountWalk(hashedKey) {
		builder = accountSubtreeBuilder{src: g.leaves}
	} else {
		builder = storageSubtreeBuilder{src: g.leaves}
	}
	expandLeaves, err := builder.subLeavesByPrefix(prefix)
	if err != nil {
		return nil, fmt.Errorf("dense V2 expand prefix %x: %w", prefix, err)
	}
	expanded, err := expandSubtreeProofPath(expandLeaves, keyNibbles[effLeafDepth:])
	if err != nil {
		return nil, fmt.Errorf("dense V2 expand: %w", err)
	}
	out = append(out, expanded...)
	return out, nil
}
