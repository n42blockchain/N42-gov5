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
// Shared hashing primitives for derivable lists. HasherPool pools
// legacy Keccak256 hashers, encodeBufferPool pools temporary
// bytes.Buffers for tx / receipt encoding, and the DerivableList
// interface (Len + EncodeIndex) is implemented by Transactions and
// Receipts. encodeForDerive is the low-level helper used by the
// concrete DeriveSha variants defined in sibling files.

package hash

import (
	"bytes"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/common/rlp"
	"github.com/n42blockchain/N42/common/types"
	"sync"

	"golang.org/x/crypto/sha3"
)

// hasherPool holds LegacyKeccak256 hashers for rlpHash.
var HasherPool = sync.Pool{
	New: func() interface{} { return sha3.NewLegacyKeccak256() },
}

// encodeBufferPool holds temporary encoder buffers for DeriveSha and TX encoding.
var encodeBufferPool = sync.Pool{
	New: func() interface{} { return new(bytes.Buffer) },
}

// DerivableList is the input to DeriveSha.
// It is implemented by the 'Transactions' and 'Receipts' types.
// This is internal, do not use these methods.
type DerivableList interface {
	Len() int
	EncodeIndex(int, *bytes.Buffer)
}

func encodeForDerive(list DerivableList, i int, buf *bytes.Buffer) []byte {
	buf.Reset()
	list.EncodeIndex(i, buf)
	// It's really unfortunate that we need to do perform this copy.
	// StackTrie holds onto the values until Hash is called, so the values
	// written to it must not alias.
	return types.CopyBytes(buf.Bytes())
}

// DeriveSha creates the tree hashes of transactions and receipts in a block header.
// Uses NilHash for empty lists (legacy N42 mainnet convention).
func DeriveSha(list DerivableList) (h types.Hash) {
	return DeriveShaWith(list, true)
}

// DeriveShaV2 uses Ethereum-standard EmptyRootHash for empty lists.
// Used for Shanghai+ blocks.
func DeriveShaV2(list DerivableList) (h types.Hash) {
	return DeriveShaWith(list, false)
}

// DeriveShaWith creates the tree hash. If legacy is true, empty lists
// return NilHash (keccak256(nil)); otherwise EmptyRootHash (keccak256(RLP([]))).
func DeriveShaWith(list DerivableList, legacy bool) (h types.Hash) {
	if list == nil || list.Len() == 0 {
		if legacy {
			return NilHash
		}
		return EmptyRootHash
	}

	sha := HasherPool.Get().(crypto.KeccakState)
	defer HasherPool.Put(sha)
	sha.Reset()

	valueBuf := encodeBufferPool.Get().(*bytes.Buffer)
	defer encodeBufferPool.Put(valueBuf)

	for i := 0; i < list.Len(); i++ {
		value := encodeForDerive(list, i, valueBuf)
		sha.Write(value)
	}
	sha.Read(h[:])
	return h
}

var (
	// NilHash sum(nil)
	NilHash = types.HexToHash("0xc5d2460186f7233c927e7db2dcc703c0e500b653ca82273b7bfad8045d85a470")
	// EmptyRootHash is the Ethereum empty trie root used for empty tx/receipt tries.
	EmptyRootHash = types.HexToHash("0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421")
	// EmptyUncleHash rlpHash([]*Header(nil))
	EmptyUncleHash = types.HexToHash("0x1dcc4de8dec75d7aab85b567b6ccd41ad312451b948a7413f0a142fd40d49347")
)

func RlpHash(x interface{}) (h types.Hash) {
	sha := HasherPool.Get().(crypto.KeccakState)
	defer HasherPool.Put(sha)
	sha.Reset()
	rlp.Encode(sha, x)
	sha.Read(h[:])
	return h
}

// PrefixedRlpHash writes the prefix into the hasher before rlp-encoding x.
// It's used for typed transactions.
func PrefixedRlpHash(prefix byte, x interface{}) (h types.Hash) {
	sha := HasherPool.Get().(crypto.KeccakState)
	defer HasherPool.Put(sha)
	sha.Reset()
	sha.Write([]byte{prefix})
	rlp.Encode(sha, x)
	sha.Read(h[:])
	return h
}

// Hash defines a function that returns the sha256 checksum of the data passed in.
// https://github.com/ethereum/consensus-specs/blob/v0.9.3/specs/core/0_beacon-chain.md#hash
func Hash(data []byte) [32]byte {
	h, _ := HasherPool.Get().(crypto.KeccakState)
	defer HasherPool.Put(h)
	h.Reset()

	var b [32]byte

	// The hash interface never returns an error, for that reason
	// we are not handling the error below. For reference, it is
	// stated here https://golang.org/pkg/hash/#Hash

	// #nosec G104
	h.Write(data)
	h.Read(b[:])

	return b
}
