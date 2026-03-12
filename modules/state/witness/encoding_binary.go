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
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/jmt"
	"github.com/n42blockchain/N42/modules/state"
)

// Binary witness format:
//   [32B parentRoot]
//   [u32 numAccountProofs] [accountProof...]
//   [u32 numStorageProofs] [storageProof...]
//   [u32 numCodes]         [code...]
//
// Each KeyProof:
//   [32B keyHash] [u32 originalKeyLen] [originalKey...] [u32 valueLen] [value...] [proof...]
//
// Each Proof:
//   [32B key] [u32 valueLen] [value...] [u32 numPathEntries] [pathEntry...]
//
// Each PathEntry:
//   [u32 nodeDataLen] [nodeData...] [i8 nibble]
//
// Each Code:
//   [32B hash] [u32 codeLen] [code...]

var (
	errBinaryWitnessShort    = errors.New("witness: binary data too short")
	errBinaryWitnessTooLarge = errors.New("witness: binary data exceeds maximum size")
)

// EncodeBinaryWitness serializes a BlockWitness into a compact binary format.
func EncodeBinaryWitness(w *BlockWitness) ([]byte, error) {
	// Estimate initial capacity.
	size := 32 + 4 + 4 + 4 // parentRoot + 3 counts
	for i := range w.AccountProofs {
		size += estimateKeyProofSize(&w.AccountProofs[i])
	}
	for i := range w.StorageProofs {
		size += estimateKeyProofSize(&w.StorageProofs[i])
	}
	for _, hc := range w.Codes {
		size += 32 + 4 + len(hc.Code)
	}

	buf := make([]byte, 0, size)

	// ParentRoot
	buf = append(buf, w.ParentRoot[:]...)

	// Account proofs
	buf = appendU32(buf, uint32(len(w.AccountProofs)))
	for i := range w.AccountProofs {
		buf = appendKeyProof(buf, &w.AccountProofs[i])
	}

	// Storage proofs
	buf = appendU32(buf, uint32(len(w.StorageProofs)))
	for i := range w.StorageProofs {
		buf = appendKeyProof(buf, &w.StorageProofs[i])
	}

	// Codes
	buf = appendU32(buf, uint32(len(w.Codes)))
	for _, hc := range w.Codes {
		buf = append(buf, hc.Hash[:]...)
		buf = appendBytes(buf, hc.Code)
	}

	return buf, nil
}

// DecodeBinaryWitness deserializes a BlockWitness from compact binary format.
func DecodeBinaryWitness(data []byte) (*BlockWitness, error) {
	if len(data) > MaxWitnessSize {
		return nil, errBinaryWitnessTooLarge
	}
	if len(data) < 32+4+4+4 { // parentRoot + 3 counts minimum
		return nil, errBinaryWitnessShort
	}

	r := &reader{data: data, pos: 0}
	w := &BlockWitness{}

	// ParentRoot
	rootBytes, err := r.readN(32)
	if err != nil {
		return nil, err
	}
	copy(w.ParentRoot[:], rootBytes)

	// Account proofs
	numAcct, err := r.readU32()
	if err != nil {
		return nil, err
	}
	w.AccountProofs = make([]KeyProof, numAcct)
	for i := uint32(0); i < numAcct; i++ {
		w.AccountProofs[i], err = readKeyProof(r)
		if err != nil {
			return nil, fmt.Errorf("witness: account proof %d: %w", i, err)
		}
	}

	// Storage proofs
	numStor, err := r.readU32()
	if err != nil {
		return nil, err
	}
	w.StorageProofs = make([]KeyProof, numStor)
	for i := uint32(0); i < numStor; i++ {
		w.StorageProofs[i], err = readKeyProof(r)
		if err != nil {
			return nil, fmt.Errorf("witness: storage proof %d: %w", i, err)
		}
	}

	// Codes
	numCodes, err := r.readU32()
	if err != nil {
		return nil, err
	}
	w.Codes = make([]*state.HashCode, numCodes)
	for i := uint32(0); i < numCodes; i++ {
		hashBytes, err := r.readN(types.HashLength)
		if err != nil {
			return nil, err
		}
		code, err := r.readBytes()
		if err != nil {
			return nil, err
		}
		hc := &state.HashCode{Code: code}
		copy(hc.Hash[:], hashBytes)
		w.Codes[i] = hc
	}

	return w, nil
}

