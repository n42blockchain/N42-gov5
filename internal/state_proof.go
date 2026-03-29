// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"fmt"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/jmt"
	jmtstore "github.com/n42blockchain/N42/lib/jmt/store"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

// StateProofBackend identifies the proof backend serving eth_getProof-style data.
type StateProofBackend string

const (
	StateProofBackendHashOnly    StateProofBackend = "hash-only"
	StateProofBackendJMT         StateProofBackend = "jmt"
	StateProofBackendEthereumMPT StateProofBackend = "ethereum-mpt"
)

// StateProofSemantics describes how closely the returned proof matches EIP-1186.
type StateProofSemantics string

const (
	StateProofSemanticsHashOnly         StateProofSemantics = "hash-only"
	StateProofSemanticsPartialEIP1186   StateProofSemantics = "partial-eip1186"
	StateProofSemanticsCanonicalEIP1186 StateProofSemantics = "canonical-eip1186"
)

// StorageHashSemantics describes how AccountResult.StorageHash is computed.
type StorageHashSemantics string

const (
	StorageHashSemanticsNone              StorageHashSemantics = "none"
	StorageHashSemanticsQueryTupleKeccak  StorageHashSemantics = "query-tuple-keccak"
	StorageHashSemanticsCanonicalTrieRoot StorageHashSemantics = "canonical-trie-root"
)

// StateProofDescriptor captures the semantics of the currently wired proof path.
// It intentionally distinguishes the proof root scheme from the block header's
// effective stateRoot scheme because the two can diverge on legacy/mainnet paths.
type StateProofDescriptor struct {
	Backend               StateProofBackend
	ProofRootScheme       state.RootScheme
	HeaderStateRootScheme state.RootScheme
	Semantics             StateProofSemantics
	StorageHash           StorageHashSemantics
	SupportsHistorical    bool
}

// StateProofProvider isolates proof serving from RPC wiring so a future
// canonical Ethereum MPT backend can be plugged in without rewriting APIs.
type StateProofProvider interface {
	Descriptor() StateProofDescriptor
	AccountProof(tx kv.Tx, address types.Address, blockNrOrHash jsonrpc.BlockNumberOrHash) ([]string, error)
	StorageProof(tx kv.Tx, address types.Address, slot types.Hash, blockNrOrHash jsonrpc.BlockNumberOrHash) ([]string, error)
	StorageHash(tx kv.Tx, address types.Address, slots []types.Hash, values []*uint256.Int, blockNrOrHash jsonrpc.BlockNumberOrHash) (types.Hash, error)
}

// DefaultStateProofDescriptor returns the fallback descriptor when no trie-backed
// proof provider is wired. This matches the existing hash-anchor placeholder path.
func DefaultStateProofDescriptor(headerScheme state.RootScheme) StateProofDescriptor {
	return StateProofDescriptor{
		Backend:               StateProofBackendHashOnly,
		ProofRootScheme:       state.RootSchemeUnknown,
		HeaderStateRootScheme: headerScheme,
		Semantics:             StateProofSemanticsHashOnly,
		StorageHash:           StorageHashSemanticsQueryTupleKeccak,
		SupportsHistorical:    false,
	}
}

// HeaderStateRootScheme reports the effective semantics of block header stateRoot
// values produced by this chain's current execution path.
func (bc *BlockChain) HeaderStateRootScheme() state.RootScheme {
	if bc == nil {
		return state.RootSchemeUnknown
	}
	if bc.rootComputer == nil || !bc.jmtForBlockProcessing {
		return state.RootSchemeLegacyKeccak
	}
	if reporter, ok := bc.rootComputer.(state.RootSchemeReporter); ok {
		return reporter.RootScheme()
	}
	return state.RootSchemeUnknown
}

// SetStateProofProvider installs the active proof provider used by RPC APIs.
func (bc *BlockChain) SetStateProofProvider(provider StateProofProvider) {
	bc.stateProofProvider = provider
}

// StateProofProvider returns the active proof provider, if any.
func (bc *BlockChain) StateProofProvider() StateProofProvider {
	if bc == nil {
		return nil
	}
	return bc.stateProofProvider
}

// StateProofDescriptor reports the effective proof-serving semantics currently
// exposed by the node, including any divergence from header stateRoot semantics.
func (bc *BlockChain) StateProofDescriptor() StateProofDescriptor {
	headerScheme := bc.HeaderStateRootScheme()
	if bc == nil || bc.stateProofProvider == nil {
		return DefaultStateProofDescriptor(headerScheme)
	}
	desc := bc.stateProofProvider.Descriptor()
	desc.HeaderStateRootScheme = headerScheme
	return desc
}

// JMTStateProofProvider adapts the current JMT proof path behind the generic
// StateProofProvider interface.
type JMTStateProofProvider struct {
	commitment *commitment.JMTCommitment
}

// NewJMTStateProofProvider wraps a JMT commitment as a generic proof provider.
func NewJMTStateProofProvider(c *commitment.JMTCommitment) *JMTStateProofProvider {
	if c == nil {
		return nil
	}
	return &JMTStateProofProvider{commitment: c}
}

