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
//
// JSON codec for BlockWitness and its KeyProof/ProofEntry children.
// MaxWitnessSize (16 MiB) caps decoded payload length to prevent
// DoS. jsonKeyProof, jsonProofEntry and jsonProof mirror the native
// jmt types with hex-encoded byte slices for transport-safe
// human-readable serialization and round-trip convertibility.

package witness

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/jmt"
	"github.com/n42blockchain/N42/modules/state"
)

// MaxWitnessSize is the maximum allowed size for encoded witness data (16 MB).
const MaxWitnessSize = 16 * 1024 * 1024

// jsonKeyProof is the JSON-friendly representation of KeyProof.
// Byte slices are hex-encoded for human readability and transport safety.
type jsonKeyProof struct {
	KeyHash     string          `json:"key_hash"`
	OriginalKey *string         `json:"original_key,omitempty"` // hex-encoded pre-image
	Value       *string         `json:"value"`                  // nil pointer for exclusion proofs
	Proof       json.RawMessage `json:"proof"`
}

// jsonProofEntry is the JSON-friendly representation of jmt.ProofEntry.
type jsonProofEntry struct {
	NodeData string `json:"node_data"`
	Nibble   int8   `json:"nibble"`
}

// jsonProof is the JSON-friendly representation of jmt.Proof.
type jsonProof struct {
	Key   string           `json:"key"`
	Value *string          `json:"value"`
	Path  []jsonProofEntry `json:"path"`
}

// jsonHashCode is the JSON-friendly representation of state.HashCode.
type jsonHashCode struct {
	Hash string `json:"hash"`
	Code string `json:"code"`
}

// jsonBlockWitness is the JSON-friendly top-level structure.
type jsonBlockWitness struct {
	ParentRoot    string         `json:"parent_root"`
	AccountProofs []jsonKeyProof `json:"account_proofs"`
	StorageProofs []jsonKeyProof `json:"storage_proofs"`
	Codes         []jsonHashCode `json:"codes"`
}

func encodeProof(p *jmt.Proof) (json.RawMessage, error) {
	jp := jsonProof{
		Key:  hex.EncodeToString(p.Key[:]),
		Path: make([]jsonProofEntry, len(p.Path)),
	}
	if p.Value != nil {
		v := hex.EncodeToString(p.Value)
		jp.Value = &v
	}
	for i, e := range p.Path {
		jp.Path[i] = jsonProofEntry{
			NodeData: hex.EncodeToString(e.NodeData),
			Nibble:   e.Nibble,
		}
	}
	return json.Marshal(jp)
}

func decodeProof(raw json.RawMessage) (jmt.Proof, error) {
	var jp jsonProof
	if err := json.Unmarshal(raw, &jp); err != nil {
		return jmt.Proof{}, err
	}

	keyBytes, err := hex.DecodeString(jp.Key)
	if err != nil {
		return jmt.Proof{}, errors.New("witness: invalid proof key hex")
	}
	var proof jmt.Proof
	if len(keyBytes) != jmt.HashSize {
		return jmt.Proof{}, errors.New("witness: proof key wrong length")
	}
	copy(proof.Key[:], keyBytes)

	if jp.Value != nil {
		proof.Value, err = hex.DecodeString(*jp.Value)
		if err != nil {
			return jmt.Proof{}, errors.New("witness: invalid proof value hex")
		}
	}

	proof.Path = make([]jmt.ProofEntry, len(jp.Path))
	for i, pe := range jp.Path {
		nodeData, err := hex.DecodeString(pe.NodeData)
		if err != nil {
			return jmt.Proof{}, errors.New("witness: invalid proof node_data hex")
		}
		proof.Path[i] = jmt.ProofEntry{
			NodeData: nodeData,
			Nibble:   pe.Nibble,
		}
	}
	return proof, nil
}

func encodeKeyProof(kp *KeyProof) (jsonKeyProof, error) {
	proofRaw, err := encodeProof(&kp.Proof)
	if err != nil {
		return jsonKeyProof{}, err
	}
	jkp := jsonKeyProof{
		KeyHash: hex.EncodeToString(kp.KeyHash[:]),
		Proof:   proofRaw,
	}
	if len(kp.OriginalKey) > 0 {
		ok := hex.EncodeToString(kp.OriginalKey)
		jkp.OriginalKey = &ok
	}
	if kp.Value != nil {
		v := hex.EncodeToString(kp.Value)
		jkp.Value = &v
	}
	return jkp, nil
}

