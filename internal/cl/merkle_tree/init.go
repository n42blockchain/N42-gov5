// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.

//go:build n42el

package merkle_tree

func init() {
	globalHasher = newMerkleHasher()
}
