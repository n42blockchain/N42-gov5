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

// EIP-7702: Set EOA account code
// https://eips.ethereum.org/EIPS/eip-7702
// Part of the Pectra upgrade

package transaction

import (
	"bytes"
	"errors"
	"math/big"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/crypto"
	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/rlp"
	"github.com/n42blockchain/N42/common/types"
)

// SetCodeTxType is the transaction type for EIP-7702 SetCode transactions
const SetCodeTxType = 0x04

// EIP-7702 magic bytes for authorization signatures
var (
	// Magic byte for EIP-7702 authorization signature (0x05)
	AuthorizationMagic = byte(0x05)

	// ErrInvalidAuthorizationSignature is returned when the authorization signature is invalid
	ErrInvalidAuthorizationSignature = errors.New("invalid authorization signature")

	// ErrInvalidAuthorizationChainID is returned when the authorization chain ID doesn't match
	ErrInvalidAuthorizationChainID = errors.New("authorization chain ID mismatch")

	// ErrAuthorizationNonceMismatch is returned when the authorization nonce doesn't match
	ErrAuthorizationNonceMismatch = errors.New("authorization nonce mismatch")

	// ErrEmptyAuthorizationList is returned when the authorization list is empty
	ErrEmptyAuthorizationList = errors.New("empty authorization list")
)

// Authorization represents an EIP-7702 authorization tuple.
// It allows an EOA to temporarily delegate its code to a contract address.
type Authorization struct {
	ChainID uint64         `json:"chainId"` // Chain ID of the authorization
	Address types.Address  `json:"address"` // Contract address to delegate to
	Nonce   uint64         `json:"nonce"`   // Nonce of the authorizing account
	V       *uint256.Int   `json:"v"`       // Signature V value
	R       *uint256.Int   `json:"r"`       // Signature R value
	S       *uint256.Int   `json:"s"`       // Signature S value
}

// Copy creates a deep copy of the authorization
func (auth *Authorization) Copy() *Authorization {
	cpy := &Authorization{
		ChainID: auth.ChainID,
		Address: auth.Address,
		Nonce:   auth.Nonce,
	}
	if auth.V != nil {
		cpy.V = new(uint256.Int).Set(auth.V)
	}
	if auth.R != nil {
		cpy.R = new(uint256.Int).Set(auth.R)
	}
	if auth.S != nil {
		cpy.S = new(uint256.Int).Set(auth.S)
	}
	return cpy
}

// SigningHash returns the hash to be signed for this authorization
func (auth *Authorization) SigningHash() types.Hash {
	return hash.PrefixedRlpHash(
		AuthorizationMagic,
		[]interface{}{auth.ChainID, auth.Address, auth.Nonce},
	)
}

// RecoverSigner recovers the signer address from the authorization signature
func (auth *Authorization) RecoverSigner() (types.Address, error) {
	if auth.V == nil || auth.R == nil || auth.S == nil {
		return types.Address{}, ErrInvalidAuthorizationSignature
	}

	// Create signature bytes
	sig := make([]byte, 65)
	auth.R.WriteToSlice(sig[0:32])
	auth.S.WriteToSlice(sig[32:64])

	// Convert V to recovery ID (0 or 1)
	v := auth.V.Uint64()
	if v >= 27 {
		v -= 27
	}
	sig[64] = byte(v)

	// Recover public key
	signingHash := auth.SigningHash()
	pub, err := crypto.Ecrecover(signingHash[:], sig)
	if err != nil {
		return types.Address{}, err
	}
	if len(pub) == 0 || pub[0] != 4 {
		return types.Address{}, ErrInvalidAuthorizationSignature
	}

	// Convert public key bytes to address (skip the 0x04 prefix)
	var addr types.Address
	copy(addr[:], crypto.Keccak256(pub[1:])[12:])
	return addr, nil
}

// AuthorizationList is a list of authorizations
type AuthorizationList []*Authorization

// Copy creates a deep copy of the authorization list
func (al AuthorizationList) Copy() AuthorizationList {
	if al == nil {
		return nil
	}
	cpy := make(AuthorizationList, len(al))
	for i, auth := range al {
		cpy[i] = auth.Copy()
	}
	return cpy
}

// copyAccessList creates a deep copy of AccessList
func copyAccessList(al AccessList) AccessList {
	if al == nil {
		return nil
	}
	cpy := make(AccessList, len(al))
	for i, tuple := range al {
		cpy[i].Address = tuple.Address
		if tuple.StorageKeys != nil {
			cpy[i].StorageKeys = make([]types.Hash, len(tuple.StorageKeys))
			copy(cpy[i].StorageKeys, tuple.StorageKeys)
		}
	}
	return cpy
}

// SetCodeTx represents an EIP-7702 SetCode transaction.
// This transaction type allows EOAs to temporarily set their code to a contract address.
type SetCodeTx struct {
	ChainID    *uint256.Int      // Chain ID
	Nonce      uint64            // Transaction nonce
	GasTipCap  *uint256.Int      // Max priority fee per gas (aka tip)
	GasFeeCap  *uint256.Int      // Max fee per gas
	Gas        uint64            // Gas limit
	To         *types.Address    // Contract address to call (nil for contract creation)
	Value      *uint256.Int      // Transfer value
	Data       []byte            // Call data
	AccessList AccessList        // Access list
	AuthList   AuthorizationList // Authorization list for EIP-7702

	// Signature values
	V *uint256.Int
	R *uint256.Int
	S *uint256.Int

	// Derived fields (cached)
	txHash     types.Hash
	fromCache  *types.Address
}

