package mptproof

import (
	"bytes"
	"errors"
	"fmt"
	"sort"

	"github.com/n42blockchain/N42/internal/mpttrie"
	"github.com/n42blockchain/N42/lib/rlphacks"
	"github.com/n42blockchain/N42/lib/trie"
)

// VerifyFullAccount runs the standard fold verify; if it returns
// ErrExtensionInPath (typical for sparse-subtree boundaries in
// compact-encoded tries), falls back to the full subtree-rebuild
// path which enumerates ALL leaves matching the walk's prefix from
// the LeafSource and rebuilds the mini-MPT below the deepest hop to
// see if its root equals the stored hash.
//
// Returns:
//
//	ok=true                   proof is internally consistent
//	ok=false, ErrInlineLeaf   parent encodes leaf inline; verify needs
//	                          the full standard branch RLP we can't
//	                          reconstruct from compact form
//	ok=false, nil             proof is inconsistent (real failure)
//	ok=false, other err       internal error during rebuild
//
// Slow path: SLOW for shallow walks (rebuild scans full plain state).
// Acceptable for test / audit; not for RPC hot paths.
func (g *Generator) VerifyFullAccount(proof *AccountProof) (bool, error) {
	// 1. Try the cheap walk-fold check first.
	ok, err := proof.Verify()
	if ok {
		return true, nil
	}
	if errors.Is(err, ErrInlineLeaf) {
		// Cannot verify this from compact form; bubble up.
		return false, err
	}
	if !errors.Is(err, ErrExtensionInPath) {
		// Real mismatch (tampered root, hash collision, etc.).
		return false, err
	}
	// 2. Fall back to subtree rebuild.
	return g.verifyAccountBySubtreeRebuild(proof)
}

// VerifyFullStorage is the same fallback path for storage proofs.
func (g *Generator) VerifyFullStorage(proof *StorageProof) (bool, error) {
	ok, err := proof.Verify()
	if ok {
		return true, nil
	}
	if errors.Is(err, ErrInlineLeaf) {
		return false, err
	}
	if !errors.Is(err, ErrExtensionInPath) {
		return false, err
	}
	return g.verifyStorageBySubtreeRebuild(proof)
}

func (g *Generator) verifyAccountBySubtreeRebuild(proof *AccountProof) (bool, error) {
	walk := proof.Walk
	deepest := walk.Hops[len(walk.Hops)-1]
	expectedHash, ok := deepest.Branch.ChildHash(deepest.TargetNibble)
	if !ok {
		return false, ErrInlineLeaf
	}
	// Sub-trie root sits at depth = (parent branch depth) + 1.
	// All leaves under it share keyNibbles[:subtreeDepth] as prefix.
	subtreeDepth := deepest.PrefixDepth + 1
	keyNibbles := nibblesOf(proof.HashedAddr[:])
	if subtreeDepth > len(keyNibbles) {
		return false, fmt.Errorf("subtree depth %d > key nibble count %d", subtreeDepth, len(keyNibbles))
	}
	prefix := keyNibbles[:subtreeDepth]

	// Enumerate leaves from source whose hashed addr matches prefix.
	leaves, err := collectAccountLeavesWithPrefix(g.leaves, prefix)
	if err != nil {
		return false, err
	}
	if len(leaves) == 0 {
		return false, fmt.Errorf("subtree-rebuild: no leaves with prefix %x", prefix)
	}

	gotRoot, err := buildSubtreeRoot(leaves, subtreeDepth)
	if err != nil {
		return false, fmt.Errorf("subtree-rebuild: %w", err)
	}
	return gotRoot == expectedHash, nil
}

func (g *Generator) verifyStorageBySubtreeRebuild(proof *StorageProof) (bool, error) {
	walk := proof.Walk
	deepest := walk.Hops[len(walk.Hops)-1]
	expectedHash, ok := deepest.Branch.ChildHash(deepest.TargetNibble)
	if !ok {
		return false, ErrInlineLeaf
	}
	subtreeDepth := deepest.PrefixDepth + 1
	keyNibbles := nibblesOf(proof.HashedKey[:])
	if subtreeDepth > len(keyNibbles) {
		return false, fmt.Errorf("subtree depth %d > key nibble count %d", subtreeDepth, len(keyNibbles))
	}
	prefix := keyNibbles[:subtreeDepth]

	leaves, err := collectStorageLeavesWithPrefix(g.leaves, prefix)
	if err != nil {
		return false, err
	}
	if len(leaves) == 0 {
		return false, fmt.Errorf("subtree-rebuild: no storage leaves with prefix %x", prefix)
	}

	gotRoot, err := buildSubtreeRoot(leaves, subtreeDepth)
	if err != nil {
		return false, fmt.Errorf("subtree-rebuild: %w", err)
	}
	return gotRoot == expectedHash, nil
}

