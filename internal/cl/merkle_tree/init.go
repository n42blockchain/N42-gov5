// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// Init unit for the merkle_tree package.
// Generalized merkle tree hashing utilities.
// Part of the n42el consensus-layer build.

//go:build n42el

package merkle_tree

func init() {
	globalHasher = newMerkleHasher()
}