// txType returns the transaction type
func (tx *SetCodeTx) txType() byte { return SetCodeTxType }

// copy creates a deep copy of the transaction
func (tx *SetCodeTx) copy() TxData {
	cpy := &SetCodeTx{
		Nonce:      tx.Nonce,
		Gas:        tx.Gas,
		Data:       make([]byte, len(tx.Data)),
		AccessList: copyAccessList(tx.AccessList),
		AuthList:   tx.AuthList.Copy(),
	}
	copy(cpy.Data, tx.Data)

	if tx.ChainID != nil {
		cpy.ChainID = new(uint256.Int).Set(tx.ChainID)
	}
	if tx.GasTipCap != nil {
		cpy.GasTipCap = new(uint256.Int).Set(tx.GasTipCap)
	}
	if tx.GasFeeCap != nil {
		cpy.GasFeeCap = new(uint256.Int).Set(tx.GasFeeCap)
	}
	if tx.Value != nil {
		cpy.Value = new(uint256.Int).Set(tx.Value)
	}
	if tx.To != nil {
		to := *tx.To
		cpy.To = &to
	}
	if tx.V != nil {
		cpy.V = new(uint256.Int).Set(tx.V)
	}
	if tx.R != nil {
		cpy.R = new(uint256.Int).Set(tx.R)
	}
	if tx.S != nil {
		cpy.S = new(uint256.Int).Set(tx.S)
	}
	return cpy
}

// accessors for inner transaction data
func (tx *SetCodeTx) chainID() *uint256.Int   { return tx.ChainID }
func (tx *SetCodeTx) data() []byte            { return tx.Data }
func (tx *SetCodeTx) gas() uint64             { return tx.Gas }
func (tx *SetCodeTx) gasPrice() *uint256.Int  { return tx.GasFeeCap }
func (tx *SetCodeTx) gasTipCap() *uint256.Int { return tx.GasTipCap }
func (tx *SetCodeTx) gasFeeCap() *uint256.Int { return tx.GasFeeCap }
func (tx *SetCodeTx) value() *uint256.Int     { return tx.Value }
func (tx *SetCodeTx) nonce() uint64           { return tx.Nonce }
func (tx *SetCodeTx) to() *types.Address      { return tx.To }
func (tx *SetCodeTx) from() *types.Address    { return tx.fromCache }
func (tx *SetCodeTx) sign() []byte            { return nil }
func (tx *SetCodeTx) accessList() AccessList  { return tx.AccessList }

// authList returns the authorization list
func (tx *SetCodeTx) authList() AuthorizationList { return tx.AuthList }

func (tx *SetCodeTx) rawSignatureValues() (v, r, s *uint256.Int) {
	return tx.V, tx.R, tx.S
}

func (tx *SetCodeTx) setSignatureValues(chainID, v, r, s *uint256.Int) {
	tx.ChainID, tx.V, tx.R, tx.S = chainID, v, r, s
}

// hash returns the hash of the transaction
func (tx *SetCodeTx) hash() types.Hash {
	if tx.txHash != (types.Hash{}) {
		return tx.txHash
	}
	tx.txHash = hash.PrefixedRlpHash(SetCodeTxType, []interface{}{
		tx.ChainID,
		tx.Nonce,
		tx.GasTipCap,
		tx.GasFeeCap,
		tx.Gas,
		tx.To,
		tx.Value,
		tx.Data,
		tx.AccessList,
		tx.AuthList,
		tx.V, tx.R, tx.S,
	})
	return tx.txHash
}

// signingHash returns the hash to be signed
func (tx *SetCodeTx) signingHash(chainID *big.Int) types.Hash {
	return hash.PrefixedRlpHash(SetCodeTxType, []interface{}{
		chainID,
		tx.Nonce,
		tx.GasTipCap,
		tx.GasFeeCap,
		tx.Gas,
		tx.To,
		tx.Value,
		tx.Data,
		tx.AccessList,
		tx.AuthList,
	})
}

// EncodeRLP implements rlp.Encoder
func (tx *SetCodeTx) EncodeRLP() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte(SetCodeTxType)
	if err := rlp.Encode(&buf, []interface{}{
		tx.ChainID,
		tx.Nonce,
		tx.GasTipCap,
		tx.GasFeeCap,
		tx.Gas,
		tx.To,
		tx.Value,
		tx.Data,
		tx.AccessList,
		tx.AuthList,
		tx.V, tx.R, tx.S,
	}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// DelegationPrefix is the prefix used to identify delegated accounts (EIP-7702)
// An account with code starting with this prefix is considered delegated
var DelegationPrefix = []byte{0xef, 0x01, 0x00}

// ParseDelegation attempts to parse a delegation from account code.
// Returns the delegated address and true if successful, empty address and false otherwise.
func ParseDelegation(code []byte) (types.Address, bool) {
	if len(code) != 23 { // 3 bytes prefix + 20 bytes address
		return types.Address{}, false
	}
	if !bytes.HasPrefix(code, DelegationPrefix) {
		return types.Address{}, false
	}
	return types.BytesToAddress(code[3:23]), true
}

// AddressToDelegation converts an address to delegation code (EIP-7702)
func AddressToDelegation(addr types.Address) []byte {
	code := make([]byte, 23)
	copy(code, DelegationPrefix)
	copy(code[3:], addr[:])
	return code
}

