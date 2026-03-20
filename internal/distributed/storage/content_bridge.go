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

package storage

import (
	"encoding/hex"
	"fmt"
)

// CIDVersion identifies the IPFS CID version.
type CIDVersion int

const (
	CIDv0 CIDVersion = 0
	CIDv1 CIDVersion = 1
)

// Keccak256ToCIDv1 converts a keccak256 hash to an IPFS CIDv1 string.
// CIDv1 format: multibase(base32) + version(1) + codec(raw=0x55) + multihash(keccak256=0x1b, 32 bytes)
//
// This enables interoperability between on-chain content-addressed storage
// (keccak256 hashes from the CAS precompile) and IPFS content identifiers.
func Keccak256ToCIDv1(hash []byte) string {
	if len(hash) != 32 {
		return ""
	}
	// Simplified CID: use hex encoding with a prefix indicating keccak256
	// Full CID encoding would use multibase + multicodec + multihash
	// For production, use github.com/ipfs/go-cid library
	return fmt.Sprintf("f01551b20%s", hex.EncodeToString(hash))
}

// CIDv1ToKeccak256 extracts a keccak256 hash from a CIDv1 string.
// Returns nil if the CID is not a keccak256-based CIDv1.
func CIDv1ToKeccak256(cid string) []byte {
	// Simplified: check for our hex-encoded format
	const prefix = "f01551b20"
	if len(cid) < len(prefix)+64 {
		return nil
	}
	if cid[:len(prefix)] != prefix {
		return nil
	}
	hashHex := cid[len(prefix) : len(prefix)+64]
	hash, err := hex.DecodeString(hashHex)
	if err != nil || len(hash) != 32 {
		return nil
	}
	return hash
}

// ContentBridge connects the on-chain ContentStore with IPFS.
// It provides CID interop and optional auto-pinning.
type ContentBridge struct {
	ipfsClient *IPFSClient
	autoPin    bool
}

// NewContentBridge creates a content bridge.
func NewContentBridge(client *IPFSClient, autoPin bool) *ContentBridge {
	return &ContentBridge{ipfsClient: client, autoPin: autoPin}
}

// HashToCID converts a keccak256 content hash to an IPFS CID.
func (cb *ContentBridge) HashToCID(hash []byte) string {
	return Keccak256ToCIDv1(hash)
}

// CIDToHash extracts a keccak256 hash from a CID.
func (cb *ContentBridge) CIDToHash(cid string) []byte {
	return CIDv1ToKeccak256(cid)
}
