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

package account

import (
	"fmt"
	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/api/protocol/state"
	"github.com/n42blockchain/N42/common/crypto"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/utils"
	"google.golang.org/protobuf/proto"
)

// Account is the Ethereum consensus representation of accounts.
// These objects are stored in the main account trie.
// DESCRIBED: docs/programmers_guide/guide.md#ethereum-state
type StateAccount struct {
	Initialised bool
	Nonce       uint64
	Balance     uint256.Int
	Root        types.Hash
	CodeHash    types.Hash // hash of the bytecode
	Incarnation uint16
}

const (
	MimetypeDataWithValidator = "data/validator"
	MimetypeTypedData         = "data/typed"
	MimetypeClique            = "application/x-clique-header"
	MimetypeParlia            = "application/x-parlia-header"
	MimetypeBor               = "application/x-bor-header"
	MimetypeTextPlain         = "text/plain"
)

var emptyCodeHash = crypto.Keccak256Hash(nil)

var emptyRoot = types.BytesHash(crypto.Keccak256(nil))

// NewAccount creates a new account w/o code nor storage.
func NewAccount() StateAccount {
	return StateAccount{
		Root:     emptyRoot,
		CodeHash: emptyCodeHash,
	}
}

func (a *StateAccount) EncodingLengthForStorage() uint {
	pb := a.ToProtoMessage()
	return uint(proto.Size(pb))
}

func (a *StateAccount) EncodeForStorage(buffer []byte) {
	pb := a.ToProtoMessage()
	data, err := proto.Marshal(pb)
	if err != nil {
		return
	}
	copy(buffer, data)
}

// Copy makes `a` a full, independent (meaning that if the `image` changes in any way, it does not affect `a`) copy of the account `image`.
func (a *StateAccount) Copy(image *StateAccount) {
	a.Initialised = image.Initialised
	a.Nonce = image.Nonce
	a.Balance.Set(&image.Balance)
	copy(a.Root[:], image.Root[:])
	copy(a.CodeHash[:], image.CodeHash[:])
	a.Incarnation = image.Incarnation
}

func (a *StateAccount) Reset() {
	a.Initialised = true
	a.Nonce = 0
	a.Incarnation = 0
	a.Balance.Clear()
	copy(a.Root[:], emptyRoot[:])
	copy(a.CodeHash[:], emptyCodeHash[:])
}

func (a *StateAccount) DecodeForStorage(enc []byte) error {
	a.Reset()
	if len(enc) == 0 {
		return nil
	}
	return a.Unmarshal(enc)
}
func bytesToUint64(buf []byte) (x uint64) {
	for i, b := range buf {
		x = x<<8 + uint64(b)
		if i == 7 {
			return
		}
	}
	return
}

func (a *StateAccount) SelfCopy() *StateAccount {
	newAcc := NewAccount()
	newAcc.Copy(a)
	return &newAcc
}

func (a *StateAccount) IsEmptyCodeHash() bool {
	return IsEmptyCodeHash(a.CodeHash)
}

func IsEmptyCodeHash(codeHash types.Hash) bool {
	return codeHash == emptyCodeHash || codeHash == (types.Hash{})
}

func (a *StateAccount) IsEmptyRoot() bool {
	return a.Root == emptyRoot || a.Root == types.Hash{}
}

func (a *StateAccount) GetIncarnation() uint16 {
	return a.Incarnation
}

func (a *StateAccount) SetIncarnation(v uint16) {
	a.Incarnation = v
}

func (a *StateAccount) Equals(acc *StateAccount) bool {
	return a.Nonce == acc.Nonce &&
		a.CodeHash == acc.CodeHash &&
		a.Balance.Cmp(&acc.Balance) == 0 &&
		a.Incarnation == acc.Incarnation
}

func (a *StateAccount) Marshal() ([]byte, error) {
	protoMsg := a.ToProtoMessage()
	v, err := proto.Marshal(protoMsg)
	if err != nil {
		return nil, err
	}
	return v, nil
}

func (a *StateAccount) Unmarshal(v []byte) error {
	var pAccount state.Account
	if err := proto.Unmarshal(v, &pAccount); err != nil {
		return err
	}
	a.Initialised = pAccount.Initialised
	a.Nonce = pAccount.Nonce
	a.Balance = *utils.ConvertH256ToUint256Int(pAccount.Balance)
	a.Root = utils.ConvertH256ToHash(pAccount.Root)
	a.CodeHash = utils.ConvertH256ToHash(pAccount.CodeHash)
	a.Incarnation = uint16(pAccount.Incarnation)
	return nil
}

func (a *StateAccount) ToProtoMessage() proto.Message {
	return &state.Account{
		Initialised: a.Initialised,
		Nonce:       a.Nonce,
		Balance:     utils.ConvertUint256IntToH256(&a.Balance),
		Root:        utils.ConvertHashToH256(a.Root),
		CodeHash:    utils.ConvertHashToH256(a.CodeHash),
		Incarnation: uint64(a.Incarnation),
	}
}

func (a *StateAccount) FromProtoMessage(msg proto.Message) error {
	pAccount, ok := msg.(*state.Account)
	if !ok {
		return fmt.Errorf("impossible type assert ")
	}

	a.Initialised = pAccount.Initialised
	a.Nonce = pAccount.Nonce
	a.Balance = *utils.ConvertH256ToUint256Int(pAccount.Balance)
	a.Root = utils.ConvertH256ToHash(pAccount.Root)
	a.CodeHash = utils.ConvertH256ToHash(pAccount.CodeHash)
	a.Incarnation = uint16(pAccount.Incarnation)
	return nil
}
