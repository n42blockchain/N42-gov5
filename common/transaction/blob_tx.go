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

// EIP-4844: Shard Blob Transactions
// Reference: https://eips.ethereum.org/EIPS/eip-4844
// Implementation based on go-ethereum and erigon

package transaction

import (
	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/types"
)

// BlobTxType is the transaction type for EIP-4844 blob transactions
const BlobTxType = 0x03

// =============================================================================
// Blob Constants (EIP-4844)
// =============================================================================

const (
	// BlobTxBlobGasPerBlob is the gas consumed per blob
	BlobTxBlobGasPerBlob = 1 << 17 // 131072

	// BlobTxMinBlobGasprice is the minimum blob gas price
	BlobTxMinBlobGasprice = 1

	// BlobTxBlobGaspriceUpdateFraction is the update fraction for blob gas price
	BlobTxBlobGaspriceUpdateFraction = 3338477

	// BlobTxTargetBlobGasPerBlock is the target blob gas per block
	BlobTxTargetBlobGasPerBlock = 3 * BlobTxBlobGasPerBlob // 393216

	// MaxBlobGasPerBlock is the maximum blob gas allowed per block
	MaxBlobGasPerBlock = 6 * BlobTxBlobGasPerBlob // 786432

	// MaxBlobsPerBlock is the maximum number of blobs per block
	MaxBlobsPerBlock = MaxBlobGasPerBlock / BlobTxBlobGasPerBlob // 6

	// BlobSize is the size of a blob in bytes (128 KB)
	BlobSize = 4096 * 32 // 131072 bytes

	// FieldElementsPerBlob is the number of field elements per blob
	FieldElementsPerBlob = 4096

	// VersionedHashVersionKZG is the version byte for KZG commitment hashes
	VersionedHashVersionKZG = 0x01
)

// =============================================================================
// BlobTx Structure
// =============================================================================

// BlobTx represents an EIP-4844 blob transaction
type BlobTx struct {
	ChainID    *uint256.Int   // Chain ID
	Nonce      uint64         // Sender nonce
	GasTipCap  *uint256.Int   // Max priority fee per gas (EIP-1559)
	GasFeeCap  *uint256.Int   // Max fee per gas (EIP-1559)
	Gas        uint64         // Gas limit
	To         types.Address  // Recipient (cannot be nil for blob tx)
	Value      *uint256.Int   // Wei amount
	Data       []byte         // Call data
	AccessList AccessList     // EIP-2930 access list
	
	// EIP-4844 specific fields
	BlobFeeCap *uint256.Int   // Max fee per blob gas
	BlobHashes []types.Hash   // Versioned blob hashes

	// Signature values
	V *uint256.Int
	R *uint256.Int
	S *uint256.Int

	// Sidecar (optional, for transaction propagation)
	Sidecar *BlobTxSidecar `rlp:"-"` // Not RLP encoded in network messages
}

// BlobTxSidecar contains the blobs and their proofs for a blob transaction
type BlobTxSidecar struct {
	Blobs       []Blob       // Actual blob data
	Commitments []Commitment // KZG commitments
	Proofs      []Proof      // KZG proofs
}

// Blob represents a blob of data (128 KB)
type Blob [BlobSize]byte

// Commitment represents a KZG commitment (48 bytes)
type Commitment [48]byte

// Proof represents a KZG proof (48 bytes)
type Proof [48]byte

// =============================================================================
// BlobTx TxData Interface Implementation
// =============================================================================

func (tx *BlobTx) txType() byte { return BlobTxType }

