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
	"encoding/binary"
	"fmt"

	"github.com/holiman/uint256"
	"google.golang.org/protobuf/proto"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/common/utils"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/proto/state"
)

// StateAccount is the Ethereum consensus representation of accounts.
// These objects are stored in the main account trie.
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

// NewAccount creates a new account without code or storage.
func NewAccount() StateAccount {
	return StateAccount{
		Root:     emptyRoot,
		CodeHash: emptyCodeHash,
	}
}

func (a *StateAccount) EncodingLengthForStorage() uint {
	return uint(a.EncodingLengthForStorageV2())
}

func (a *StateAccount) EncodeForStorage(buffer []byte) {
	a.EncodeForStorageV2(buffer)
}

// Copy makes a a full, independent deep copy of image.
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
	return a.DecodeForStorageV2(enc)
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
	return proto.Marshal(a.ToProtoMessage())
}

func (a *StateAccount) Unmarshal(v []byte) error {
	var pAccount state.Account
	if err := proto.Unmarshal(v, &pAccount); err != nil {
		return err
	}
	a.applyProtoFields(&pAccount)
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
		return fmt.Errorf("expected *state.Account, got %T", msg)
	}
	a.applyProtoFields(pAccount)
	return nil
}

// applyProtoFields populates the StateAccount fields from a protobuf Account.
func (a *StateAccount) applyProtoFields(pAccount *state.Account) {
	a.Initialised = pAccount.Initialised
	a.Nonce = pAccount.Nonce
	a.Balance = *utils.ConvertH256ToUint256Int(pAccount.Balance)
	a.Root = utils.ConvertH256ToHash(pAccount.Root)
	a.CodeHash = utils.ConvertH256ToHash(pAccount.CodeHash)
	a.Incarnation = uint16(pAccount.Incarnation)
}

// MarshalV2 encodes a StateAccount into a new byte slice using V2 format.
// Convenience method that combines length calculation and encoding in one call.
func (a *StateAccount) MarshalV2() []byte {
	buf := make([]byte, 74) // max possible: 1 + 10 + 33 + 10 + 32 = 86, 74 is tight upper
	n := a.EncodeForStorageV2(buf)
	return buf[:n]
}

// EncodeForStorageV2 encodes using Erigon-style variable-length format.
// Format: [fieldBits:1B][nonce:varint][balance:lenB+data][incarnation:varint][codeHash:32B]
// Fields with default values are omitted. Empty account = 1 byte.
func (a *StateAccount) EncodeForStorageV2(buf []byte) int {
	var fieldBits byte
	pos := 1

	if a.Nonce > 0 {
		fieldBits |= 1
		n := binary.PutUvarint(buf[pos:], a.Nonce)
		pos += n
	}
	if !a.Balance.IsZero() {
		fieldBits |= 2
		balBytes := a.Balance.Bytes32()
		start := 0
		for start < 31 && balBytes[start] == 0 {
			start++
		}
		trimLen := 32 - start
		buf[pos] = byte(trimLen)
		pos++
		copy(buf[pos:pos+trimLen], balBytes[start:])
		pos += trimLen
	}
	if a.Incarnation > 0 {
		fieldBits |= 4
		n := binary.PutUvarint(buf[pos:], uint64(a.Incarnation))
		pos += n
	}
	if !IsEmptyCodeHash(a.CodeHash) {
		fieldBits |= 8
		copy(buf[pos:pos+32], a.CodeHash[:])
		pos += 32
	}
	buf[0] = fieldBits
	return pos
}

// EncodingLengthForStorageV2 returns the encoded length without writing.
func (a *StateAccount) EncodingLengthForStorageV2() int {
	n := 1 // fieldBits
	if a.Nonce > 0 {
		n += uvarintSize(a.Nonce)
	}
	if !a.Balance.IsZero() {
		balBytes := a.Balance.Bytes32()
		start := 0
		for start < 31 && balBytes[start] == 0 {
			start++
		}
		n += 1 + (32 - start)
	}
	if a.Incarnation > 0 {
		n += uvarintSize(uint64(a.Incarnation))
	}
	if !IsEmptyCodeHash(a.CodeHash) {
		n += 32
	}
	return n
}

// DecodeForStorageV2 decodes a V2-encoded account.
func (a *StateAccount) DecodeForStorageV2(enc []byte) error {
	a.Reset()
	if len(enc) == 0 {
		return nil
	}
	fieldBits := enc[0]
	pos := 1

	if fieldBits&1 != 0 {
		v, n := binary.Uvarint(enc[pos:])
		if n <= 0 {
			return fmt.Errorf("malformed nonce varint")
		}
		a.Nonce = v
		pos += n
	}
	if fieldBits&2 != 0 {
		if pos >= len(enc) {
			return fmt.Errorf("truncated balance length")
		}
		balLen := int(enc[pos])
		pos++
		if balLen > 32 || pos+balLen > len(enc) {
			return fmt.Errorf("truncated balance data")
		}
		var balBytes [32]byte
		copy(balBytes[32-balLen:], enc[pos:pos+balLen])
		a.Balance.SetBytes32(balBytes[:])
		pos += balLen
	}
	if fieldBits&4 != 0 {
		v, n := binary.Uvarint(enc[pos:])
		if n <= 0 {
			return fmt.Errorf("malformed incarnation varint")
		}
		a.Incarnation = uint16(v)
		pos += n
	}
	if fieldBits&8 != 0 {
		if pos+32 > len(enc) {
			return fmt.Errorf("truncated codeHash")
		}
		copy(a.CodeHash[:], enc[pos:pos+32])
		pos += 32
	}
	a.Initialised = true
	return nil
}

func uvarintSize(v uint64) int {
	n := 1
	for v >= 0x80 {
		v >>= 7
		n++
	}
	return n
}
