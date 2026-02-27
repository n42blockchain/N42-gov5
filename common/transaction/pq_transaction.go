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

// Post-Quantum Transaction Type (Type 0x05)
// This transaction type supports post-quantum cryptographic signatures
// including Falcon-512, SQIsign, and Dilithium for quantum-resistant security.

package transaction

import (
	"errors"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/types"
)

// PostQuantumTxType is the transaction type for post-quantum transactions
const PostQuantumTxType = 0x05

// Post-quantum signature algorithm identifiers
const (
	// PQAlgoFalcon512 uses Falcon-512 signatures (NIST standard, fast, 666B sig)
	PQAlgoFalcon512 uint8 = 0

	// PQAlgoSQIsign uses SQIsign-I signatures (compact, 177B sig, slower)
	PQAlgoSQIsign uint8 = 1

	// PQAlgoDilithium2 uses Dilithium2 signatures (NIST standard, 2420B sig)
	PQAlgoDilithium2 uint8 = 2

	// PQAlgoDilithium3 uses Dilithium3 signatures (higher security)
	PQAlgoDilithium3 uint8 = 3
)

// Post-quantum key and signature sizes
const (
	// Falcon-512 sizes
	Falcon512PublicKeySize  = 897
	Falcon512PrivateKeySize = 1281
	Falcon512SignatureSize  = 690 // Maximum size

	// SQIsign-I sizes
	SQIsignPublicKeySize  = 64
	SQIsignPrivateKeySize = 782
	SQIsignSignatureSize  = 177

	// Dilithium2 sizes
	Dilithium2PublicKeySize  = 1312
	Dilithium2PrivateKeySize = 2528
	Dilithium2SignatureSize  = 2420

	// Dilithium3 sizes
	Dilithium3PublicKeySize  = 1952
	Dilithium3PrivateKeySize = 4000
	Dilithium3SignatureSize  = 3293

	// Public key hash size (used for subsequent transactions)
	PQPublicKeyHashSize = 32
)

// PostQuantumTx represents a post-quantum secure transaction.
type PostQuantumTx struct {
	ChainID   *uint256.Int   // Chain ID
	Nonce     uint64         // Sender nonce
	GasTipCap *uint256.Int   // Max priority fee per gas (EIP-1559)
	GasFeeCap *uint256.Int   // Max fee per gas (EIP-1559)
	Gas       uint64         // Gas limit
	To        *types.Address `rlp:"nil"` // Recipient (nil for contract creation)
	Value     *uint256.Int   // Wei amount
	Data      []byte         // Call data

	// Access list (EIP-2930 compatible)
	AccessList AccessList

	// Post-quantum specific fields
	SigAlgo     uint8  // Signature algorithm (0=Falcon, 1=SQIsign, 2=Dilithium2, 3=Dilithium3)
	PubKeyMode  uint8  // 0=Full public key, 1=Public key hash (reference)
	PubKeyData  []byte // Full public key or 32-byte hash
	PQSignature []byte // Post-quantum signature

	// Legacy signature fields (unused for PQ, kept for interface compatibility)
	V *uint256.Int
	R *uint256.Int
	S *uint256.Int
}

func (tx *PostQuantumTx) txType() byte { return PostQuantumTxType }

func (tx *PostQuantumTx) copy() TxData {
	cpy := &PostQuantumTx{
		Nonce:      tx.Nonce,
		To:         copyAddressPtr(tx.To),
		Data:       make([]byte, len(tx.Data)),
		Gas:        tx.Gas,
		SigAlgo:    tx.SigAlgo,
		PubKeyMode: tx.PubKeyMode,
		// Initialize uint256 fields
		ChainID:   new(uint256.Int),
		GasTipCap: new(uint256.Int),
		GasFeeCap: new(uint256.Int),
		Value:     new(uint256.Int),
		V:         new(uint256.Int),
		R:         new(uint256.Int),
		S:         new(uint256.Int),
		// Copy access list
		AccessList: copyAccessList(tx.AccessList),
	}

	copy(cpy.Data, tx.Data)

	// Copy PQ-specific fields
	if len(tx.PubKeyData) > 0 {
		cpy.PubKeyData = make([]byte, len(tx.PubKeyData))
		copy(cpy.PubKeyData, tx.PubKeyData)
	}
	if len(tx.PQSignature) > 0 {
		cpy.PQSignature = make([]byte, len(tx.PQSignature))
		copy(cpy.PQSignature, tx.PQSignature)
	}

	if tx.ChainID != nil {
		cpy.ChainID.Set(tx.ChainID)
	}
	if tx.GasTipCap != nil {
		cpy.GasTipCap.Set(tx.GasTipCap)
	}
	if tx.GasFeeCap != nil {
		cpy.GasFeeCap.Set(tx.GasFeeCap)
	}
	if tx.Value != nil {
		cpy.Value.Set(tx.Value)
	}
	if tx.V != nil {
		cpy.V.Set(tx.V)
	}
	if tx.R != nil {
		cpy.R.Set(tx.R)
	}
	if tx.S != nil {
		cpy.S.Set(tx.S)
	}

	return cpy
}

