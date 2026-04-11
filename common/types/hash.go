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
// 32-byte Hash primitive used throughout N42. Defines HashLength,
// the hashT / addressT reflect descriptors, Bytes / Big / Hex /
// TerminalString / String accessors, a SHA3-Keccak256 back-end for
// hashing and the BytesToHash / BigToHash / HexToHash constructors.
// Also used as tx / block / receipt / state-root identifier.

package types

import (
	"bytes"
	"database/sql/driver"
	"encoding/hex"
	"fmt"
	"hash"
	"math/big"
	"math/rand"
	"reflect"

	"golang.org/x/crypto/sha3"

	"github.com/n42blockchain/N42/common/hexutil"
)

const HashLength = 32

var (
	hashT    = reflect.TypeOf(Hash{})
	addressT = reflect.TypeOf(Address{})
)

type Hash [HashLength]byte

func (h Hash) Bytes() []byte  { return h[:] }
func (h Hash) Big() *big.Int  { return new(big.Int).SetBytes(h[:]) }
func (h Hash) Hex() string    { return hexutil.Encode(h[:]) }

func (h Hash) TerminalString() string {
	return fmt.Sprintf("%x…%x", h[:3], h[29:])
}

func (h *Hash) UnmarshalText(input []byte) error {
	return hexutil.UnmarshalFixedText("Hash", input, h[:])
}

func (h Hash) MarshalText() ([]byte, error) {
	return hexutil.Bytes(h[:]).MarshalText()
}

func (h *Hash) SetBytes(b []byte) error {
	if len(b) != HashLength {
		return fmt.Errorf("invalid bytes len %d", len(b))
	}

	copy(h[:], b[:HashLength])
	return nil
}

func (h Hash) Generate(rand *rand.Rand, size int) reflect.Value {
	m := rand.Intn(len(h))
	for i := len(h) - 1; i > m; i-- {
		h[i] = byte(rand.Uint32())
	}
	return reflect.ValueOf(h)
}

func (h *Hash) Scan(src interface{}) error {
	srcB, ok := src.([]byte)
	if !ok {
		return fmt.Errorf("can't scan %T into Hash", src)
	}
	if len(srcB) != HashLength {
		return fmt.Errorf("can't scan []byte of len %d into Hash, want %d", len(srcB), HashLength)
	}
	copy(h[:], srcB)
	return nil
}

func (h Hash) Value() (driver.Value, error) {
	return h[:], nil
}

func BytesHash(b []byte) Hash {
	return sha3.Sum256(b)
}

func BytesToHash(b []byte) Hash {
	var h Hash
	if len(b) > HashLength {
		b = b[len(b)-HashLength:]
	}
	copy(h[HashLength-len(b):], b)
	return h
}

func StringToHash(s string) Hash {
	b, err := hex.DecodeString(s)
	if err != nil {
		return Hash{}
	}
	return BytesToHash(b)
}

func (h Hash) String() string {
	return hex.EncodeToString(h[:])
}

func (h Hash) HexBytes() []byte {
	return []byte(h.String())
}

func (h *Hash) SetString(s string) error {
	if len(s) != HashLength*2 {
		return fmt.Errorf("invalid string len %v， len(%d)", string(s), len(s))
	}

	b, err := hex.DecodeString(s)
	if err != nil {
		return err
	}

	return h.SetBytes(b)
}

func (h Hash) Marshal() ([]byte, error) {
	return h.Bytes(), nil
}

func (h *Hash) MarshalTo(data []byte) (n int, err error) {
	copy(data, h[:])
	return HashLength, nil
}

func (h *Hash) Unmarshal(data []byte) error {
	return h.SetBytes(data)
}

func (h *Hash) Size() int {
	return HashLength
}

func (h Hash) Format(s fmt.State, c rune) {
	hexb := make([]byte, 2+len(h)*2)
	copy(hexb, "0x")
	hex.Encode(hexb[2:], h[:])

	switch c {
	case 'x', 'X':
		if !s.Flag('#') {
			hexb = hexb[2:]
		}
		if c == 'X' {
			hexb = bytes.ToUpper(hexb)
		}
		fallthrough
	case 'v', 's':
		s.Write(hexb)
	case 'q':
		q := []byte{'"'}
		s.Write(q)
		s.Write(hexb)
		s.Write(q)
	case 'd':
		fmt.Fprint(s, ([len(h)]byte)(h))
	default:
		fmt.Fprintf(s, "%%!%c(hash=%x)", c, h)
	}
}

func (h *Hash) UnmarshalJSON(input []byte) error {
	return hexutil.UnmarshalFixedJSON(hashT, input, h[:])
}

func (h Hash) Equal(other Hash) bool {
	return h == other
}

func HashDifference(a, b []Hash) []Hash {
	remove := make(map[Hash]struct{}, len(b))
	for _, hash := range b {
		remove[hash] = struct{}{}
	}
	keep := make([]Hash, 0, len(a))
	for _, hash := range a {
		if _, ok := remove[hash]; !ok {
			keep = append(keep, hash)
		}
	}
	return keep
}

type keccakState interface {
	hash.Hash
	Read([]byte) (int, error)
}

type Hasher struct {
	Sha keccakState
}

var hasherPools = make(chan *Hasher, 128)

func NewHasher() *Hasher {
	var h *Hasher
	select {
	case h = <-hasherPools:
	default:
		h = &Hasher{Sha: sha3.NewLegacyKeccak256().(keccakState)}
	}
	return h
}

func ReturnHasherToPool(h *Hasher) {
	select {
	case hasherPools <- h:
	default:
	}
}

func HashData(data []byte) (Hash, error) {
	h := NewHasher()
	defer ReturnHasherToPool(h)
	h.Sha.Reset()

	_, err := h.Sha.Write(data)
	if err != nil {
		return Hash{}, err
	}

	var buf Hash
	_, err = h.Sha.Read(buf[:])
	if err != nil {
		return Hash{}, err
	}
	return buf, nil
}

func Bytes2Hex(d []byte) string {
	return hex.EncodeToString(d)
}

func HexToHash(s string) Hash { return BytesToHash(FromHex2Bytes(s)) }

func FromHex2Bytes(s string) []byte { return FromHex1(s) }

type Hashes []Hash

func (hashes Hashes) Len() int {
	return len(hashes)
}
func (hashes Hashes) Less(i, j int) bool {
	return bytes.Compare(hashes[i][:], hashes[j][:]) == -1
}
func (hashes Hashes) Swap(i, j int) {
	hashes[i], hashes[j] = hashes[j], hashes[i]
}
