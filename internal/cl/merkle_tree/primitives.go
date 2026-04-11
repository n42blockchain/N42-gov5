// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.

//go:build n42el

package merkle_tree

import (
	"encoding/binary"

	"github.com/n42blockchain/N42/internal/cl/depshim/common"
	"github.com/n42blockchain/N42/internal/cl/depshim/length"
)

// Uint64Root retrieves the root hash of a uint64 value by converting it to a byte array and returning it as a hash.
func Uint64Root(val uint64) common.Hash {
	var root common.Hash
	binary.LittleEndian.PutUint64(root[:], val)
	return root
}

func BytesRoot(b []byte) (out [32]byte, err error) {
	leafCount := NextPowerOfTwo(uint64((len(b) + 31) / length.Hash))
	leaves := make([]byte, leafCount*length.Hash)
	copy(leaves, b)
	if err = MerkleRootFromFlatLeaves(leaves, leaves); err != nil {
		return [32]byte{}, err
	}
	copy(out[:], leaves)
	return
}

func InPlaceRoot(key []byte) error {
	err := MerkleRootFromFlatLeaves(key, key)
	if err != nil {
		return err
	}
	return nil
}
