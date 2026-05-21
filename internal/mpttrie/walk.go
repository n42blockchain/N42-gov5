package mpttrie

import (
	"context"
	"errors"
	"fmt"
)

// WalkResult describes the trie walk from root toward a target hashed
// key. Even for keys that don't exist we return what the walk found
// (so the caller can prove non-inclusion).
type WalkResult struct {
	// Hops is one entry per branch traversed, root first.
	Hops []Hop
	// Outcome describes where the walk terminated.
	Outcome WalkOutcome
	// TerminalHash is the leaf hash at the termination point when
	// Outcome == LandedOnLeaf. Empty otherwise.
	TerminalHash [32]byte
	// LeafDepth is the nibble depth at which the leaf hash was found
	// (only set when Outcome == LandedOnLeaf).
	LeafDepth int
}

type WalkOutcome int

const (
	WalkUnknown            WalkOutcome = iota
	LandedOnLeaf                       // child at nibble was a leaf (hash present, no deeper branch)
	NoSuchChild                        // branch HasState bit was 0 for the target nibble
	NoBranchAtPath                     // tried to descend but no branch stored at extended path
	WalkExhaustedTarget                // ran out of target nibbles before reaching a leaf
)

// Hop captures one branch traversal step.
type Hop struct {
	// PrefixDepth: nibble depth of THIS branch (0 for root, 1 for
	// branches directly below root, etc.).
	PrefixDepth int
	// Branch is the decoded node at this prefix.
	Branch *BranchNode
	// TargetNibble is the next nibble of the target key consumed
	// here (the nibble we descended into).
	TargetNibble byte
}

// Walk descends from the root of the trie toward the given hashed key
// nibble path. The path is variable length but typically 64 (for
// accounts: keccak(addr)) or 128 (for storage: keccak(addr)||keccak(slot))
// nibbles; the walk stops earlier if the trie diverges.
//
// We do NOT need a 0x10 terminator — only the raw key nibbles.
func (r *Reader) Walk(hashedKeyNibbles []byte) (*WalkResult, error) {
	if len(hashedKeyNibbles) == 0 {
		return nil, errors.New("mpttrie: empty target key")
	}
	res := &WalkResult{Hops: make([]Hop, 0, 16)}

	tx, err := r.db.BeginRo(context.Background())
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	prefix := make([]byte, 0, len(hashedKeyNibbles))
	depth := 0
	for {
		// Fetch branch at current prefix.
		v, err := tx.GetOne(r.table, prefix)
		if err != nil {
			return nil, fmt.Errorf("mpttrie: GetOne(%x): %w", prefix, err)
		}
		if v == nil {
			res.Outcome = NoBranchAtPath
			return res, nil
		}
		branch, _, err := DecodeBranch(v)
		if err != nil {
			return nil, fmt.Errorf("mpttrie: decode branch at %x: %w", prefix, err)
		}
		if depth >= len(hashedKeyNibbles) {
			res.Outcome = WalkExhaustedTarget
			res.Hops = append(res.Hops, Hop{PrefixDepth: depth, Branch: branch})
			return res, nil
		}
		n := hashedKeyNibbles[depth]
		hop := Hop{PrefixDepth: depth, Branch: branch, TargetNibble: n}
		res.Hops = append(res.Hops, hop)

		if !branch.HasChild(n) {
			res.Outcome = NoSuchChild
			return res, nil
		}
		if !branch.IsChildSubtree(n) {
			// Child is a leaf — its hash (if stored) is in this branch's hashes.
			h, ok := branch.ChildHash(n)
			if ok {
				res.Outcome = LandedOnLeaf
				res.TerminalHash = h
				res.LeafDepth = depth + 1
			} else {
				// HasState set but no hash and no subtree — embedded leaf
				// the parent encoding stored inline. We don't reconstruct
				// that here; the leaf value lives in PlainState anyway.
				res.Outcome = LandedOnLeaf
				res.LeafDepth = depth + 1
			}
			return res, nil
		}

		// Descend: child is its own branch stored at extended path.
		prefix = append(prefix, n)
		depth++
		if depth > 132 {
			return nil, fmt.Errorf("mpttrie: walk depth exceeded 132 (cycle?)")
		}
	}
}

// CollectSiblings folds the walk's hops into a flat list of sibling
// subtree hashes, root-first. This is the raw material a proof
// generator needs.
func (w *WalkResult) CollectSiblings() [][32]byte {
	var out [][32]byte
	for _, hop := range w.Hops {
		out = append(out, hop.Branch.SiblingHashes(hop.TargetNibble)...)
	}
	return out
}

// PathDepth reports the nibble depth at which the walk terminated.
func (w *WalkResult) PathDepth() int {
	if len(w.Hops) == 0 {
		return 0
	}
	return w.Hops[len(w.Hops)-1].PrefixDepth + 1
}
