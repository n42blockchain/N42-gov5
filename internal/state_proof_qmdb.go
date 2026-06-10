// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// QMDB-native eth_getProof provider. The QMDB world root is a Blake3 twig-forest
// commitment (not an MPT), so it serves QMDB membership proofs: each proof is a
// single self-describing blob (qmdb.Proof.Marshal) carried in the EIP-1186
// accountProof / storageProof arrays. A client detects the QMDB backend from the
// proof descriptor and verifies with qmdb.VerifyEncodedProof against the block's
// stateRoot.
//
// The running node does not keep a live QMDB commitment (QMDB is built offline by
// the replay engine), so this provider lazily reloads the twig forest from the
// persisted entry log and caches it, rebuilding only when the head root changes.
//
// QMDB is history-dependent, so arbitrary past states are NOT provable — but the
// last W heights are: the replay engine records a tiny per-block undo record
// (QMDBUndoWindow table), and proofs at a recent height replay those records on
// scratch copies (qmdb.Tree.ProofAt), verifying the reconstructed root against
// the target block's header before serving anything.

package internal

import (
	"fmt"
	"sync"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/qmdb"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

// errQMDBHistorical is returned for queries outside the recent-blocks undo
// window (QMDB cannot prove arbitrary past states — the root is
// history-dependent). It is non-fatal — the RPC layer falls back to the
// hash-only path.
var errQMDBHistorical = fmt.Errorf("qmdb proof: height outside the recent-blocks proof window")

// QMDBStateProofProvider serves QMDB-native membership proofs for eth_getProof.
type QMDBStateProofProvider struct {
	mu         sync.Mutex
	rc         *commitment.QMDBRootComputer
	loadedRoot types.Hash
}

// NewQMDBStateProofProvider builds the provider (tree loaded lazily on first use).
func NewQMDBStateProofProvider() *QMDBStateProofProvider {
	return &QMDBStateProofProvider{}
}

// Descriptor reports the QMDB proof semantics so clients route verification to
// qmdb.VerifyEncodedProof and do not expect MPT/EIP-1186 node lists.
func (*QMDBStateProofProvider) Descriptor() StateProofDescriptor {
	return StateProofDescriptor{
		Backend:            StateProofBackendQMDB,
		ProofRootScheme:    state.RootSchemeQMDB,
		Semantics:          StateProofSemanticsHashOnly, // not EIP-1186 node lists
		StorageHash:        StorageHashSemanticsNone,    // QMDB has no per-account storage trie
		SupportsHistorical: true,                        // recent-blocks undo window only
	}
}

// AccountProof returns a QMDB membership proof blob for the account, or an empty
// array when the account is absent from the committed set.
func (p *QMDBStateProofProvider) AccountProof(tx kv.Tx, address types.Address, blockNrOrHash jsonrpc.BlockNumberOrHash) ([]string, error) {
	return p.proofFor(tx, qmdb.Hash(commitment.AccountKeyHash(address)), blockNrOrHash)
}

// StorageProof returns a QMDB membership proof blob for the storage slot, or an
// empty array when the slot is zero/absent.
func (p *QMDBStateProofProvider) StorageProof(tx kv.Tx, address types.Address, slot types.Hash, blockNrOrHash jsonrpc.BlockNumberOrHash) ([]string, error) {
	return p.proofFor(tx, qmdb.Hash(commitment.StorageKeyHash(address, slot)), blockNrOrHash)
}

// proofFor serves a membership proof for keyHash at the requested height:
// directly off the loaded forest for the latest block, via the recent-blocks
// undo window (qmdb.Tree.ProofAt) for an older height within the window.
func (p *QMDBStateProofProvider) proofFor(tx kv.Tx, kh qmdb.Hash, blockNrOrHash jsonrpc.BlockNumberOrHash) ([]string, error) {
	head, headRoot, err := latestHead(tx)
	if err != nil {
		return nil, err
	}
	tree, err := p.ensureTree(tx, headRoot)
	if err != nil {
		return nil, err
	}

	target, ok := resolveProofTarget(tx, blockNrOrHash, head)
	if !ok {
		return nil, errQMDBHistorical
	}
	if target == head {
		proof, found := tree.GetProof(kh)
		if !found {
			return []string{}, nil
		}
		return []string{hexutil.Encode(proof.Marshal())}, nil
	}

	// Recent-height path: replay the undo window down to the target.
	undos, err := rawdb.ReadQMDBUndos(tx, target, head)
	if err != nil {
		return nil, err
	}
	if undos == nil {
		return nil, errQMDBHistorical // window does not reach back to the target
	}
	proof, root, found, err := tree.ProofAt(kh, undos)
	if err != nil {
		return nil, err
	}
	// The reconstructed root must equal the target block's header root — a
	// mismatch means corrupt window data and must never serve a proof.
	targetHash, err := rawdb.ReadCanonicalHash(tx, target)
	if err != nil {
		return nil, err
	}
	targetHeader := rawdb.ReadHeader(tx, targetHash, target)
	if targetHeader == nil {
		return nil, fmt.Errorf("qmdb proof: header %d unavailable", target)
	}
	if types.Hash(root) != targetHeader.Root {
		return nil, fmt.Errorf("qmdb proof: reconstructed root %x != header root %x at block %d",
			root[:8], targetHeader.Root[:8], target)
	}
	if !found {
		return []string{}, nil
	}
	return []string{hexutil.Encode(proof.Marshal())}, nil
}

// StorageHash returns the zero hash: QMDB is a unified flat commitment with no
// per-account storage trie root.
func (*QMDBStateProofProvider) StorageHash(_ kv.Tx, _ types.Address, _ []types.Hash, _ []*uint256.Int, _ jsonrpc.BlockNumberOrHash) (types.Hash, error) {
	return types.Hash{}, nil
}

// ensureTree returns the loaded QMDB tree for the latest state, reloading from
// the persisted entry log when the head root has changed.
func (p *QMDBStateProofProvider) ensureTree(tx kv.Tx, headRoot types.Hash) (*qmdb.Tree, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.rc != nil && p.loadedRoot == headRoot {
		return p.rc.Tree(), nil
	}
	rc := commitment.NewQMDBRootComputer()
	if err := rc.LoadFrom(tx); err != nil {
		return nil, fmt.Errorf("qmdb proof: load forest: %w", err)
	}
	if rc.Root() != headRoot {
		return nil, fmt.Errorf("qmdb proof: reloaded root %x != head root %x", rc.Root(), headRoot)
	}
	p.rc = rc
	p.loadedRoot = headRoot
	return rc.Tree(), nil
}

// latestHead reads the head block number and its stateRoot.
func latestHead(tx kv.Tx) (uint64, types.Hash, error) {
	headPtr := rawdb.ReadCurrentBlockNumber(tx)
	if headPtr == nil {
		return 0, types.Hash{}, fmt.Errorf("qmdb proof: head block number unavailable")
	}
	hash, err := rawdb.ReadCanonicalHash(tx, *headPtr)
	if err != nil {
		return 0, types.Hash{}, err
	}
	header := rawdb.ReadHeader(tx, hash, *headPtr)
	if header == nil {
		return 0, types.Hash{}, fmt.Errorf("qmdb proof: head header %d unavailable", *headPtr)
	}
	return *headPtr, header.Root, nil
}

// resolveProofTarget maps the request to a concrete block number ≤ head.
// ok=false for unresolvable requests (future blocks, unknown hashes).
func resolveProofTarget(tx kv.Tx, blockNrOrHash jsonrpc.BlockNumberOrHash, head uint64) (uint64, bool) {
	if blockNr, ok := blockNrOrHash.Number(); ok {
		switch blockNr {
		case jsonrpc.LatestBlockNumber, jsonrpc.PendingBlockNumber,
			jsonrpc.SafeBlockNumber, jsonrpc.FinalizedBlockNumber:
			return head, true
		case jsonrpc.EarliestBlockNumber:
			return 0, head == 0
		}
		n := uint64(blockNr.Int64())
		return n, blockNr.Int64() >= 0 && n <= head
	}
	if hash, ok := blockNrOrHash.Hash(); ok {
		header, err := rawdb.ReadHeaderByHash(tx, hash)
		if err != nil || header == nil || header.Number == nil {
			return 0, false
		}
		n := header.Number.Uint64()
		return n, n <= head
	}
	return 0, false
}