func decodeKeyProof(jkp jsonKeyProof) (KeyProof, error) {
	keyBytes, err := hex.DecodeString(jkp.KeyHash)
	if err != nil {
		return KeyProof{}, errors.New("witness: invalid key_hash hex")
	}
	var kp KeyProof
	if len(keyBytes) != jmt.HashSize {
		return KeyProof{}, errors.New("witness: key_hash wrong length")
	}
	copy(kp.KeyHash[:], keyBytes)

	if jkp.OriginalKey != nil {
		kp.OriginalKey, err = hex.DecodeString(*jkp.OriginalKey)
		if err != nil {
			return KeyProof{}, errors.New("witness: invalid original_key hex")
		}
	}

	if jkp.Value != nil {
		kp.Value, err = hex.DecodeString(*jkp.Value)
		if err != nil {
			return KeyProof{}, errors.New("witness: invalid value hex")
		}
	}

	kp.Proof, err = decodeProof(jkp.Proof)
	if err != nil {
		return KeyProof{}, err
	}
	return kp, nil
}

// encodeBlockWitness serializes a BlockWitness to JSON with hex-encoded byte slices.
func encodeBlockWitness(w *BlockWitness) ([]byte, error) {
	jw := jsonBlockWitness{
		ParentRoot:    hex.EncodeToString(w.ParentRoot[:]),
		AccountProofs: make([]jsonKeyProof, len(w.AccountProofs)),
		StorageProofs: make([]jsonKeyProof, len(w.StorageProofs)),
		Codes:         make([]jsonHashCode, len(w.Codes)),
	}

	for i := range w.AccountProofs {
		jkp, err := encodeKeyProof(&w.AccountProofs[i])
		if err != nil {
			return nil, err
		}
		jw.AccountProofs[i] = jkp
	}

	for i := range w.StorageProofs {
		jkp, err := encodeKeyProof(&w.StorageProofs[i])
		if err != nil {
			return nil, err
		}
		jw.StorageProofs[i] = jkp
	}

	for i, hc := range w.Codes {
		jw.Codes[i] = jsonHashCode{
			Hash: hex.EncodeToString(hc.Hash[:]),
			Code: hex.EncodeToString(hc.Code),
		}
	}

	return json.Marshal(jw)
}

// decodeBlockWitness deserializes a BlockWitness from JSON.
func decodeBlockWitness(data []byte) (*BlockWitness, error) {
	if len(data) > MaxWitnessSize {
		return nil, fmt.Errorf("witness data exceeds maximum size: %d > %d", len(data), MaxWitnessSize)
	}

	var jw jsonBlockWitness
	if err := json.Unmarshal(data, &jw); err != nil {
		return nil, err
	}

	rootBytes, err := hex.DecodeString(jw.ParentRoot)
	if err != nil {
		return nil, errors.New("witness: invalid parent_root hex")
	}
	w := &BlockWitness{}
	if len(rootBytes) != types.HashLength {
		return nil, errors.New("witness: parent_root wrong length")
	}
	copy(w.ParentRoot[:], rootBytes)

	w.AccountProofs = make([]KeyProof, len(jw.AccountProofs))
	for i, jkp := range jw.AccountProofs {
		kp, err := decodeKeyProof(jkp)
		if err != nil {
			return nil, err
		}
		w.AccountProofs[i] = kp
	}

	w.StorageProofs = make([]KeyProof, len(jw.StorageProofs))
	for i, jkp := range jw.StorageProofs {
		kp, err := decodeKeyProof(jkp)
		if err != nil {
			return nil, err
		}
		w.StorageProofs[i] = kp
	}

	w.Codes = make([]*state.HashCode, len(jw.Codes))
	for i, jhc := range jw.Codes {
		hashBytes, err := hex.DecodeString(jhc.Hash)
		if err != nil {
			return nil, errors.New("witness: invalid code hash hex")
		}
		codeBytes, err := hex.DecodeString(jhc.Code)
		if err != nil {
			return nil, errors.New("witness: invalid code hex")
		}
		hc := &state.HashCode{Code: codeBytes}
		if len(hashBytes) != types.HashLength {
			return nil, errors.New("witness: code hash wrong length")
		}
		copy(hc.Hash[:], hashBytes)
		w.Codes[i] = hc
	}

	return w, nil
}
