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
// N42 block-layer 2048-bit bloom filter. Bloom is the 256-byte log
// bloom used in headers; Add/Test/SetBytes provide read and write
// while BytesToBloom builds one from a raw byte slice. Hashing uses
// the crypto/cryptopool Keccak256 pool for allocation reuse and the
// lib/common/hexutility helpers for hex marshaling.

package block

import (
	"encoding/binary"
	"math/big"
	"sync"

	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
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
	if len(d) > BloomByteLength {
		d = d[len(d)-BloomByteLength:]
	}
	copy(b[BloomByteLength-len(d):], d)
}

func (b *Bloom) Add(d []byte) {
	b.add(d, make([]byte, 6))
}

func (b *Bloom) add(d []byte, buf []byte) {
	sha := crypto.NewKeccakState()
	i1, v1, i2, v2, i3, v3 := bloomValuesWithHasher(d, buf, sha)
	crypto.ReturnKeccakState(sha)
	b[i1] |= v1
	b[i2] |= v2
	b[i3] |= v3
}

func (b *Bloom) addWithHasher(d []byte, buf []byte, sha crypto.KeccakState) {
	i1, v1, i2, v2, i3, v3 := bloomValuesWithHasher(d, buf, sha)
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

// bloomBits is the (byte index, bit value) triple one bloom input contributes.
// It is a pure function of the input bytes, which is what makes memoizing it
// safe: the same address or topic always sets the same three bits.
type bloomBits struct {
	i1, i2, i3 uint16
	v1, v2, v3 byte
}

// bloomMemoLimit caps each memo. Real blocks reuse a small set of contract
// addresses and event signatures, so a few thousand entries covers the working
// set; past the cap the memo is dropped wholesale rather than evicted one at a
// time, which keeps the hot path a plain map read with no bookkeeping.
const bloomMemoLimit = 4096

// bloomHasher is the scratch a single CreateBloom/LogsBloom call borrows: a
// Keccak state plus a memo of inputs already hashed.
//
// Log addresses and topic0 event signatures repeat relentlessly — a dense
// mainnet block is thousands of ERC-20 Transfers over a handful of tokens — and
// each repeat used to pay a full Keccak permutation. Profiling the 24.0M–24.2M
// replay put CreateBloom at 3.1% of all CPU, effectively all of it in
// keccakF1600.
//
// The memo has to be owned by whoever is hashing, because 254 replay workers
// share this code and a process-wide cache would either need a lock on the hot
// path or race on its entries. Hanging it off the pooled hasher gives every
// caller a private memo for the duration of the call without threading a cache
// parameter through ProcessBlock, and sync.Pool's per-P reuse means a worker
// usually gets its own warm memo back.
type bloomHasher struct {
	sha       crypto.KeccakState
	buf       []byte
	addrMemo  map[types.Address]bloomBits
	topicMemo map[types.Hash]bloomBits
}

var bloomHasherPool = sync.Pool{
	New: func() any {
		return &bloomHasher{
			sha:       crypto.NewKeccakState(),
			buf:       make([]byte, 6),
			addrMemo:  make(map[types.Address]bloomBits, 64),
			topicMemo: make(map[types.Hash]bloomBits, 64),
		}
	},
}

func (h *bloomHasher) compute(d []byte) bloomBits {
	i1, v1, i2, v2, i3, v3 := bloomValuesWithHasher(d, h.buf, h.sha)
	return bloomBits{i1: uint16(i1), i2: uint16(i2), i3: uint16(i3), v1: v1, v2: v2, v3: v3}
}

// addressBits and topicBits take pointers on purpose. The miss path slices the
// value to hash it, and Go's escape analysis is per-variable, not per-branch: a
// by-value parameter that escapes in ANY branch is heap-allocated on EVERY
// call. Taking the address of the caller's already-heap-resident Log field
// keeps both the hit and miss paths allocation-free — the old by-value bloom
// loop was paying one small allocation per topic even before this cache.
func (h *bloomHasher) addressBits(a *types.Address) bloomBits {
	if bits, ok := h.addrMemo[*a]; ok {
		return bits
	}
	bits := h.compute(a[:])
	if len(h.addrMemo) >= bloomMemoLimit {
		clear(h.addrMemo)
	}
	h.addrMemo[*a] = bits
	return bits
}

func (h *bloomHasher) topicBits(t *types.Hash) bloomBits {
	if bits, ok := h.topicMemo[*t]; ok {
		return bits
	}
	bits := h.compute(t[:])
	if len(h.topicMemo) >= bloomMemoLimit {
		clear(h.topicMemo)
	}
	h.topicMemo[*t] = bits
	return bits
}

func (b *Bloom) addBits(bits bloomBits) {
	b[bits.i1] |= bits.v1
	b[bits.i2] |= bits.v2
	b[bits.i3] |= bits.v3
}

func (b *Bloom) addLogs(logs []*Log, h *bloomHasher) {
	for _, log := range logs {
		b.addBits(h.addressBits(&log.Address))
		for i := range log.Topics {
			b.addBits(h.topicBits(&log.Topics[i]))
		}
	}
}

func CreateBloom(receipts Receipts) Bloom {
	h := bloomHasherPool.Get().(*bloomHasher)
	defer bloomHasherPool.Put(h)
	var bin Bloom
	for _, receipt := range receipts {
		bin.addLogs(receipt.Logs, h)
	}
	return bin
}

func LogsBloom(logs []*Log) []byte {
	h := bloomHasherPool.Get().(*bloomHasher)
	defer bloomHasherPool.Put(h)
	var bin Bloom
	bin.addLogs(logs, h)
	return bin[:]
}

func Bloom9(data []byte) []byte {
	var b Bloom
	b.SetBytes(data)
	return b.Bytes()
}

func bloomValues(data []byte, hashbuf []byte) (uint, byte, uint, byte, uint, byte) {
	sha := crypto.NewKeccakState()
	i1, v1, i2, v2, i3, v3 := bloomValuesWithHasher(data, hashbuf, sha)
	crypto.ReturnKeccakState(sha)
	return i1, v1, i2, v2, i3, v3
}

func bloomValuesWithHasher(data []byte, hashbuf []byte, sha crypto.KeccakState) (uint, byte, uint, byte, uint, byte) {
	sha.Reset()
	sha.Write(data)   //nolint:errcheck
	sha.Read(hashbuf) //nolint:errcheck

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
