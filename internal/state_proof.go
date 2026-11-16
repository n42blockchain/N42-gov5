// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// State inclusion proof helpers. Builds JMT / MPT Merkle proofs for
// a given (address, slot) at a specified state root so RPC callers
// (eth_getProof) and the bridge subsystem can verify account data
// against a header commitment without a trusted full node.

package internal

import (
	"errors"
	"fmt"
	"strings"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/jmt"
	"github.com/n42blockchain/N42/lib/kv"
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
	StateProofBackendQMDB        StateProofBackend = "qmdb"
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
	// HistoricalMode details SupportsHistorical: "none", "recent-window"
	// (only the last RecentWindow heights are provable) or "full" (every
	// height from the earliest provable one). Empty for providers that do not
	// report it.
	HistoricalMode string
	// RecentWindow is the configured recent-heights proof window (blocks).
	RecentWindow uint64
}

// Historical proof modes reported in StateProofDescriptor.HistoricalMode.
const (
	StateProofHistoricalNone   = "none"
	StateProofHistoricalRecent = "recent-window"
	StateProofHistoricalFull   = "full"
)

// StateProofRange is the inclusive height range a provider can prove right now.
type StateProofRange struct {
	Earliest uint64
	Latest   uint64
}

// StateProofRangeReporter is implemented by providers whose provable range
// depends on what the database holds (a recent undo window, a history journal).
type StateProofRangeReporter interface {
	ProvableRange(tx kv.Tx) (StateProofRange, error)
}

// StateProofUnavailableError is returned instead of a proof when the request
// cannot be served with a verifiable proof (height outside the provable range,
// unknown block). It is a JSON-RPC error carrying the provable range as data.
type StateProofUnavailableError struct {
	Requested    uint64
	HasRequested bool
	Earliest     uint64
	Latest       uint64
	HasRange     bool
	Reason       string
}

func (e *StateProofUnavailableError) Error() string {
	var b strings.Builder
	b.WriteString("state proof unavailable")
	if e.HasRequested {
		fmt.Fprintf(&b, " for block %d", e.Requested)
	}
	if e.Reason != "" {
		b.WriteString(": ")
		b.WriteString(e.Reason)
	}
	if e.HasRange {
		fmt.Fprintf(&b, " (provable blocks: %d..%d)", e.Earliest, e.Latest)
	}
	return b.String()
}

// ErrorCode implements jsonrpc.Error (generic server error).
func (*StateProofUnavailableError) ErrorCode() int { return -32000 }

// ErrorData implements jsonrpc.DataError.
func (e *StateProofUnavailableError) ErrorData() interface{} {
	d := map[string]interface{}{"reason": e.Reason}
	if e.HasRequested {
		d["requestedBlock"] = hexutil.Uint64(e.Requested)
	}
	if e.HasRange {
		d["earliestProvableBlock"] = hexutil.Uint64(e.Earliest)
		d["latestProvableBlock"] = hexutil.Uint64(e.Latest)
	}
	return d
}

// StateProofProvider isolates proof serving from RPC wiring so a future
// canonical Ethereum MPT backend can be plugged in without rewriting APIs.
type StateProofProvider interface {
	Descriptor() StateProofDescriptor
	AccountProof(tx kv.Tx, address types.Address, blockNrOrHash jsonrpc.BlockNumberOrHash) ([]string, error)
	StorageProof(tx kv.Tx, address types.Address, slot types.Hash, blockNrOrHash jsonrpc.BlockNumberOrHash) ([]string, error)
	StorageHash(tx kv.Tx, address types.Address, slots []types.Hash, values []*uint256.Int, blockNrOrHash jsonrpc.BlockNumberOrHash) (types.Hash, error)
}

// ErrCanonicalStateProofNotImplemented marks a fail-closed canonical MPT proof
// path that has been wired conceptually but not implemented yet.
var ErrCanonicalStateProofNotImplemented = errors.New("canonical Ethereum MPT state proof provider not implemented")

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
		HistoricalMode:        StateProofHistoricalNone,
	}
}

// HeaderStateRootScheme reports the effective semantics of block header stateRoot
// values produced by this chain's current execution path.
func (bc *BlockChain) HeaderStateRootScheme() state.RootScheme {
	if bc == nil {
		return state.RootSchemeUnknown
	}
	// QMDB and persistent MPT have dedicated live-processing gates instead of
	// jmtForBlockProcessing. Report them before the legacy/JMT branch or a live
	// QMDB chain advertises QMDB proofs anchored to "legacy-keccak" headers.
	if bc.qmdbEnabled && bc.qmdbRootComputer != nil {
		return bc.qmdbRootComputer.RootScheme()
	}
	if bc.mptEnabled && bc.mptRootComputer != nil {
		return bc.mptRootComputer.RootScheme()
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
	// Read state root from block header — no separate VersionRoots table needed.
	h, err := rawdb.ReadCanonicalHash(tx, height)
	if err != nil {
		return types.Hash{}, err
	}
	header := rawdb.ReadHeader(tx, h, height)
	if header == nil {
		return types.Hash{}, fmt.Errorf("header not found at height %d", height)
	}
	return header.Root, nil
}

// EthereumMPTStateProofProvider generates canonical EIP-1186 Merkle proofs
// using CalcTrieRoot (erigon2.7 FlatDBTrieLoader) + ProofRetainer.
//
// Requires HashedAccounts, HashedStorage, TrieOfAccounts, TrieOfStorage tables
// to be populated (via replay --tree trie or rebuild-trie).
type EthereumMPTStateProofProvider struct{}

func NewEthereumMPTStateProofProvider() *EthereumMPTStateProofProvider {
	return &EthereumMPTStateProofProvider{}
}

func (*EthereumMPTStateProofProvider) Descriptor() StateProofDescriptor {
	return StateProofDescriptor{
		Backend:            StateProofBackendEthereumMPT,
		ProofRootScheme:    state.RootSchemeEthereumMPT,
		Semantics:          StateProofSemanticsCanonicalEIP1186,
		StorageHash:        StorageHashSemanticsCanonicalTrieRoot,
		SupportsHistorical: true,
	}
}

// AccountProof generates a canonical Ethereum MPT proof for an account.
func (*EthereumMPTStateProofProvider) AccountProof(_ kv.Tx, _ types.Address, _ jsonrpc.BlockNumberOrHash) ([]string, error) {
	return nil, ErrCanonicalStateProofNotImplemented
}

// StorageProof generates a canonical MPT proof for a storage slot.
func (*EthereumMPTStateProofProvider) StorageProof(_ kv.Tx, _ types.Address, _ types.Hash, _ jsonrpc.BlockNumberOrHash) ([]string, error) {
	return nil, ErrCanonicalStateProofNotImplemented
}

// StorageHash computes the canonical storage root for an account.
func (*EthereumMPTStateProofProvider) StorageHash(_ kv.Tx, _ types.Address, _ []types.Hash, _ []*uint256.Int, _ jsonrpc.BlockNumberOrHash) (types.Hash, error) {
	return types.Hash{}, ErrCanonicalStateProofNotImplemented
}