func (tx *PostQuantumTx) chainID() *uint256.Int       { return tx.ChainID }
func (tx *PostQuantumTx) accessList() AccessList      { return tx.AccessList }
func (tx *PostQuantumTx) authList() AuthorizationList { return nil } // Not supported
func (tx *PostQuantumTx) data() []byte                { return tx.Data }
func (tx *PostQuantumTx) gas() uint64                 { return tx.Gas }
func (tx *PostQuantumTx) gasPrice() *uint256.Int      { return tx.GasFeeCap }
func (tx *PostQuantumTx) gasTipCap() *uint256.Int     { return tx.GasTipCap }
func (tx *PostQuantumTx) gasFeeCap() *uint256.Int     { return tx.GasFeeCap }
func (tx *PostQuantumTx) value() *uint256.Int         { return tx.Value }
func (tx *PostQuantumTx) nonce() uint64               { return tx.Nonce }
func (tx *PostQuantumTx) to() *types.Address          { return tx.To }
func (tx *PostQuantumTx) from() *types.Address        { return nil } // Computed from PQ signature
func (tx *PostQuantumTx) sign() []byte                { return tx.PQSignature }

// hash computes the transaction hash for signing and identification
func (tx *PostQuantumTx) hash() types.Hash {
	return hash.PrefixedRlpHash(PostQuantumTxType, []interface{}{
		tx.ChainID,
		tx.Nonce,
		tx.GasTipCap,
		tx.GasFeeCap,
		tx.Gas,
		tx.To,
		tx.Value,
		tx.Data,
		tx.AccessList,
		tx.SigAlgo,
		tx.PubKeyMode,
		tx.PubKeyData,
		tx.PQSignature,
	})
}

// rawSignatureValues returns the legacy signature values (unused for PQ)
func (tx *PostQuantumTx) rawSignatureValues() (v, r, s *uint256.Int) {
	return tx.V, tx.R, tx.S
}

// setSignatureValues sets the legacy signature values
func (tx *PostQuantumTx) setSignatureValues(chainID, v, r, s *uint256.Int) {
	tx.ChainID = chainID
	tx.V = v
	tx.R = r
	tx.S = s
}

// SigningHash returns the hash to be signed (without signature).
func (tx *PostQuantumTx) SigningHash() types.Hash {
	return hash.PrefixedRlpHash(PostQuantumTxType, []interface{}{
		tx.ChainID,
		tx.Nonce,
		tx.GasTipCap,
		tx.GasFeeCap,
		tx.Gas,
		tx.To,
		tx.Value,
		tx.Data,
		tx.AccessList,
		tx.SigAlgo,
		tx.PubKeyMode,
		tx.PubKeyData,
		// Note: PQSignature is NOT included in signing hash
	})
}

// GetSigAlgo returns the signature algorithm
func (tx *PostQuantumTx) GetSigAlgo() uint8 {
	return tx.SigAlgo
}

// GetSigAlgoName returns the human-readable name of the signature algorithm
func (tx *PostQuantumTx) GetSigAlgoName() string {
	switch tx.SigAlgo {
	case PQAlgoFalcon512:
		return "Falcon-512"
	case PQAlgoSQIsign:
		return "SQIsign-I"
	case PQAlgoDilithium2:
		return "Dilithium2"
	case PQAlgoDilithium3:
		return "Dilithium3"
	default:
		return "Unknown"
	}
}

// GetPubKeyMode returns the public key mode
func (tx *PostQuantumTx) GetPubKeyMode() uint8 {
	return tx.PubKeyMode
}

// IsFullPubKey returns true if the transaction contains a full public key
func (tx *PostQuantumTx) IsFullPubKey() bool {
	return tx.PubKeyMode == 0
}

// GetPubKeyData returns the public key data (full key or hash)
func (tx *PostQuantumTx) GetPubKeyData() []byte {
	return tx.PubKeyData
}

// GetPubKeyHash returns the public key hash
// If the transaction contains a full public key, this computes the hash
// If the transaction contains a hash reference, this returns it directly
func (tx *PostQuantumTx) GetPubKeyHash() types.Hash {
	if tx.PubKeyMode == 1 && len(tx.PubKeyData) == PQPublicKeyHashSize {
		// Already a hash
		var h types.Hash
		copy(h[:], tx.PubKeyData)
		return h
	}
	// Compute hash from full public key
	return hash.Hash(tx.PubKeyData)
}

// GetPQSignature returns the post-quantum signature
func (tx *PostQuantumTx) GetPQSignature() []byte {
	return tx.PQSignature
}