func (tx *BlobTx) copy() TxData {
	cpy := &BlobTx{
		Nonce: tx.Nonce,
		To:    tx.To,
		Data:  make([]byte, len(tx.Data)),
		Gas:   tx.Gas,
		// Initialize uint256 fields
		ChainID:    new(uint256.Int),
		GasTipCap:  new(uint256.Int),
		GasFeeCap:  new(uint256.Int),
		Value:      new(uint256.Int),
		BlobFeeCap: new(uint256.Int),
		V:          new(uint256.Int),
		R:          new(uint256.Int),
		S:          new(uint256.Int),
		// Copy access list
		AccessList: copyAccessList(tx.AccessList),
		// Copy blob hashes
		BlobHashes: make([]types.Hash, len(tx.BlobHashes)),
	}

	copy(cpy.Data, tx.Data)
	copy(cpy.BlobHashes, tx.BlobHashes)

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
	if tx.BlobFeeCap != nil {
		cpy.BlobFeeCap.Set(tx.BlobFeeCap)
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

	// Copy sidecar if present
	if tx.Sidecar != nil {
		cpy.Sidecar = tx.Sidecar.Copy()
	}

	return cpy
}

func (tx *BlobTx) chainID() *uint256.Int   { return tx.ChainID }
func (tx *BlobTx) accessList() AccessList  { return tx.AccessList }
func (tx *BlobTx) data() []byte            { return tx.Data }
func (tx *BlobTx) gas() uint64             { return tx.Gas }
func (tx *BlobTx) gasPrice() *uint256.Int  { return tx.GasFeeCap }
func (tx *BlobTx) gasTipCap() *uint256.Int { return tx.GasTipCap }
func (tx *BlobTx) gasFeeCap() *uint256.Int { return tx.GasFeeCap }
func (tx *BlobTx) value() *uint256.Int     { return tx.Value }
func (tx *BlobTx) nonce() uint64           { return tx.Nonce }
func (tx *BlobTx) to() *types.Address      { return &tx.To }
func (tx *BlobTx) from() *types.Address    { return nil } // Computed from signature
func (tx *BlobTx) sign() []byte            { return nil }

func (tx *BlobTx) hash() types.Hash {
	return hash.PrefixedRlpHash(BlobTxType, []interface{}{
		tx.ChainID,
		tx.Nonce,
		tx.GasTipCap,
		tx.GasFeeCap,
		tx.Gas,
		tx.To,
		tx.Value,
		tx.Data,
		tx.AccessList,
		tx.BlobFeeCap,
		tx.BlobHashes,
		tx.V, tx.R, tx.S,
	})
}

func (tx *BlobTx) rawSignatureValues() (v, r, s *uint256.Int) {
	return tx.V, tx.R, tx.S
}

func (tx *BlobTx) setSignatureValues(chainID, v, r, s *uint256.Int) {
	tx.ChainID = chainID
	tx.V = v
	tx.R = r
	tx.S = s
}

// =============================================================================
// BlobTx Specific Methods
// =============================================================================

// GetBlobFeeCap returns the blob fee cap
func (tx *BlobTx) GetBlobFeeCap() *uint256.Int {
	return tx.BlobFeeCap
}

// GetBlobHashes returns the versioned blob hashes
func (tx *BlobTx) GetBlobHashes() []types.Hash {
	return tx.BlobHashes
}

// BlobGas returns the blob gas used by this transaction
func (tx *BlobTx) BlobGas() uint64 {
	return uint64(len(tx.BlobHashes)) * BlobTxBlobGasPerBlob
}

// GetSidecar returns the blob sidecar
func (tx *BlobTx) GetSidecar() *BlobTxSidecar {
	return tx.Sidecar
}

// SetSidecar sets the blob sidecar
func (tx *BlobTx) SetSidecar(sidecar *BlobTxSidecar) {
	tx.Sidecar = sidecar
}

// HasSidecar returns true if the transaction has a sidecar
func (tx *BlobTx) HasSidecar() bool {
	return tx.Sidecar != nil && len(tx.Sidecar.Blobs) > 0
}

// =============================================================================
// BlobTxSidecar Methods
// =============================================================================

// Copy creates a deep copy of the sidecar
func (s *BlobTxSidecar) Copy() *BlobTxSidecar {
	if s == nil {
		return nil
	}

	cpy := &BlobTxSidecar{
		Blobs:       make([]Blob, len(s.Blobs)),
		Commitments: make([]Commitment, len(s.Commitments)),
		Proofs:      make([]Proof, len(s.Proofs)),
	}

	copy(cpy.Blobs, s.Blobs)
	copy(cpy.Commitments, s.Commitments)
	copy(cpy.Proofs, s.Proofs)

	return cpy
}

// BlobCount returns the number of blobs in the sidecar
func (s *BlobTxSidecar) BlobCount() int {
	if s == nil {
		return 0
	}
	return len(s.Blobs)
}

// BlobGas returns the total blob gas for the sidecar
func (s *BlobTxSidecar) BlobGas() uint64 {
	return uint64(s.BlobCount()) * BlobTxBlobGasPerBlob
}

// =============================================================================
// Versioned Hash Utilities
// =============================================================================

// KZGToVersionedHash converts a KZG commitment to a versioned hash
func KZGToVersionedHash(commitment Commitment) types.Hash {
	h := types.Hash{}
	h[0] = VersionedHashVersionKZG
	// Hash the commitment and copy bytes 1-31
	commitHash := hash.Hash(commitment[:])
	copy(h[1:], commitHash[1:])
	return h
}

// IsValidVersionedHash checks if a hash has the correct version byte
func IsValidVersionedHash(h types.Hash) bool {
	return h[0] == VersionedHashVersionKZG
}

// =============================================================================
// Blob Gas Price Calculation
// =============================================================================

// CalcBlobFee calculates the blob fee for a given excess blob gas
func CalcBlobFee(excessBlobGas uint64) *uint256.Int {
	return fakeExponential(
		uint256.NewInt(BlobTxMinBlobGasprice),
		uint256.NewInt(excessBlobGas),
		uint256.NewInt(BlobTxBlobGaspriceUpdateFraction),
	)
}

// CalcExcessBlobGas calculates the excess blob gas for a block
func CalcExcessBlobGas(parentExcessBlobGas, parentBlobGasUsed uint64) uint64 {
	excessBlobGas := parentExcessBlobGas + parentBlobGasUsed
	if excessBlobGas < BlobTxTargetBlobGasPerBlock {
		return 0
	}
	return excessBlobGas - BlobTxTargetBlobGasPerBlock
}

// VerifyBlobGas verifies that the blob gas used does not exceed the maximum
func VerifyBlobGas(blobGasUsed uint64) error {
	if blobGasUsed > MaxBlobGasPerBlock {
		return ErrBlobGasLimitExceeded
	}
	return nil
}

// fakeExponential approximates exp(numerator/denominator) * factor
// Used for blob gas price calculation
func fakeExponential(factor, numerator, denominator *uint256.Int) *uint256.Int {
	i := uint256.NewInt(1)
	output := uint256.NewInt(0)
	numeratorAccum := new(uint256.Int).Mul(factor, denominator)

	for {
		output.Add(output, numeratorAccum)

		// numeratorAccum = numeratorAccum * numerator / (denominator * i)
		numeratorAccum.Mul(numeratorAccum, numerator)
		numeratorAccum.Div(numeratorAccum, denominator)
		numeratorAccum.Div(numeratorAccum, i)

		i.AddUint64(i, 1)

		if numeratorAccum.IsZero() {
			break
		}
	}

	return output.Div(output, denominator)
}

// =============================================================================
// Errors
// =============================================================================

var (
	// ErrBlobGasLimitExceeded is returned when blob gas exceeds the limit
	ErrBlobGasLimitExceeded = &blobError{"blob gas limit exceeded"}

	// ErrBlobFeeCapTooLow is returned when blob fee cap is too low
	ErrBlobFeeCapTooLow = &blobError{"blob fee cap too low"}

	// ErrTooManyBlobs is returned when transaction has too many blobs
	ErrTooManyBlobs = &blobError{"too many blobs in transaction"}

	// ErrNoBlobs is returned when blob transaction has no blobs
	ErrNoBlobs = &blobError{"blob transaction has no blobs"}

	// ErrBlobHashMismatch is returned when blob hash doesn't match commitment
	ErrBlobHashMismatch = &blobError{"blob hash doesn't match commitment"}

	// ErrInvalidBlobProof is returned when blob proof is invalid
	ErrInvalidBlobProof = &blobError{"invalid blob proof"}

	// ErrBlobTxCreate is returned when blob tx tries to create a contract
	ErrBlobTxCreate = &blobError{"blob transaction cannot be contract creation"}

	// ErrBlobSidecarMissing is returned when blob sidecar is required but missing
	ErrBlobSidecarMissing = &blobError{"blob sidecar missing"}
)

type blobError struct {
	msg string
}

func (e *blobError) Error() string {
	return e.msg
}