// subLeaf is one (effective key, value) pair within the sub-trie.
// effectiveKey = full hashed nibble key starting AT subtreeDepth,
// plus the 0x10 leaf terminator.
type subLeaf struct {
	effectiveKey []byte
	value        []byte
}

func collectAccountLeavesWithPrefix(src LeafSource, prefix []byte) ([]subLeaf, error) {
	// Fast path: LeafSource implementing HashedKeyScanner — the source
	// is sorted by keccak key (e.g. reth's HashedAccounts table) and
	// can be range-scanned by hashed prefix directly.
	if hs, ok := src.(HashedKeyScanner); ok {
		return hs.AccountsByHashedPrefix(prefix)
	}
	var out []subLeaf
	err := src.ScanAccounts(func(addr [20]byte, value []byte) error {
		h := keccak(addr[:])
		hn := nibblesOf(h)
		if !bytes.HasPrefix(hn, prefix) {
			return nil
		}
		eff := make([]byte, len(hn)-len(prefix)+1)
		copy(eff, hn[len(prefix):])
		eff[len(eff)-1] = 0x10
		out = append(out, subLeaf{effectiveKey: eff, value: value})
		return nil
	})
	return out, err
}

func collectStorageLeavesWithPrefix(src LeafSource, prefix []byte) ([]subLeaf, error) {
	// Fast path: HashedKeyScanner (e.g. reth's HashedStorages).
	if hs, ok := src.(HashedKeyScanner); ok {
		return hs.StorageByHashedPrefix(prefix)
	}
	var out []subLeaf
	err := src.ScanStorage(func(addr [20]byte, slot [32]byte, value []byte) error {
		// Storage trie composite key: keccak(addr) || keccak(slot)
		composite := append(keccak(addr[:]), keccak(slot[:])...)
		hn := nibblesOf(composite)
		if !bytes.HasPrefix(hn, prefix) {
			return nil
		}
		eff := make([]byte, len(hn)-len(prefix)+1)
		copy(eff, hn[len(prefix):])
		eff[len(eff)-1] = 0x10
		out = append(out, subLeaf{effectiveKey: eff, value: value})
		return nil
	})
	return out, err
}

// buildSubtreeRoot computes the standard MPT root over `leaves` using
// the same HashBuilder + GenStructStep pattern as Phase A's build (and
// as common/hash/derive_sha_erigon.go). The effective keys must
// already be relative (i.e., the common subtree prefix is stripped).
//
// The subtreeDepth argument is unused by the algorithm itself but is
// kept for diagnostic/error messages.
func buildSubtreeRoot(leaves []subLeaf, subtreeDepth int) ([32]byte, error) {
	if len(leaves) == 0 {
		return [32]byte{}, errors.New("buildSubtreeRoot: empty")
	}
	sort.Slice(leaves, func(i, j int) bool {
		return bytes.Compare(leaves[i].effectiveKey, leaves[j].effectiveKey) < 0
	})

	hb := trie.NewHashBuilder(false)
	var (
		groups, hasTree, hasHash []uint16
		curr, succ, currVal      []byte
		leafData                 trie.GenStructStepLeafData
	)
	retain := func(_ []byte) bool { return false }

	for _, e := range leaves {
		succ = e.effectiveKey
		if len(curr) > 0 {
			leafData.Value = rlphacks.RlpEncodedBytes(currVal)
			var err error
			groups, hasTree, hasHash, err = trie.GenStructStep(
				retain, curr, succ, hb, nil, &leafData,
				groups, hasTree, hasHash, false,
			)
			if err != nil {
				return [32]byte{}, fmt.Errorf("GenStructStep: %w", err)
			}
		}
		curr = append(curr[:0], succ...)
		currVal = append(currVal[:0], e.value...)
	}
	leafData.Value = rlphacks.RlpEncodedBytes(currVal)
	if _, _, _, err := trie.GenStructStep(
		retain, curr, []byte{}, hb, nil, &leafData,
		groups, hasTree, hasHash, false,
	); err != nil {
		return [32]byte{}, fmt.Errorf("final GenStructStep: %w", err)
	}
	root, err := hb.RootHash()
	if err != nil {
		return [32]byte{}, err
	}
	var out [32]byte
	copy(out[:], root[:])
	return out, nil
}

// Avoid unused-import warning if mpttrie is removed from this file
// in a future refactor (currently referenced via Branch indirectly).
var _ = mpttrie.BranchNode{}
