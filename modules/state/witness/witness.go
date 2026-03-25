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

// Package witness implements block witness generation and verification
// for stateless EVM execution. A block witness contains the minimal set
// of Merkle proofs needed to verify state transitions without full state.
package witness

import (
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/jmt"
	"github.com/n42blockchain/N42/modules/state"
)

// KeyProof holds a single JMT Merkle proof for a state key.
// For inclusion proofs, Value contains the serialized data.
// For exclusion proofs, Value is nil.
type KeyProof struct {
	KeyHash     jmt.Hash  `json:"key_hash"`
	OriginalKey []byte    `json:"original_key"` // pre-image: address(20) for accounts, addr(20)||slot(32) for storage
	Value       []byte    `json:"value"`
	Proof       jmt.Proof `json:"proof"`
}

// BlockWitness contains all Merkle proofs required to verify a block's
// state transitions without access to the full state database.
type BlockWitness struct {
	// ParentRoot is the JMT state root before block execution.
	ParentRoot types.Hash `json:"parent_root"`

	// AccountProofs contains inclusion/exclusion proofs for all accounts
	// accessed during block execution.
	AccountProofs []KeyProof `json:"account_proofs"`

	// StorageProofs contains inclusion/exclusion proofs for all storage
	// slots accessed during block execution.
	StorageProofs []KeyProof `json:"storage_proofs"`

	// Codes contains the contract bytecode for all code accesses during
	// block execution, keyed by code hash.
	Codes []*state.HashCode `json:"codes"`

	// AncestorHashes contains up to 256 recent block hashes for the
	// BLOCKHASH opcode. Index 0 = parent, index 1 = grandparent, etc.
	// In the guest (stateless) context, these are the only block hashes available.
	AncestorHashes []types.Hash `json:"ancestor_hashes,omitempty"`
}

// Encode serializes a BlockWitness into bytes using JSON encoding.
func (w *BlockWitness) Encode() ([]byte, error) {
	return encodeBlockWitness(w)
}

// DecodeBlockWitness deserializes a BlockWitness from JSON-encoded bytes.
func DecodeBlockWitness(data []byte) (*BlockWitness, error) {
	return decodeBlockWitness(data)
}
