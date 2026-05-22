package mptproof

import (
	"fmt"

	"github.com/n42blockchain/N42/internal/mpttrie"
)

// WalkRethAccount descends reth's AccountsTrie following the
// nibblized hashed-address path, collecting branches as Hops. Mirrors
// mpttrie.Reader.Walk but reads reth's per-row format (variable-len
// nibble keys, LE u16 masks).
//
//	hashedKey: 32-byte keccak(addr) (caller nibblizes internally)
//
// Reth stores branches starting at depth 1; the root branch at
// depth 0 is implicit (= 16-child branch derived from rows at
// nibble [0]..[f]). Our walker mirrors this convention.
func WalkRethAccount(r *RethTrieReader, hashedKey []byte) (*mpttrie.WalkResult, error) {
	nibbles := nibblesOf(hashedKey)
	return walkRethGeneric(nibbles, func(prefix []byte) (*mpttrie.BranchNode, bool, error) {
		return r.AccountBranchAt(prefix)
	})
}

// WalkRethStorage descends reth's StoragesTrie for a given contract
// (identified by addrHash) following the nibblized slot-keccak path.
func WalkRethStorage(r *RethTrieReader, addrHash []byte, slotHashedKey []byte) (*mpttrie.WalkResult, error) {
	if len(addrHash) != 32 {
		return nil, fmt.Errorf("WalkRethStorage: addrHash must be 32B, got %d", len(addrHash))
	}
	nibbles := nibblesOf(slotHashedKey)
	return walkRethGeneric(nibbles, func(prefix []byte) (*mpttrie.BranchNode, bool, error) {
		return r.StorageBranchAt(addrHash, prefix)
	})
}

// walkRethGeneric descends nibble by nibble. Reth stores
// AccountsTrie / StoragesTrie branches starting at depth 1 (one
// nibble consumed); the depth-0 root branch is implicit (= the 16
// depth-1 branches' hashes). Our walker therefore:
//
//   - Skips the depth-0 read.
//   - Reads the depth-1 branch for the target's first nibble as
//     the first hop. (PrefixDepth = 1 for that hop.)
//   - Descends as usual from there.
//
// The implicit root branch is reconstructed elsewhere (proof gen)
// by reading all 16 depth-1 rows and composing them.
func walkRethGeneric(
	hashedKeyNibbles []byte,
	getBranch func(prefix []byte) (*mpttrie.BranchNode, bool, error),
) (*mpttrie.WalkResult, error) {
	res := &mpttrie.WalkResult{Hops: make([]mpttrie.Hop, 0, 16)}
	if len(hashedKeyNibbles) == 0 {
		res.Outcome = mpttrie.WalkUnknown
		return res, nil
	}

	// Start at depth 1 (skip implicit depth-0 root). The "prefix"
	// for the FIRST branch we read is hashedKeyNibbles[:1].
	prefix := []byte{hashedKeyNibbles[0]}
	depth := 1

	for {
		branch, ok, err := getBranch(prefix)
		if err != nil {
			return nil, fmt.Errorf("walk reth: getBranch(%x): %w", prefix, err)
		}
		if !ok {
			res.Outcome = mpttrie.NoBranchAtPath
			return res, nil
		}
		if depth >= len(hashedKeyNibbles) {
			res.Outcome = mpttrie.WalkExhaustedTarget
			res.Hops = append(res.Hops, mpttrie.Hop{PrefixDepth: depth, Branch: branch})
			return res, nil
		}
		n := hashedKeyNibbles[depth]
		hop := mpttrie.Hop{PrefixDepth: depth, Branch: branch, TargetNibble: n}
		res.Hops = append(res.Hops, hop)

		if !branch.HasChild(n) {
			res.Outcome = mpttrie.NoSuchChild
			return res, nil
		}
		if !branch.IsChildSubtree(n) {
			h, hashOk := branch.ChildHash(n)
			if hashOk {
				res.Outcome = mpttrie.LandedOnLeaf
				res.TerminalHash = h
				res.LeafDepth = depth + 1
			} else {
				res.Outcome = mpttrie.LandedOnLeaf
				res.LeafDepth = depth + 1
			}
			return res, nil
		}
		prefix = append(prefix, n)
		depth++
		if depth > 132 {
			return nil, fmt.Errorf("walk reth: depth exceeded 132 (cycle?)")
		}
	}
}
