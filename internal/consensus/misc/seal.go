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

package misc

import (
	"bytes"
	"fmt"
	"io"

	lru "github.com/hashicorp/golang-lru"
	"github.com/n42blockchain/N42/common/avmtypes"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/crypto"
	"github.com/n42blockchain/N42/common/rlp"
	"github.com/n42blockchain/N42/common/types"
	"golang.org/x/crypto/sha3"
)

// SealHash returns the hash of a block header prior to it being sealed.
// This is the hash that gets signed for PoA consensus.
// Returns zero hash and error if encoding fails.
func SealHash(header block.IHeader) (hash types.Hash, err error) {
	hasher := sha3.NewLegacyKeccak256()
	if err = EncodeSigHeader(hasher, header); err != nil {
		return types.Hash{}, err
	}
	hasher.(crypto.KeccakState).Read(hash[:])
	return hash, nil
}

// MustSealHash returns the hash of a block header prior to it being sealed.
// Panics if encoding fails. Use SealHash for error handling.
func MustSealHash(header block.IHeader) types.Hash {
	hash, err := SealHash(header)
	if err != nil {
		panic("seal hash: " + err.Error())
	}
	return hash
}

// SealProto returns the RLP-encoded header for signing.
// Used for generating the signature in Seal().
// Returns nil and error if encoding fails.
func SealProto(header block.IHeader) ([]byte, error) {
	b := new(bytes.Buffer)
	if err := EncodeSigHeader(b, header); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// EncodeSigHeader encodes a header for signature.
// It excludes the seal (last 65 bytes of extra-data).
// Returns error if encoding fails or extra data is too short.
func EncodeSigHeader(w io.Writer, iHeader block.IHeader) error {
	header := avmtypes.FromN42Header(iHeader)
	// Validate extra data length before slicing
	if len(header.Extra) < crypto.SignatureLength {
		return ErrMissingSignature
	}
	enc := []interface{}{
		header.ParentHash,
		header.UncleHash,
		header.Coinbase,
		header.Root,
		header.TxHash,
		header.ReceiptHash,
		header.Bloom,
		header.Difficulty,
		header.Number,
		header.GasLimit,
		header.GasUsed,
		header.Time,
		header.Extra[:len(header.Extra)-crypto.SignatureLength], // Exclude the seal
		header.MixDigest,
		header.Nonce,
	}
	if header.BaseFee != nil {
		enc = append(enc, header.BaseFee)
	}
	return rlp.Encode(w, enc)
}

// Ecrecover extracts the Ethereum account address from a signed header.
// The result is cached in sigcache for performance.
func Ecrecover(iHeader block.IHeader, sigcache *lru.ARCCache) (types.Address, error) {
	header, ok := iHeader.(*block.Header)
	if !ok {
		return types.Address{}, fmt.Errorf("invalid header type: expected *block.Header, got %T", iHeader)
	}
	// If the signature's already cached, return that
	hash := header.Hash()
	if address, known := sigcache.Get(hash); known {
		return address.(types.Address), nil
	}
	// Retrieve the signature from the header extra-data
	if len(header.Extra) < ExtraSeal {
		return types.Address{}, ErrMissingSignature
	}
	signature := header.Extra[len(header.Extra)-ExtraSeal:]

	// Recover the public key and the N42 address
	sealHash, err := SealHash(header)
	if err != nil {
		return types.Address{}, err
	}
	pubkey, err := crypto.Ecrecover(sealHash.Bytes(), signature)
	if err != nil {
		return types.Address{}, err
	}
	var signer types.Address
	copy(signer[:], crypto.Keccak256(pubkey[1:])[12:])

	sigcache.Add(hash, signer)
	return signer, nil
}

// NewSignatureCache creates a new LRU cache for signatures.
func NewSignatureCache() *lru.ARCCache {
	cache, _ := lru.NewARC(InmemorySignatures)
	return cache
}

// NewSnapshotCache creates a new LRU cache for snapshots.
func NewSnapshotCache() *lru.ARCCache {
	cache, _ := lru.NewARC(InmemorySnapshots)
	return cache
}

