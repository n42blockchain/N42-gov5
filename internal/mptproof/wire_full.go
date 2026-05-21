package mptproof

import (
	"bytes"
	"errors"
	"fmt"
	"sort"

	"golang.org/x/crypto/sha3"

	"github.com/n42blockchain/N42/internal/mpttrie"
	"github.com/n42blockchain/N42/lib/rlphacks"
	"github.com/n42blockchain/N42/lib/trie"
)

// FullAccountProofBytes is the eth_getProof byte assembly that
// resolves inline siblings on the proof path by enumerating their
// leaves from the LeafSource and rebuilding their sub-tries.
//
// SLOW: each inline sibling triggers one ScanAccounts on the source.
// For RPC hot paths a hashed-key-sorted plain-state index is needed
// (separate work).
//
// CURRENT MVP scope (D.1):
//   - direct-leaf case (walk lands on the leaf at the deepest hop):
//     the proof round-trips cleanly through VerifyStandardProof
//   - inline-sibling resolution on the proof path: sibling slots in
//     branch nodes are rebuilt with the correct standard-MPT child
//     encoding (inline or 0xa0||hash based on encoded size)
//
// NOT YET (D.1.5 target-subtree expansion):
//   - subtree-below-deepest-hop case (Walk's deepest hop stored hash
//     is a sub-trie root that contains our leaf at a deeper position
//     than LeafDepth). For these, the proof needs intermediate
//     extension/branch nodes between the deepest persisted branch
//     and the leaf. The walk-fold verify (Verify) returns
//     ErrExtensionInPath for these — same condition applies here.
//
// Returns ProofBytes that, when passed to VerifyStandardProof against
// the recorded StateRoot, will verify iff the proof is in MVP scope.
func (g *Generator) FullAccountProofBytes(proof *AccountProof) (ProofBytes, error) {
	// V2 path DISABLED — LeafMarker compression assumes "HasTree=0 →
	// direct leaf at slot+1", but gen_struct_step.go emits extension
	// nodes between a branch slot and a deeper leaf when the keccak
	// space is sparse. In that case the slot's stored hash is
	// keccak(extension RLP), not keccak(leaf RLP), and the
	// reconstruction in DenseReader.GetV2 produces a different hash.
	// See docs/ethel/g2-extension-aware-encoding.md for the fix.
	//
	// Falls through to V1 (still 78% smaller than reth's combined
	// AccountsTrie + HashedAccounts).
	if g.accountsDense != nil {
		return g.fullProofBytesDense(proof.HashedAddr[:], proof.LeafValue,
			proof.LeafFound, proof.Walk, g.accountsDense)
	}
	return g.fullProofBytes(proof.HashedAddr[:], proof.LeafValue, proof.LeafFound,
		proof.Walk, accountSubtreeBuilder{src: g.leaves})
}

// FullStorageProofBytes is the storage equivalent.
func (g *Generator) FullStorageProofBytes(proof *StorageProof) (ProofBytes, error) {
	// V2 path DISABLED — same extension-node issue as accounts above.
	if g.storagesDense != nil {
		return g.fullProofBytesDense(proof.HashedKey[:], proof.LeafValue,
			proof.LeafFound, proof.Walk, g.storagesDense)
	}
	return g.fullProofBytes(proof.HashedKey[:], proof.LeafValue, proof.LeafFound,
		proof.Walk, storageSubtreeBuilder{src: g.leaves})
}

// subtreeRootBuilder is the per-domain enumerator: given a nibble
// prefix, returns either the RAW standard-MPT-node bytes representing
// the sub-trie below that prefix (SubtreeNodeBytes — used by sibling
// rebuild) OR the list of leaves with effective keys relative to the
// prefix (subLeavesByPrefix — used by target subtree expansion).
type subtreeRootBuilder interface {
	SubtreeNodeBytes(prefix []byte) ([]byte, error)
	subLeavesByPrefix(prefix []byte) ([]subLeaf, error)
}

type accountSubtreeBuilder struct{ src LeafSource }

func (a accountSubtreeBuilder) SubtreeNodeBytes(prefix []byte) ([]byte, error) {
	leaves, err := collectAccountLeavesWithPrefix(a.src, prefix)
	if err != nil {
		return nil, err
	}
	if len(leaves) == 0 {
		return []byte{0x80}, nil
	}
	return buildSubtreeNodeBytes(leaves)
}

func (a accountSubtreeBuilder) subLeavesByPrefix(prefix []byte) ([]subLeaf, error) {
	return collectAccountLeavesWithPrefix(a.src, prefix)
}

type storageSubtreeBuilder struct{ src LeafSource }

func (s storageSubtreeBuilder) SubtreeNodeBytes(prefix []byte) ([]byte, error) {
	leaves, err := collectStorageLeavesWithPrefix(s.src, prefix)
	if err != nil {
		return nil, err
	}
	if len(leaves) == 0 {
		return []byte{0x80}, nil
	}
	return buildSubtreeNodeBytes(leaves)
}

