// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.

//go:build n42el

package utils

import "github.com/n42blockchain/N42/internal/cl/depshim/common"

// Check if leaf at index verifies against the Merkle root and branch
func IsValidMerkleBranch(leaf common.Hash, branch []common.Hash, depth uint64, index uint64, root [32]byte) bool {
	value := leaf
	for i := uint64(0); i < depth; i++ {
		if (index / PowerOf2(i) % 2) == 1 {
			value = Sha256(append(branch[i][:], value[:]...))
		} else {
			value = Sha256(append(value[:], branch[i][:]...))
		}
	}
	return value == root
}

func PreparateRootsForHashing(roots []common.Hash) [][32]byte {
	ret := make([][32]byte, len(roots))
	for i := range roots {
		copy(ret[i][:], roots[i][:])
	}
	return ret
}
