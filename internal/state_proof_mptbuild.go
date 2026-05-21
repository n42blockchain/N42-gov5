// Copyright 2022-2026 The N42 Authors
//
// MPTProofProvider adapts internal/mptproof.Generator (canonical
// Ethereum MPT, built by cmd/n42-mpt-build) behind the generic
// StateProofProvider interface so eth_getProof RPC routes through it
// without needing handler changes.
//
// This is the live wiring for Phase D.3:
//   - Account proof bytes via Generator.FullAccountProofBytes
//   - Storage proof bytes via Generator.FullStorageProofBytes
//   - StorageHash via the unified storage trie's stateRoot
//     (NOT Ethereum-canonical per-account; matches our unified
//     trie architecture decision — Phase A.5 future work to switch
//     to per-account storage tries for full EIP-1186 parity)
//
// Limitations (documented; will be addressed in subsequent phases):
//   - Historical proofs (blockNrOrHash != latest) return an error
//     (Phase D.4 will integrate historicalstate for as-of queries)
//   - Inline siblings in the proof path trigger ScanAccounts/Storage
//     (SLOW — ~30s per inline sibling on reth-scale plain state)

package internal

import (
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/mptproof"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
	"github.com/n42blockchain/N42/modules/state"
)

// MPTProofProvider serves canonical EIP-1186 proofs from the N42
// MPT MDBX env (typically D:\n42-chaindata) via mptproof.Generator.
type MPTProofProvider struct {
	gen *mptproof.Generator
}

// NewMPTProofProvider wraps a Generator as a StateProofProvider.
// Caller retains ownership of the Generator and must Close it.
func NewMPTProofProvider(gen *mptproof.Generator) *MPTProofProvider {
	if gen == nil {
		return nil
	}
	return &MPTProofProvider{gen: gen}
}

func (p *MPTProofProvider) Descriptor() StateProofDescriptor {
	return StateProofDescriptor{
		Backend:            StateProofBackendEthereumMPT,
		ProofRootScheme:    state.RootSchemeEthereumMPT,
		Semantics:          StateProofSemanticsCanonicalEIP1186,
		StorageHash:        StorageHashSemanticsCanonicalTrieRoot,
		SupportsHistorical: false, // Phase D.4 will flip this
	}
}

// AccountProof returns the EIP-1186 accountProof array (hex strings,
// each one RLP-encoded standard MPT node, root → leaf).
func (p *MPTProofProvider) AccountProof(tx kv.Tx, address types.Address,
	blockNrOrHash jsonrpc.BlockNumberOrHash) ([]string, error) {

	if err := mvpRequireLatest(blockNrOrHash); err != nil {
		return nil, err
	}

	var addr [20]byte
	copy(addr[:], address[:])
	proof, err := p.gen.LatestAccountProof(addr)
	if err != nil {
		return nil, fmt.Errorf("MPTProofProvider AccountProof: %w", err)
	}
	pb, err := p.gen.FullAccountProofBytes(proof)
	if err != nil {
		return nil, fmt.Errorf("MPTProofProvider FullAccountProofBytes: %w", err)
	}
	return toHexStrings(pb), nil
}

// StorageProof returns the EIP-1186 proof array for one slot.
func (p *MPTProofProvider) StorageProof(tx kv.Tx, address types.Address,
	slot types.Hash, blockNrOrHash jsonrpc.BlockNumberOrHash) ([]string, error) {

	if err := mvpRequireLatest(blockNrOrHash); err != nil {
		return nil, err
	}

	var addr [20]byte
	copy(addr[:], address[:])
	var slot32 [32]byte
	copy(slot32[:], slot[:])
	proofs, err := p.gen.LatestStorageProofs(addr, [][32]byte{slot32})
	if err != nil {
		return nil, fmt.Errorf("MPTProofProvider StorageProof: %w", err)
	}
	if len(proofs) == 0 {
		return nil, errors.New("MPTProofProvider StorageProof: empty result")
	}
	pb, err := p.gen.FullStorageProofBytes(proofs[0])
	if err != nil {
		return nil, fmt.Errorf("MPTProofProvider FullStorageProofBytes: %w", err)
	}
	return toHexStrings(pb), nil
}

// StorageHash returns our storage trie root. Note: this is the
// UNIFIED storage trie root (composite key keccak(addr)||keccak(slot)),
// NOT the per-account storage root that EIP-1186 strictly specifies.
// For N42-native light clients that understand the unified scheme this
// is correct; for Ethereum-canonical compatibility, Phase A.5 must
// switch to per-account storage tries (separate work).
func (p *MPTProofProvider) StorageHash(tx kv.Tx, address types.Address,
	slots []types.Hash, values []*uint256.Int,
	blockNrOrHash jsonrpc.BlockNumberOrHash) (types.Hash, error) {

	if err := mvpRequireLatest(blockNrOrHash); err != nil {
		return types.Hash{}, err
	}

	root, err := p.gen.StorageTrieRoot()
	if err != nil {
		return types.Hash{}, fmt.Errorf("MPTProofProvider StorageHash: %w", err)
	}
	var out types.Hash
	copy(out[:], root[:])
	return out, nil
}

// mvpRequireLatest rejects non-latest block queries until Phase D.4
// wires historical proofs (via historicalstate overlay + on-demand
// subtree rebuild at blockN).
func mvpRequireLatest(blockNrOrHash jsonrpc.BlockNumberOrHash) error {
	if num, ok := blockNrOrHash.Number(); ok {
		if num == jsonrpc.LatestBlockNumber || num == jsonrpc.PendingBlockNumber {
			return nil
		}
		return fmt.Errorf("MPTProofProvider: historical proofs not yet supported (block %d) — Phase D.4 pending", int64(num))
	}
	if _, ok := blockNrOrHash.Hash(); ok {
		// Block-hash queries can target any block; reject for now.
		return errors.New("MPTProofProvider: block-hash queries not yet supported — Phase D.4 pending")
	}
	return nil
}

// toHexStrings converts [][]byte to []string of 0x-prefixed hex.
func toHexStrings(pb mptproof.ProofBytes) []string {
	out := make([]string, len(pb))
	for i, n := range pb {
		out[i] = "0x" + hex.EncodeToString(n)
	}
	return out
}