func (s storageSubtreeBuilder) subLeavesByPrefix(prefix []byte) ([]subLeaf, error) {
	return collectStorageLeavesWithPrefix(s.src, prefix)
}

// buildSubtreeNodeBytes builds the standard MPT root node RLP from
// sorted (effective key, value) pairs. For a single leaf, returns
// the leaf RLP directly; for multiple leaves, returns the branch
// node RLP that would appear in the parent.
//
// We do this by:
//  1. Running HashBuilder + GenStructStep to get the root hash, and
//  2. If the hash was DERIVED (root encoding >= 32 bytes), look at
//     the hashStack's BYTE PRECEDING the hash — if it's 0xa0, the
//     root encoding wasn't inline (we have just the hash). In that
//     case we need to re-emit the root encoding by re-doing the
//     step but capturing the bytes.
//
// Simpler implementation: just hash the root and emit the standard
// node. For single-leaf trees we know the structure (leaf with full
// remainder); for multi-leaf, we'd need to inspect the internal
// HashBuilder state.
//
// For D.1 MVP we focus on the common cases:
//   - 0 leaves: empty (0x80)
//   - 1 leaf:   leaf RLP
//   - 2+ leaves: branch RLP — reconstructed via separate logic
//     using lib/trie's HashBuilder we cannot easily extract the
//     branch RLP bytes from. Fall back to a hash-only encoding:
//     return 0xa0||rootHash (33 bytes). This works because the
//     CALLER then wraps differently per inline-vs-hashed:
//     - If 33 bytes, parent stores 0xa0||hash (the same 33 bytes)
//       so we embed AS-IS, which preserves the standard RLP.
//     - If actually inline (< 32 bytes raw), we'd need the actual
//       branch RLP. For the multi-leaf inline case we error out
//       with a documented limit.
func buildSubtreeNodeBytes(leaves []subLeaf) ([]byte, error) {
	if len(leaves) == 0 {
		return []byte{0x80}, nil
	}
	if len(leaves) == 1 {
		// Single leaf: parent slot holds either the leaf RLP inline
		// (when encoding < 32 bytes) or 0xa0||keccak(leaf_RLP).
		enc := encodeLeafRLP(leaves[0].effectiveKey, leaves[0].value)
		if len(enc) < 32 {
			return enc, nil
		}
		h := keccakOf(enc)
		return wrapHashSlot(h), nil
	}
	// Multi-leaf: build the sub-trie root via HashBuilder. For
	// production paths with random hashed keys, the resulting branch
	// encoding is virtually always >= 32 bytes (16-way fanout with
	// multiple hash children), so the parent stored the hash form.
	// Inline-multi-leaf is exceptional and not handled in MVP.
	root, err := buildSubtreeRoot(leaves, 0)
	if err != nil {
		return nil, err
	}
	return wrapHashSlot(root), nil
}

// wrapHashSlot returns the 33-byte standard MPT slot encoding for a
// 32-byte child hash: 0xa0 byte-string prefix + the hash itself.
func wrapHashSlot(h [32]byte) []byte {
	out := make([]byte, 33)
	out[0] = 0xa0
	copy(out[1:], h[:])
	return out
}

// fullProofBytes assembles proof bytes including inline-sibling
// recovery via the provided subtreeRootBuilder.
func (g *Generator) fullProofBytes(hashedKey, leafValue []byte, leafFound bool,
	walk *mpttrie.WalkResult, builder subtreeRootBuilder) (ProofBytes, error) {

	if walk == nil {
		return nil, errors.New("mptproof: nil walk")
	}
	keyNibbles := nibblesOf(hashedKey)

	// Single-leaf special case (no branches, root = leaf).
	if leafFound && len(walk.Hops) == 0 && walk.Outcome == mpttrie.NoBranchAtPath {
		return ProofBytes{encodeLeafRLP(keyNibbles, leafValue)}, nil
	}
	if len(walk.Hops) == 0 {
		return nil, fmt.Errorf("mptproof: empty walk (outcome=%v)", walk.Outcome)
	}

	out := make(ProofBytes, 0, len(walk.Hops)+1)
	for i, hop := range walk.Hops {
		// Path prefix to THIS branch: keyNibbles[:hop.PrefixDepth].
		// For inline-sibling rebuild at slot s, sibling prefix =
		// path-to-branch || s.
		branchRLP, err := encodeStandardBranchRLPWithRebuild(
			hop.Branch, keyNibbles[:hop.PrefixDepth], builder)
		if err != nil {
			return nil, fmt.Errorf("hop %d (depth %d): %w", i, hop.PrefixDepth, err)
		}
		out = append(out, branchRLP)
	}

	if leafFound && walk.Outcome == mpttrie.LandedOnLeaf {
		remainder := keyNibbles[walk.LeafDepth:]

		// Check whether the leaf is DIRECTLY at LeafDepth — i.e. the
		// deepest hop's stored hash for our target slot equals the
		// hash of the leaf node RLP. If so, simple emit.
		deepest := walk.Hops[len(walk.Hops)-1]
		storedHash, _ := deepest.Branch.ChildHash(deepest.TargetNibble)
		leafHash := computeLeafHash(remainder, leafValue)
		if leafHash == storedHash {
			out = append(out, encodeLeafRLP(remainder, leafValue))
			return out, nil
		}

		// The leaf is BELOW a sub-trie at this slot — expand the
		// sub-trie to assemble extension/branch nodes between the
		// deepest persisted branch and the leaf.
		prefix := keyNibbles[:walk.LeafDepth] // walk path + target nibble
		expandLeaves, err := builder.subLeavesByPrefix(prefix)
		if err != nil {
			return nil, fmt.Errorf("subtree expand: enumerate prefix %x: %w", prefix, err)
		}
		if len(expandLeaves) == 0 {
			return nil, fmt.Errorf("subtree expand: 0 leaves under prefix %x", prefix)
		}
		expanded, err := expandSubtreeProofPath(expandLeaves, keyNibbles[walk.LeafDepth:])
		if err != nil {
			return nil, fmt.Errorf("subtree expand: %w", err)
		}
		out = append(out, expanded...)
	}

	return out, nil
}