// SetPQSignature sets the post-quantum signature
func (tx *PostQuantumTx) SetPQSignature(sig []byte) {
	tx.PQSignature = make([]byte, len(sig))
	copy(tx.PQSignature, sig)
}

// SetPubKey sets the public key data
func (tx *PostQuantumTx) SetPubKey(pubKey []byte, useHash bool) {
	if useHash {
		tx.PubKeyMode = 1
		h := hash.Hash(pubKey)
		tx.PubKeyData = h[:]
	} else {
		tx.PubKeyMode = 0
		tx.PubKeyData = make([]byte, len(pubKey))
		copy(tx.PubKeyData, pubKey)
	}
}

// GetExpectedPubKeySize returns the expected public key size for the algorithm
func (tx *PostQuantumTx) GetExpectedPubKeySize() int {
	return GetExpectedPubKeySize(tx.SigAlgo)
}

// GetExpectedSignatureSize returns the expected signature size for the algorithm
func (tx *PostQuantumTx) GetExpectedSignatureSize() int {
	return GetExpectedSignatureSize(tx.SigAlgo)
}

// EstimatedSize returns the estimated transaction size in bytes
func (tx *PostQuantumTx) EstimatedSize() int {
	baseSize := 200 // Base transaction fields

	// Add public key size
	if tx.PubKeyMode == 0 {
		baseSize += tx.GetExpectedPubKeySize()
	} else {
		baseSize += PQPublicKeyHashSize
	}

	// Add signature size
	baseSize += tx.GetExpectedSignatureSize()

	// Add data size
	baseSize += len(tx.Data)

	// Add access list size (approximate)
	for _, tuple := range tx.AccessList {
		baseSize += 20 + len(tuple.StorageKeys)*32
	}

	return baseSize
}

var (
	// ErrInvalidPQAlgorithm is returned when the signature algorithm is unknown
	ErrInvalidPQAlgorithm = errors.New("pq: invalid signature algorithm")

	// ErrInvalidPQSignature is returned when the PQ signature is invalid
	ErrInvalidPQSignature = errors.New("pq: invalid signature")

	// ErrInvalidPQPublicKey is returned when the PQ public key is invalid
	ErrInvalidPQPublicKey = errors.New("pq: invalid public key")

	// ErrPQPubKeyNotFound is returned when the public key cannot be found in registry
	ErrPQPubKeyNotFound = errors.New("pq: public key not found in registry")

	// ErrPQSignatureTooLarge is returned when the signature exceeds maximum size
	ErrPQSignatureTooLarge = errors.New("pq: signature too large")

	// ErrPQPubKeyMismatch is returned when the public key doesn't match the hash
	ErrPQPubKeyMismatch = errors.New("pq: public key hash mismatch")
)

// IsPostQuantum returns true if the transaction is a post-quantum transaction.
func (tx *Transaction) IsPostQuantum() bool {
	return tx.Type() == PostQuantumTxType
}

// GetPostQuantumTx returns the inner PostQuantumTx if this is a PQ transaction.
func (tx *Transaction) GetPostQuantumTx() *PostQuantumTx {
	if pqTx, ok := tx.inner.(*PostQuantumTx); ok {
		return pqTx
	}
	return nil
}

// PQSigAlgo returns the post-quantum signature algorithm (0 for non-PQ transactions).
func (tx *Transaction) PQSigAlgo() uint8 {
	if pqTx := tx.GetPostQuantumTx(); pqTx != nil {
		return pqTx.SigAlgo
	}
	return 0
}

// PQSignature returns the post-quantum signature (nil for non-PQ transactions).
func (tx *Transaction) PQSignature() []byte {
	if pqTx := tx.GetPostQuantumTx(); pqTx != nil {
		return pqTx.PQSignature
	}
	return nil
}

// PQPubKeyData returns the post-quantum public key data (nil for non-PQ transactions).
func (tx *Transaction) PQPubKeyData() []byte {
	if pqTx := tx.GetPostQuantumTx(); pqTx != nil {
		return pqTx.PubKeyData
	}
	return nil
}

func NewPostQuantumTx(
	chainID *uint256.Int,
	nonce uint64,
	to *types.Address,
	value *uint256.Int,
	gas uint64,
	gasTipCap *uint256.Int,
	gasFeeCap *uint256.Int,
	data []byte,
	accessList AccessList,
	sigAlgo uint8,
) *PostQuantumTx {
	return &PostQuantumTx{
		ChainID:    chainID,
		Nonce:      nonce,
		GasTipCap:  gasTipCap,
		GasFeeCap:  gasFeeCap,
		Gas:        gas,
		To:         to,
		Value:      value,
		Data:       data,
		AccessList: accessList,
		SigAlgo:    sigAlgo,
		PubKeyMode: 0,
		V:          new(uint256.Int),
		R:          new(uint256.Int),
		S:          new(uint256.Int),
	}
}
