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

// Package zkprover implements the ZK proof generation service that
// communicates with an external ZISK prover cluster via gRPC.
package zkprover

import (
	"encoding/binary"
	"errors"

	"github.com/n42blockchain/N42/common/types"
)

// ProofType identifies the proof system used.
type ProofType string

const (
	ProofTypeSTARK ProofType = "stark"
	ProofTypeSNARK ProofType = "snark"
)

// Proof represents a ZK proof for a block state transition.
type Proof struct {
	// BlockHash is the hash of the block this proof covers.
	BlockHash types.Hash

	// BlockNumber is the block number.
	BlockNumber uint64

	// ProofData contains the serialized proof bytes (STARK or SNARK).
	ProofData []byte

	// PublicInputs contains the public inputs for verification.
	// For STARK: [postStateRoot, receiptsRoot, gasUsed]
	PublicInputs []byte

	// Type is the proof system used.
	Type ProofType
}

// MaxProofSize is the maximum allowed size for a serialized proof (1 MB).
const MaxProofSize = 1 * 1024 * 1024

// EncodeProof serializes a Proof to bytes.
func EncodeProof(p *Proof) ([]byte, error) {
	if p == nil {
		return nil, errors.New("nil proof")
	}
	// Format: [32B blockHash][8B blockNumber][4B proofDataLen][proofData]
	//         [4B publicInputsLen][publicInputs][1B proofType]
	size := 32 + 8 + 4 + len(p.ProofData) + 4 + len(p.PublicInputs) + 1
	buf := make([]byte, 0, size)

	buf = append(buf, p.BlockHash[:]...)

	bn := make([]byte, 8)
	binary.LittleEndian.PutUint64(bn, p.BlockNumber)
	buf = append(buf, bn...)

	pd := make([]byte, 4)
	binary.LittleEndian.PutUint32(pd, uint32(len(p.ProofData)))
	buf = append(buf, pd...)
	buf = append(buf, p.ProofData...)

	pi := make([]byte, 4)
	binary.LittleEndian.PutUint32(pi, uint32(len(p.PublicInputs)))
	buf = append(buf, pi...)
	buf = append(buf, p.PublicInputs...)

	switch p.Type {
	case ProofTypeSTARK:
		buf = append(buf, 0)
	case ProofTypeSNARK:
		buf = append(buf, 1)
	default:
		buf = append(buf, 0)
	}

	return buf, nil
}

// DecodeProof deserializes a Proof from bytes.
func DecodeProof(data []byte) (*Proof, error) {
	if len(data) < 32+8+4+4+1 {
		return nil, errors.New("proof data too short")
	}
	if len(data) > MaxProofSize {
		return nil, errors.New("proof data exceeds maximum size")
	}

	p := &Proof{}
	pos := 0

	copy(p.BlockHash[:], data[pos:pos+32])
	pos += 32

	p.BlockNumber = binary.LittleEndian.Uint64(data[pos:])
	pos += 8

	if pos+4 > len(data) {
		return nil, errors.New("proof data length truncated")
	}
	pdLen := binary.LittleEndian.Uint32(data[pos:])
	pos += 4
	if pdLen > uint32(MaxProofSize) || pos+int(pdLen) > len(data) {
		return nil, errors.New("proof data truncated")
	}
	p.ProofData = make([]byte, pdLen)
	copy(p.ProofData, data[pos:pos+int(pdLen)])
	pos += int(pdLen)

	if pos+4 > len(data) {
		return nil, errors.New("proof public inputs truncated")
	}
	piLen := binary.LittleEndian.Uint32(data[pos:])
	pos += 4
	if piLen > uint32(MaxProofSize) || pos+int(piLen) > len(data) {
		return nil, errors.New("proof public inputs data truncated")
	}
	p.PublicInputs = make([]byte, piLen)
	copy(p.PublicInputs, data[pos:pos+int(piLen)])
	pos += int(piLen)

	if pos >= len(data) {
		return nil, errors.New("proof type missing")
	}
	switch data[pos] {
	case 0:
		p.Type = ProofTypeSTARK
	case 1:
		p.Type = ProofTypeSNARK
	default:
		p.Type = ProofTypeSTARK
	}

	return p, nil
}