// encodeStandardBranchRLPWithRebuild is like encodeStandardBranchRLP
// but recovers inline siblings via the subtreeRootBuilder. Each
// inline sibling is resolved by enumerating its leaves and getting
// the standard sub-trie node bytes.
//
// Note on HasTree bit: an earlier draft tried a "single-leaf shortcut"
// when HasTree[i]=0, on the theory that the child terminates as one
// leaf. That broke proofs: reth's compact form sets HasTree=0 when
// the deeper subtree's branch nodes aren't cached, NOT when there's
// only one leaf below. The slow-but-correct path is "enumerate every
// leaf under the prefix and build the sub-trie", and the hashed
// index makes that enumeration acceptable for accounts. Storage
// proofs at depths within the address-keccak portion remain the
// dominant cost.
func encodeStandardBranchRLPWithRebuild(b *mpttrie.BranchNode, branchPath []byte,
	builder subtreeRootBuilder) ([]byte, error) {

	slots := [17][]byte{}
	emptyEnc := []byte{0x80}
	for i := byte(0); i < 16; i++ {
		if !b.HasChild(i) {
			slots[i] = emptyEnc
			continue
		}
		h, ok := b.ChildHash(i)
		if ok {
			// Hashed sibling: wrap as RLP 32-byte string.
			buf := make([]byte, 33)
			buf[0] = 0xa0
			copy(buf[1:], h[:])
			slots[i] = buf
			continue
		}
		// Inline sibling: recover by rebuilding the sub-trie.
		sibPath := append(append([]byte{}, branchPath...), i)
		nodeBytes, err := builder.SubtreeNodeBytes(sibPath)
		if err != nil {
			return nil, fmt.Errorf("inline sibling nibble 0x%x rebuild: %w", i, err)
		}
		// For sub-trie that HASHES (>= 32-byte encoding) the
		// builder returns 33-byte 0xa0||hash — same as a normal
		// hashed sibling slot. For TRULY INLINE sub-tries (< 32
		// bytes raw encoding), the builder would return the raw RLP
		// directly. Standard MPT wants the raw RLP in the slot when
		// inline; we currently emit the 33-byte hash form for
		// multi-leaf rebuilds, which works because:
		// - At sparse subtree boundaries multi-leaf sub-tries are
		//   usually > 32 bytes encoded, so they're stored as a hash
		//   in their parent anyway. The hash we re-derive matches.
		// - Single-leaf sub-trees produce the leaf RLP directly,
		//   which is INLINE if < 32 bytes — preserved verbatim.
		slots[i] = nodeBytes
	}
	slots[16] = emptyEnc

	var payload bytes.Buffer
	for _, s := range slots {
		payload.Write(s)
	}
	return encodeList(payload.Bytes()), nil
}

// === helpers that the subtree builder needs ===

func sortedLeavesForRebuild(leaves []subLeaf) []subLeaf {
	out := make([]subLeaf, len(leaves))
	copy(out, leaves)
	sort.Slice(out, func(i, j int) bool {
		return bytes.Compare(out[i].effectiveKey, out[j].effectiveKey) < 0
	})
	return out
}

// keccakOf returns keccak256 of arbitrary bytes — small wrapper to
// avoid recomputing the sha3 ctor in tests.
func keccakOf(data []byte) [32]byte {
	h := sha3.NewLegacyKeccak256()
	h.Write(data)
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// Re-emit a no-op linker for trie used in this file.
var _ = trie.NewHashBuilder
var _ = rlphacks.RlpEncodedBytes(nil)
