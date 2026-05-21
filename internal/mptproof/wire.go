package mptproof

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/n42blockchain/N42/internal/mpttrie"
)

// ProofBytes is the eth_getProof-style proof byte array: each element
// is one RLP-encoded standard MPT node, ordered root → leaf. The
// caller verifies by:
//
//	keccak(proof[0]) == stateRoot
//	for each i: descend from proof[i] using key nibbles to get the
//	            expected hash → must equal keccak(proof[i+1])
//	last element either IS the leaf node OR contains the leaf inline.
//
// See EIP-1186 "eth_getProof" for the canonical format consumed by
// light clients and cross-chain bridges.
type ProofBytes [][]byte

// AccountProofBytes returns the eth_getProof "accountProof" array
// reconstructed from the walk's BranchNodes and the leaf value.
func (p *AccountProof) ProofBytes() (ProofBytes, error) {
	return proofBytesFromWalk(p.HashedAddr[:], p.LeafValue, p.LeafFound, p.Walk)
}

// StorageProofBytes returns the "proof" array for one storage slot.
func (p *StorageProof) ProofBytes() (ProofBytes, error) {
	return proofBytesFromWalk(p.HashedKey[:], p.LeafValue, p.LeafFound, p.Walk)
}

// proofBytesFromWalk emits one standard MPT node per walk hop +
// optionally a leaf node at the end.
//
// LIMITATION: if any sibling on the path is encoded INLINE in its
// parent (state_mask bit set but hash_mask bit clear), we can't
// reconstruct the parent's standard branch RLP — returns
// ErrInlineSiblingInPath. For deep production tries this is rare
// (siblings of large subtrees are always hashed); for shallow
// synthetic tries it can happen.
func proofBytesFromWalk(hashedKey, leafValue []byte, leafFound bool, walk *mpttrie.WalkResult) (ProofBytes, error) {
	if walk == nil {
		return nil, errors.New("mptproof: nil walk")
	}

	// Single-leaf trie special case: the ENTIRE trie is just the leaf
	// node, no branches were persisted. Walk tries to read the root
	// branch and finds nothing → outcome NoBranchAtPath, 0 hops.
	// The proof is simply [leafRLP] (whose keccak equals stateRoot).
	if leafFound && len(walk.Hops) == 0 && walk.Outcome == mpttrie.NoBranchAtPath {
		remainder := nibblesOf(hashedKey)
		return ProofBytes{encodeLeafRLP(remainder, leafValue)}, nil
	}
	if len(walk.Hops) == 0 {
		return nil, fmt.Errorf("mptproof: empty walk (outcome=%v)", walk.Outcome)
	}

	out := make(ProofBytes, 0, len(walk.Hops)+1)
	for i, hop := range walk.Hops {
		branchRLP, err := encodeStandardBranchRLP(hop.Branch)
		if err != nil {
			return nil, fmt.Errorf("hop %d (depth %d): %w", i, hop.PrefixDepth, err)
		}
		out = append(out, branchRLP)
	}

	// Append the leaf node RLP when the walk lands on a leaf AND that
	// leaf's hash is genuinely stored (not inline) at the deepest
	// hop's TargetNibble. For ExtensionInPath cases — the cheap walk
	// doesn't know the actual leaf depth — the caller can use the
	// D.2 subtree-rebuild path to assemble the full proof; for direct
	// LandedOnLeaf, the encoding works.
	if leafFound && walk.Outcome == mpttrie.LandedOnLeaf {
		remainder := nibblesOf(hashedKey)[walk.LeafDepth:]
		leafRLP := encodeLeafRLP(remainder, leafValue)
		out = append(out, leafRLP)
	}

	return out, nil
}

// ErrInlineSiblingInPath is returned when a SIBLING on the path is
// encoded inline in its parent's standard branch RLP. We don't store
// inline bytes in BranchNodeCompact, so reconstruction would produce
// an INVALID branch (a sibling slot containing a 32-byte hash where
// the standard format expected the inline RLP bytes). Phase D.x will
// resolve this by either (a) extending BranchNodeCompact to carry
// inline data, or (b) reconstructing from plain state on demand.
var ErrInlineSiblingInPath = errors.New("mptproof: sibling on proof path is inline — cannot reconstruct standard branch RLP")

// encodeStandardBranchRLP turns a BranchNodeCompact into the
// canonical 17-element-list RLP that goes in eth_getProof bytes.
//
//	RLP([c0, c1, ..., c15, value])
//
// Each ci is:
//   - empty (RLP 0x80) when state_mask bit i clear
//   - 32-byte hash wrapped (0xa0 || hash) when hash_mask bit i set
//   - inline bytes when state set but hash_mask clear — UNRECOVERABLE
//     from compact form. Returns ErrInlineSiblingInPath.
//
// Value (slot 16) is always empty for account/storage branches.
func encodeStandardBranchRLP(b *mpttrie.BranchNode) ([]byte, error) {
	slots := [17][]byte{}
	emptyEnc := []byte{0x80}
	for i := byte(0); i < 16; i++ {
		if !b.HasChild(i) {
			slots[i] = emptyEnc
			continue
		}
		h, ok := b.ChildHash(i)
		if !ok {
			return nil, fmt.Errorf("nibble 0x%x state set but no stored hash: %w", i, ErrInlineSiblingInPath)
		}
		buf := make([]byte, 33)
		buf[0] = 0xa0
		copy(buf[1:], h[:])
		slots[i] = buf
	}
	slots[16] = emptyEnc

	var payload bytes.Buffer
	for _, s := range slots {
		payload.Write(s)
	}
	return encodeList(payload.Bytes()), nil
}

// encodeLeafRLP returns RLP([HP(remainder, leaf=true), value]) for a
// standard MPT leaf node.
func encodeLeafRLP(remainderNibbles, value []byte) []byte {
	terminated := remainderNibbles
	if len(terminated) == 0 || terminated[len(terminated)-1] != 0x10 {
		terminated = append(append([]byte{}, remainderNibbles...), 0x10)
	}
	hp := hexToCompact(terminated)
	payload := append(encodeBytes(hp), encodeBytes(value)...)
	return encodeList(payload)
}
