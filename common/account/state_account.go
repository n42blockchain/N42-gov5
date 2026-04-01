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
	"math/bits"

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
	if len(enc) == 0 {
		a.Reset()
		return nil
	}
	// Auto-detect: V2 uses low 4 bits of fieldBits (max 0x0F).
	// Protobuf field tags >= 0x10 for fields 2+.
	if enc[0] > 0x0F {
		return a.Unmarshal(enc)
	}
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
	n := a.EncodingLengthForStorageV2()
	buf := make([]byte, n)
	a.EncodeForStorageV2(buf)
	return buf
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
		if v > 0xFFFF {
			return fmt.Errorf("incarnation %d exceeds uint16 range", v)
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

// EncodingLengthForHashing returns the RLP-encoded length of the account
// for trie hashing (nonce, balance, root, codeHash — no incarnation).
func (a *StateAccount) EncodingLengthForHashing() uint {
	balanceBytes := 0
	if !a.Balance.LtUint64(128) {
		balanceBytes = a.Balance.ByteLen()
	}
	nonceBytes := intLenExcludingHead(a.Nonce)
	structLength := balanceBytes + nonceBytes + 2
	structLength += 66 // Two 32-byte arrays (root + codeHash) + 2 length prefixes
	return uint(rlpListPrefixLen(structLength) + structLength)
}

// EncodeForHashing writes the RLP encoding of the account for trie hashing.
// Format: RLP([nonce, balance, root, codeHash])
func (a *StateAccount) EncodeForHashing(buffer []byte) {
	balanceBytes := 0
	if !a.Balance.LtUint64(128) {
		balanceBytes = a.Balance.ByteLen()
	}
	nonceBytes := intLenExcludingHead(a.Nonce)
	var structLength = uint(balanceBytes + nonceBytes + 2)
	structLength += 66

	var pos int
	if structLength < 56 {
		buffer[0] = byte(192 + structLength)
		pos = 1
	} else {
		lengthBytes := (bits.Len(structLength) + 7) / 8
		buffer[0] = byte(247 + lengthBytes)
		for i := lengthBytes; i > 0; i-- {
			buffer[i] = byte(structLength)
			structLength >>= 8
		}
		pos = lengthBytes + 1
	}

	// Nonce
	if a.Nonce < 128 && a.Nonce != 0 {
		buffer[pos] = byte(a.Nonce)
	} else {
		buffer[pos] = byte(128 + nonceBytes)
		var nonce = a.Nonce
		for i := nonceBytes; i > 0; i-- {
			buffer[pos+i] = byte(nonce)
			nonce >>= 8
		}
	}
	pos += 1 + nonceBytes

	// Balance
	if a.Balance.LtUint64(128) && !a.Balance.IsZero() {
		buffer[pos] = byte(a.Balance.Uint64())
		pos++
	} else {
		buffer[pos] = byte(128 + balanceBytes)
		pos++
		a.Balance.WriteToSlice(buffer[pos : pos+balanceBytes])
		pos += balanceBytes
	}

	// Root
	buffer[pos] = 128 + 32
	pos++
	copy(buffer[pos:], a.Root[:])
	pos += 32

	// CodeHash
	buffer[pos] = 128 + 32
	pos++
	copy(buffer[pos:], a.CodeHash[:])
}

func intLenExcludingHead(i uint64) int {
	if i < 0x80 {
		return 0
	}
	return (bits.Len64(i) + 7) / 8
}

func rlpListPrefixLen(contentLen int) int {
	if contentLen < 56 {
		return 1
	}
	return 1 + (bits.Len(uint(contentLen))+7)/8
}

func uvarintSize(v uint64) int {
	n := 1
	for v >= 0x80 {
		v >>= 7
		n++
	}
	return n
}

