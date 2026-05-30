// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// merkle_stage.go — staged catch-up Merkle stage for hashed-canonical state.
//
// During staged catch-up the execution stage runs the TrieRootComputer in
// writeOnly mode: it writes HashedAccounts/HashedStorage + AccountChangeSet/
// StorageChangeSet per block but SKIPS the per-block CalcTrieRoot + TrieOf*
// update (no ~82ms/block dRoot). MerkleStageIncremental then computes the state
// root for a whole sub-batch [from,to] in ONE incremental pass, writing the
// updated intermediate nodes (TrieOf*) back.
//
// This mirrors erigon2.7 stage_interhashes.IncrementIntermediateHashes
// (C:/N42/erigon2.7/eth/stagedsync/stage_interhashes.go:553-626): build a
// RetainList of the range's touched keys from the changesets, then run
// FlatDBTrieLoader.CalcTrieRoot — which reads existing TrieOf* via cursor and
// recomputes ONLY the touched subtrees (canUse/SkipState skips untouched ones).
// Peak state-proportional memory is the RetainList of unique touched keys, NOT
// the full trie — so this is memory-safe at 25M-block state, unlike CalcStateRoot
// (the full-rebuild path that OOMs and is reserved for from-scratch/block-0).
//
// Caller contract: HashedAccounts/HashedStorage are already at the post-state of
// block `to` (writeOnly execution wrote them). MerkleStageIncremental returns the
// root for that post-state; the caller verifies it against block `to`'s wire
// header.Root, then commits (state + TrieOf* + head atomic) or rolls back the
// whole sub-batch.

package commitment

import (
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/common/length"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/trie"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/changeset"
)

// BuildRetainListFromChangesets builds a RetainList of the keccak-hashed touched
// keys across blocks [from,to] (inclusive) from AccountChangeSet/StorageChangeSet.
// Marker=true on every key (forces a leaf scan around the path — REQUIRED for
// inserts, harmless for modifies/deletes; matches ComputeRoot). Returns the list
// plus the unique account/storage key counts. Accepts any kv.Tx (RO or RW).
func BuildRetainListFromChangesets(tx kv.Tx, from, to uint64) (*trie.RetainList, int, int, error) {
	rl := trie.NewRetainList(0)

	// Account touches: AccountChangeSet key = plain address (20B) → keccak → 32B.
	seenAcc := make(map[types.Address]struct{})
	if err := changeset.ForRange(tx, modules.AccountChangeSet, from, to+1, func(_ uint64, k, _ []byte) error {
		if len(k) < length.Addr {
			return nil
		}
		var addr types.Address
		copy(addr[:], k[:length.Addr])
		if _, ok := seenAcc[addr]; ok {
			return nil
		}
		seenAcc[addr] = struct{}{}
		rl.AddKeyWithMarker(crypto.Keccak256(addr[:]), true)
		return nil
	}); err != nil {
		return nil, 0, 0, err
	}

	// Storage touches: StorageChangeSet key = plain address(20B)+slot(32B).
	// hashed composite = keccak(addr)(32B) + keccak(slot)(32B) = 64B.
	seenSto := make(map[string]struct{})
	if err := changeset.ForRange(tx, modules.StorageChangeSet, from, to+1, func(_ uint64, k, _ []byte) error {
		if len(k) < length.Addr+length.Hash {
			return nil
		}
		ck := string(k[:length.Addr+length.Hash])
		if _, ok := seenSto[ck]; ok {
			return nil
		}
		seenSto[ck] = struct{}{}
		var comp [64]byte
		copy(comp[:32], crypto.Keccak256(k[:length.Addr]))
		copy(comp[32:], crypto.Keccak256(k[length.Addr:length.Addr+length.Hash]))
		rl.AddKeyWithMarker(comp[:], true)
		return nil
	}); err != nil {
		return nil, 0, 0, err
	}
	return rl, len(seenAcc), len(seenSto), nil
}

