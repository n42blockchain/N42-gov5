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

package block

import (
	"encoding/binary"
	"fmt"
	"math/big"

	"github.com/n42blockchain/N42/common/crypto"
	"github.com/n42blockchain/N42/common/crypto/cryptopool"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/lib/common/hexutility"
)

type bytesBacked interface {
	Bytes() []byte
}

const (
	BloomByteLength = 256
	BloomBitLength  = 8 * BloomByteLength
)

type Bloom [BloomByteLength]byte

func BytesToBloom(b []byte) Bloom {
	var bloom Bloom
	bloom.SetBytes(b)
	return bloom
}

func (b *Bloom) SetBytes(d []byte) {
	if len(b) < len(d) {
		panic(fmt.Sprintf("bloom bytes too big %d %d", len(b), len(d)))
	}
	copy(b[BloomByteLength-len(d):], d)
}

func (b *Bloom) Add(d []byte) {
	b.add(d, make([]byte, 6))
}

func (b *Bloom) add(d []byte, buf []byte) {
	i1, v1, i2, v2, i3, v3 := bloomValues(d, buf)
	b[i1] |= v1
	b[i2] |= v2
	b[i3] |= v3
}

func (b Bloom) Big() *big.Int {
	return new(big.Int).SetBytes(b[:])
}

func (b Bloom) Bytes() []byte {
	return b[:]
}

func (b Bloom) Test(topic []byte) bool {
	i1, v1, i2, v2, i3, v3 := bloomValues(topic, make([]byte, 6))
	return v1 == v1&b[i1] &&
		v2 == v2&b[i2] &&
		v3 == v3&b[i3]
}

func (b Bloom) MarshalText() ([]byte, error) {
	return hexutil.Bytes(b[:]).MarshalText()
}

func (b *Bloom) UnmarshalText(input []byte) error {
	return hexutility.UnmarshalFixedText("Bloom", input, b[:])
}

func CreateBloom(receipts Receipts) Bloom {
	buf := make([]byte, 6)
	var bin Bloom
	for _, receipt := range receipts {
		for _, log := range receipt.Logs {
			bin.add(log.Address.Bytes(), buf)
			for _, b := range log.Topics {
				bin.add(b[:], buf)
			}
		}
	}
	return bin
}

func LogsBloom(logs []*Log) []byte {
	buf := make([]byte, 6)
	var bin Bloom
	for _, log := range logs {
		bin.add(log.Address.Bytes(), buf)
		for _, b := range log.Topics {
			bin.add(b[:], buf)
		}
	}
	return bin[:]
}

func Bloom9(data []byte) []byte {
	var b Bloom
	b.SetBytes(data)
	return b.Bytes()
}

func bloomValues(data []byte, hashbuf []byte) (uint, byte, uint, byte, uint, byte) {
	sha := crypto.NewKeccakState()
	sha.Write(data)   //nolint:errcheck
	sha.Read(hashbuf) //nolint:errcheck
	cryptopool.ReturnToPoolKeccak256(sha)

	v1 := byte(1 << (hashbuf[1] & 0x7))
	v2 := byte(1 << (hashbuf[3] & 0x7))
	v3 := byte(1 << (hashbuf[5] & 0x7))
	i1 := BloomByteLength - uint((binary.BigEndian.Uint16(hashbuf)&0x7ff)>>3) - 1
	i2 := BloomByteLength - uint((binary.BigEndian.Uint16(hashbuf[2:])&0x7ff)>>3) - 1
	i3 := BloomByteLength - uint((binary.BigEndian.Uint16(hashbuf[4:])&0x7ff)>>3) - 1
	return i1, v1, i2, v2, i3, v3
}

func BloomLookup(bin Bloom, topic bytesBacked) bool {
	return bin.Test(topic.Bytes())
}