// --- helpers ---

func appendU32(buf []byte, v uint32) []byte {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, v)
	return append(buf, b...)
}

func appendBytes(buf []byte, data []byte) []byte {
	buf = appendU32(buf, uint32(len(data)))
	return append(buf, data...)
}

func appendKeyProof(buf []byte, kp *KeyProof) []byte {
	// KeyHash
	buf = append(buf, kp.KeyHash[:]...)
	// OriginalKey
	buf = appendBytes(buf, kp.OriginalKey)
	// Value (nil encoded as length 0xFFFFFFFF)
	if kp.Value == nil {
		buf = appendU32(buf, 0xFFFFFFFF)
	} else {
		buf = appendBytes(buf, kp.Value)
	}
	// Proof
	buf = appendProof(buf, &kp.Proof)
	return buf
}

func appendProof(buf []byte, p *jmt.Proof) []byte {
	buf = append(buf, p.Key[:]...)
	if p.Value == nil {
		buf = appendU32(buf, 0xFFFFFFFF)
	} else {
		buf = appendBytes(buf, p.Value)
	}
	buf = appendU32(buf, uint32(len(p.Path)))
	for _, e := range p.Path {
		buf = appendBytes(buf, e.NodeData)
		buf = append(buf, byte(e.Nibble))
	}
	return buf
}

func estimateKeyProofSize(kp *KeyProof) int {
	s := jmt.HashSize + 4 + len(kp.OriginalKey) + 4
	if kp.Value != nil {
		s += len(kp.Value)
	}
	s += jmt.HashSize + 4 // proof key + value len
	if kp.Proof.Value != nil {
		s += len(kp.Proof.Value)
	}
	s += 4 // numPath
	for _, e := range kp.Proof.Path {
		s += 4 + len(e.NodeData) + 1
	}
	return s
}

// reader is a simple byte reader with position tracking.
type reader struct {
	data []byte
	pos  int
}

func (r *reader) readN(n int) ([]byte, error) {
	if r.pos+n > len(r.data) {
		return nil, io.ErrUnexpectedEOF
	}
	out := make([]byte, n)
	copy(out, r.data[r.pos:r.pos+n])
	r.pos += n
	return out, nil
}

func (r *reader) readU32() (uint32, error) {
	if r.pos+4 > len(r.data) {
		return 0, io.ErrUnexpectedEOF
	}
	v := binary.LittleEndian.Uint32(r.data[r.pos:])
	r.pos += 4
	return v, nil
}

func (r *reader) readBytes() ([]byte, error) {
	n, err := r.readU32()
	if err != nil {
		return nil, err
	}
	if n == 0xFFFFFFFF {
		return nil, nil
	}
	if n > uint32(MaxWitnessSize) {
		return nil, fmt.Errorf("witness: field size %d exceeds maximum", n)
	}
	return r.readN(int(n))
}

func readKeyProof(r *reader) (KeyProof, error) {
	var kp KeyProof

	// KeyHash
	hashBytes, err := r.readN(jmt.HashSize)
	if err != nil {
		return kp, err
	}
	copy(kp.KeyHash[:], hashBytes)

	// OriginalKey
	kp.OriginalKey, err = r.readBytes()
	if err != nil {
		return kp, err
	}

	// Value
	kp.Value, err = r.readBytes()
	if err != nil {
		return kp, err
	}

	// Proof
	kp.Proof, err = readProof(r)
	if err != nil {
		return kp, err
	}

	return kp, nil
}

func readProof(r *reader) (jmt.Proof, error) {
	var p jmt.Proof

	keyBytes, err := r.readN(jmt.HashSize)
	if err != nil {
		return p, err
	}
	copy(p.Key[:], keyBytes)

	p.Value, err = r.readBytes()
	if err != nil {
		return p, err
	}

	numPath, err := r.readU32()
	if err != nil {
		return p, err
	}
	p.Path = make([]jmt.ProofEntry, numPath)
	for i := uint32(0); i < numPath; i++ {
		p.Path[i].NodeData, err = r.readBytes()
		if err != nil {
			return p, err
		}
		nibbleByte, err := r.readN(1)
		if err != nil {
			return p, err
		}
		p.Path[i].Nibble = int8(nibbleByte[0])
	}

	return p, nil
}
