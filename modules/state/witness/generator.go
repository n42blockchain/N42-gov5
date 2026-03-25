// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package witness

import (
	"errors"
	"fmt"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

// Generator produces a BlockWitness from a JMTCommitment and a TracingReader
// that recorded all state accesses during block execution.
type Generator struct {
	commitment *commitment.JMTCommitment
}

// NewGenerator creates a witness Generator backed by the given JMTCommitment.
// Returns nil and an error if c is nil.
func NewGenerator(c *commitment.JMTCommitment) (*Generator, error) {
	if c == nil {
		return nil, errors.New("witness: NewGenerator called with nil JMTCommitment")
	}
	return &Generator{commitment: c}, nil
}

// Generate builds a BlockWitness containing Merkle proofs for every state key
// accessed by the tracer, plus any contract code that was read.
//
// parentRoot is the state root before block execution (used as anchor).
// tracer contains the set of accessed accounts, storage slots, and codes.
// codes maps codeHash to bytecode for all contracts whose code was accessed.
// SetAncestorHashes populates the witness with recent block hashes for BLOCKHASH.
func (w *BlockWitness) SetAncestorHashes(hashes []types.Hash) {
	if len(hashes) > 256 {
		hashes = hashes[:256]
	}
	w.AncestorHashes = hashes
}

func (g *Generator) Generate(parentRoot types.Hash, tracer *TracingReader, codes map[types.Hash][]byte) (*BlockWitness, error) {
	if tracer == nil {
		return nil, errors.New("witness: Generate called with nil tracer")
	}

	w := &BlockWitness{
		ParentRoot: parentRoot,
	}

	// Generate account proofs.
	accessedAccounts := tracer.AccessedAccounts()
	w.AccountProofs = make([]KeyProof, 0, len(accessedAccounts))
	for _, addr := range accessedAccounts {
		proof, err := g.commitment.GetAccountProof(addr)
		if err != nil {
			return nil, fmt.Errorf("witness: account proof for %x: %w", addr, err)
		}
		kp := KeyProof{
			KeyHash:     proof.Key,
			OriginalKey: addr[:],
			Value:       proof.Value,
			Proof:       *proof,
		}
		w.AccountProofs = append(w.AccountProofs, kp)
	}

	// Generate storage proofs.
	accessedStorage := tracer.AccessedStorage()
	for addr, slots := range accessedStorage {
		for _, slot := range slots {
			proof, err := g.commitment.GetStorageProof(addr, slot)
			if err != nil {
				return nil, fmt.Errorf("witness: storage proof for %x slot %x: %w", addr, slot, err)
			}
			originalKey := make([]byte, types.AddressLength+types.HashLength)
			copy(originalKey[:types.AddressLength], addr[:])
			copy(originalKey[types.AddressLength:], slot[:])
			kp := KeyProof{
				KeyHash:     proof.Key,
				OriginalKey: originalKey,
				Value:       proof.Value,
				Proof:       *proof,
			}
			w.StorageProofs = append(w.StorageProofs, kp)
		}
	}

	// Collect accessed contract codes.
	// The codes map is already filtered to only contain codes accessed during
	// block execution (provided by the caller, e.g. from ibs.CodeHashes()).
	for hash, code := range codes {
		hc := &state.HashCode{
			Hash: hash,
			Code: make([]byte, len(code)),
		}
		copy(hc.Code, code)
		w.Codes = append(w.Codes, hc)
	}

	return w, nil
}