// MerkleStageIncremental computes the state root at the post-state of block `to`
// by replaying the touched-key set of blocks [from,to] against the existing
// TrieOf* (incremental FlatDBTrieLoader), writing updated nodes back. `from` and
// `to` are inclusive. Returns the computed root for the caller to verify.
func MerkleStageIncremental(tx kv.RwTx, from, to uint64) (types.Hash, error) {
	rl, nAcc, nSto, err := BuildRetainListFromChangesets(tx, from, to)
	if err != nil {
		return types.Hash{}, err
	}

	trc := NewTrieRootComputer()
	trc.SetRwTx(tx)
	trc.SetIncremental(true)
	root, err := trc.flushTrieRoot(rl)
	if err != nil {
		return types.Hash{}, err
	}
	log.Info("MerkleStageIncremental", "from", from, "to", to,
		"accKeys", nAcc, "stoKeys", nSto, "root", root.Hex())
	return root, nil
}

// MerkleStageIncrementalWithProof is MerkleStageIncremental plus block-witness
// capture: it returns the standard-RLP multiproof for the touched keys of
// [from,to] alongside the computed root. The multiproof reconstructs to the
// returned (post-state) root, so a minimal client can verify the [from,to]
// transition against it. For a single block use from==to==N.
//
// Anchoring note: the captured proof is anchored at the COMPUTED (post) root —
// it is a post-state multiproof. A minimal client verifies with it in the
// reverse direction (post proof + changeset OLD values -> pre root), the model
// n42-stateless-transition uses. A forward pre-state proof (anchored at the
// N-1 root) is instead obtained by running this over [N,N] while the trie is
// still at N-1 (before block N's leaf writes are flushed).
func MerkleStageIncrementalWithProof(tx kv.RwTx, from, to uint64) (types.Hash, [][]byte, error) {
	rl, nAcc, nSto, err := BuildRetainListFromChangesets(tx, from, to)
	if err != nil {
		return types.Hash{}, nil, err
	}
	trc := NewTrieRootComputer()
	trc.SetRwTx(tx)
	trc.SetIncremental(true)
	trc.EnableProofCapture(true)
	root, err := trc.flushTrieRoot(rl)
	if err != nil {
		return types.Hash{}, nil, err
	}
	proof := trc.CapturedProof()
	log.Info("MerkleStageIncrementalWithProof", "from", from, "to", to,
		"accKeys", nAcc, "stoKeys", nSto, "root", root.Hex(), "proofNodes", len(proof))
	return root, proof, nil
}

// ExtractBlockMultiproof is the READ-ONLY twin of MerkleStageIncrementalWithProof:
// it captures the same touched-path multiproof for blocks [from,to] and returns
// the root the current trie reconstructs to, WITHOUT writing TrieOf* back (no-op
// HashCollectors). Because proof capture happens entirely in CalcTrieRoot's read
// pass, this takes a plain kv.Tx and is safe to run against a live datadir.
// Run it while the trie is at the post-state of `to` to obtain a multiproof
// anchored at header[to].Root.
func ExtractBlockMultiproof(tx kv.Tx, from, to uint64) (types.Hash, [][]byte, error) {
	rl, _, _, err := BuildRetainListFromChangesets(tx, from, to)
	if err != nil {
		return types.Hash{}, nil, err
	}
	wr := trie.NewWitnessRetainerFromList(rl)
	loader := trie.NewFlatDBTrieLoader("extract-proof", rl,
		func([]byte, uint16, uint16, uint16, []byte, []byte) error { return nil },
		func([]byte, []byte, uint16, uint16, uint16, []byte, []byte) error { return nil },
		false)
	loader.SetWitnessRetainer(wr)
	root, err := loader.CalcTrieRoot(tx, nil)
	if err != nil {
		return types.Hash{}, nil, err
	}
	return root, wr.Nodes(), nil
}
