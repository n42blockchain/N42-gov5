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
// Ethereum-style 20-byte Address primitive. Defines AddressLength and
// the internal Address32Length alongside the
// nullAddress sentinel. Provides BytesToAddress / SetBytes, hex /
// checksum string conversions, JSON + SQL driver support and
// libp2p-crypto hook-ins. Core identifier used across txpool,
// state, RPC and P2P messaging.

package types

import (
	"bytes"
	"database/sql/driver"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/n42blockchain/N42/common/hexutil"
	"golang.org/x/crypto/sha3"
)

const (
	AddressLength   = 20
	Address32Length = 32
)

var (
	prefixAddress = "0X"
	nullAddress   = Address{0}
)

type Address [AddressLength]byte

func BytesToAddress(b []byte) Address {
	var a Address
	a.SetBytes(b)
	return a
}

func HexToAddress(s string) Address { return BytesToAddress(FromHex1(s)) }

func PublicToAddress(key crypto.PubKey) Address {
	bPub, err := crypto.MarshalPublicKey(key)
	if err != nil {
		return Address{0}
	}

	h := sha3.New256()
	h.Write(bPub)
	hash := h.Sum(nil)
	var addr Address
	copy(addr[:], hash[:AddressLength])
	return addr
}

func PrivateToAddress(key crypto.PrivKey) Address {
	return PublicToAddress(key.GetPublic())
}

func HexToString(hexs string) (Address, error) {
	a := Address{0}
	if !strings.HasPrefix(strings.ToUpper(hexs), prefixAddress) {
		return a, fmt.Errorf("invalid prefix address")
	}

	b, err := hex.DecodeString(hexs[len(prefixAddress):])
	if err != nil {
		return a, err
	}

	copy(a[:], b)

	return a, nil
}

func IsHexAddress(s string) bool {
	if has0xPrefix(s) {
		s = s[2:]
	}
	return len(s) == 2*AddressLength && isHex(s)
}

func (a Address) Bytes() []byte { return a[:] }
func (a Address) Hash() Hash    { return BytesToHash(a[:]) }

func (a Address) Hex() string {
	return string(a.checksumHex())
}

func (a Address) String() string {
	return a.Hex()
}

type Addresses []Address

func (addrs Addresses) Len() int {
	return len(addrs)
}
func (addrs Addresses) Less(i, j int) bool {
	return bytes.Compare(addrs[i][:], addrs[j][:]) == -1
}
func (addrs Addresses) Swap(i, j int) {
	addrs[i], addrs[j] = addrs[j], addrs[i]
}

func (a *Address) checksumHex() []byte {
	buf := a.hex()
	sha := sha3.NewLegacyKeccak256()
	sha.Write(buf[2:]) //nolint:errcheck
	hash := sha.Sum(nil)
	for i := 2; i < len(buf); i++ {
		hashByte := hash[(i-2)/2]
		if i%2 == 0 {
			hashByte = hashByte >> 4
		} else {
			hashByte &= 0xf
		}
		if buf[i] > '9' && hashByte > 7 {
			buf[i] -= 32
		}
	}
	return buf
}

func (a Address) hex() []byte {
	var buf [len(a)*2 + 2]byte
	copy(buf[:2], "0x")
	hex.Encode(buf[2:], a[:])
	return buf[:]
}

func (a *Address) DecodeBytes(b []byte) bool {
	if len(b) != AddressLength {
		return false
	}

	copy(a[:], b)
	return true
}

func (a *Address) DecodeString(s string) bool {
	if !strings.HasPrefix(strings.ToUpper(s), prefixAddress) {
		return false
	}

	b, err := hex.DecodeString(s[len(prefixAddress):])
	if err != nil {
		*a = Address{}
		return false
	}

	copy(a[:], b)
	return true
}

func (a Address) Equal(other Address) bool {
	return a == other
}

func (a *Address) IsNull() bool {
	return *a == nullAddress
}

func (a Address) Marshal() ([]byte, error) {
	return a.Bytes(), nil
}

func (a *Address) MarshalTo(data []byte) (n int, err error) {
	copy(data, a[:])
	return len(data), nil
}

func (a *Address) Unmarshal(data []byte) error {
	if len(data) != AddressLength {
		return fmt.Errorf("invalid bytes len: %d, hex: %s", len(data), a.Hex())
	}

	copy(a[:], data)
	return nil
}

func (a *Address) SetBytes(b []byte) *Address {
	if len(b) > len(a) {
		b = b[len(b)-AddressLength:]
	}
	copy(a[AddressLength-len(b):], b)
	return a
}

func (a Address) MarshalText() ([]byte, error) {
	return hexutil.Bytes(a[:]).MarshalText()
}

func (a *Address) UnmarshalText(input []byte) error {
	return hexutil.UnmarshalFixedText("Address", input, a[:])
}

func (a *Address) UnmarshalJSON(input []byte) error {
	return hexutil.UnmarshalFixedJSON(addressT, input, a[:])
}

func (a *Address) Scan(src interface{}) error {
	srcB, ok := src.([]byte)
	if !ok {
		return fmt.Errorf("can't scan %T into Address", src)
	}
	if len(srcB) != AddressLength {
		return fmt.Errorf("can't scan []byte of len %d into Address, want %d", len(srcB), AddressLength)
	}
	copy(a[:], srcB)
	return nil
}

func (a Address) Value() (driver.Value, error) {
	return a[:], nil
}

func (a Address) Size() int {
	return AddressLength
}

func isHex(str string) bool {
	if len(str)%2 != 0 {
		return false
	}
	for _, c := range []byte(str) {
		if !isHexCharacter(c) {
			return false
		}
	}
	return true
}

func isHexCharacter(c byte) bool {
	return ('0' <= c && c <= '9') || ('a' <= c && c <= 'f') || ('A' <= c && c <= 'F')
}