// Descriptor describes the semantics of the current JMT proof path.
func (*JMTStateProofProvider) Descriptor() StateProofDescriptor {
	return StateProofDescriptor{
		Backend:            StateProofBackendJMT,
		ProofRootScheme:    state.RootSchemeJMTBlake3,
		Semantics:          StateProofSemanticsPartialEIP1186,
		StorageHash:        StorageHashSemanticsQueryTupleKeccak,
		SupportsHistorical: true,
	}
}

// AccountProof returns JMT proof nodes for the given account.
func (p *JMTStateProofProvider) AccountProof(tx kv.Tx, address types.Address, blockNrOrHash jsonrpc.BlockNumberOrHash) ([]string, error) {
	if p == nil || p.commitment == nil {
		return nil, nil
	}
	tree, err := p.resolveProofTree(tx, blockNrOrHash)
	if err != nil {
		return nil, err
	}
	var proof *jmt.Proof
	if tree != nil {
		proof, err = tree.GetProof(commitment.AccountKeyHash(address))
	} else {
		proof, err = p.commitment.GetAccountProof(address)
	}
	if err != nil {
		return nil, err
	}
	return encodeJMTProofNodes(proof), nil
}

// StorageProof returns JMT proof nodes for the given storage slot.
func (p *JMTStateProofProvider) StorageProof(tx kv.Tx, address types.Address, slot types.Hash, blockNrOrHash jsonrpc.BlockNumberOrHash) ([]string, error) {
	if p == nil || p.commitment == nil {
		return nil, nil
	}
	tree, err := p.resolveProofTree(tx, blockNrOrHash)
	if err != nil {
		return nil, err
	}
	var proof *jmt.Proof
	if tree != nil {
		proof, err = tree.GetProof(commitment.StorageKeyHash(address, slot))
	} else {
		proof, err = p.commitment.GetStorageProof(address, slot)
	}
	if err != nil {
		return nil, err
	}
	return encodeJMTProofNodes(proof), nil
}

// StorageHash returns the current non-canonical storageHash approximation used
// by the JMT proof path: keccak(query_slot || query_value) over the requested slots.
func (*JMTStateProofProvider) StorageHash(_ kv.Tx, _ types.Address, slots []types.Hash, values []*uint256.Int, _ jsonrpc.BlockNumberOrHash) (types.Hash, error) {
	if len(slots) != len(values) {
		return types.Hash{}, fmt.Errorf("storage hash input mismatch: %d slots, %d values", len(slots), len(values))
	}
	if len(slots) == 0 {
		return types.Hash{}, nil
	}
	data := make([]byte, 0, len(slots)*64)
	for i, slot := range slots {
		data = append(data, slot.Bytes()...)
		var word [32]byte
		if values[i] != nil {
			word = values[i].Bytes32()
		}
		data = append(data, word[:]...)
	}
	return crypto.Keccak256Hash(data), nil
}

func (p *JMTStateProofProvider) resolveProofTree(tx kv.Tx, blockNrOrHash jsonrpc.BlockNumberOrHash) (*jmt.Tree, error) {
	if p == nil || p.commitment == nil {
		return nil, nil
	}
	effectiveRoot, err := resolveJMTRoot(tx, p.commitment, blockNrOrHash)
	if err != nil || effectiveRoot == (types.Hash{}) || effectiveRoot == p.commitment.Root() {
		return nil, err
	}
	return p.commitment.SnapshotAt(effectiveRoot)
}

func encodeJMTProofNodes(proof *jmt.Proof) []string {
	if proof == nil {
		return nil
	}
	nodes := make([]string, len(proof.Path))
	for i, entry := range proof.Path {
		nodes[i] = hexutil.Encode(entry.NodeData)
	}
	return nodes
}

func resolveJMTRoot(tx kv.Tx, commit *commitment.JMTCommitment, blockNrOrHash jsonrpc.BlockNumberOrHash) (types.Hash, error) {
	if commit == nil {
		return types.Hash{}, nil
	}
	if blockNr, ok := blockNrOrHash.Number(); ok {
		switch blockNr {
		case jsonrpc.LatestBlockNumber, jsonrpc.PendingBlockNumber,
			jsonrpc.SafeBlockNumber, jsonrpc.FinalizedBlockNumber:
			return types.Hash{}, nil
		case jsonrpc.EarliestBlockNumber:
			return jmtRootAtHeight(tx, 0)
		}
		return jmtRootAtHeight(tx, uint64(blockNr))
	}
	if hash, ok := blockNrOrHash.Hash(); ok {
		header, err := rawdb.ReadHeaderByHash(tx, hash)
		if err != nil || header == nil {
			return types.Hash{}, err
		}
		return jmtRootAtHeight(tx, header.Number.Uint64())
	}
	return types.Hash{}, nil
}

func jmtRootAtHeight(tx kv.Tx, height uint64) (types.Hash, error) {
	root, _, err := jmtstore.ReadJMTVersionRootAt(tx, height)
	if err != nil {
		log.Warn("Failed to read JMT version root", "height", height, "err", err)
		return types.Hash{}, err
	}
	return types.Hash(root), nil
}
