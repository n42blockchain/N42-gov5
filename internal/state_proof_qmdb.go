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
// QMDB is history-dependent, so only the latest state is provable.

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

// errQMDBHistorical is returned for non-latest queries (QMDB cannot prove past
// states). It is non-fatal — the RPC layer falls back to the hash-only path.
var errQMDBHistorical = fmt.Errorf("qmdb proof: only the latest state is provable")

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
		SupportsHistorical: false,
	}
}

// AccountProof returns a QMDB membership proof blob for the account, or an empty
// array when the account is absent from the committed set.
func (p *QMDBStateProofProvider) AccountProof(tx kv.Tx, address types.Address, blockNrOrHash jsonrpc.BlockNumberOrHash) ([]string, error) {
	tree, err := p.ensureTree(tx, blockNrOrHash)
	if err != nil {
		return nil, err
	}
	kh := qmdb.Hash(commitment.AccountKeyHash(address))
	proof, ok := tree.GetProof(kh)
	if !ok {
		return []string{}, nil
	}
	return []string{hexutil.Encode(proof.Marshal())}, nil
}

// StorageProof returns a QMDB membership proof blob for the storage slot, or an
// empty array when the slot is zero/absent.
func (p *QMDBStateProofProvider) StorageProof(tx kv.Tx, address types.Address, slot types.Hash, blockNrOrHash jsonrpc.BlockNumberOrHash) ([]string, error) {
	tree, err := p.ensureTree(tx, blockNrOrHash)
	if err != nil {
		return nil, err
	}
	kh := qmdb.Hash(commitment.StorageKeyHash(address, slot))
	proof, ok := tree.GetProof(kh)
	if !ok {
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
// the persisted entry log when the head root has changed. Non-latest queries
// return errQMDBHistorical.
func (p *QMDBStateProofProvider) ensureTree(tx kv.Tx, blockNrOrHash jsonrpc.BlockNumberOrHash) (*qmdb.Tree, error) {
	headRoot, err := latestStateRoot(tx)
	if err != nil {
		return nil, err
	}
	if !proofRequestIsLatest(tx, blockNrOrHash, headRoot) {
		return nil, errQMDBHistorical
	}

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

// latestStateRoot reads the head block's stateRoot.
func latestStateRoot(tx kv.Tx) (types.Hash, error) {
	headPtr := rawdb.ReadCurrentBlockNumber(tx)
	if headPtr == nil {
		return types.Hash{}, fmt.Errorf("qmdb proof: head block number unavailable")
	}
	hash, err := rawdb.ReadCanonicalHash(tx, *headPtr)
	if err != nil {
		return types.Hash{}, err
	}
	header := rawdb.ReadHeader(tx, hash, *headPtr)
	if header == nil {
		return types.Hash{}, fmt.Errorf("qmdb proof: head header %d unavailable", *headPtr)
	}
	return header.Root, nil
}

// proofRequestIsLatest reports whether the request targets the latest state.
func proofRequestIsLatest(tx kv.Tx, blockNrOrHash jsonrpc.BlockNumberOrHash, headRoot types.Hash) bool {
	if blockNr, ok := blockNrOrHash.Number(); ok {
		switch blockNr {
		case jsonrpc.LatestBlockNumber, jsonrpc.PendingBlockNumber,
			jsonrpc.SafeBlockNumber, jsonrpc.FinalizedBlockNumber:
			return true
		}
		headPtr := rawdb.ReadCurrentBlockNumber(tx)
		return headPtr != nil && uint64(blockNr) == *headPtr
	}
	if hash, ok := blockNrOrHash.Hash(); ok {
		header, err := rawdb.ReadHeaderByHash(tx, hash)
		return err == nil && header != nil && header.Root == headRoot
	}
	return false
}
