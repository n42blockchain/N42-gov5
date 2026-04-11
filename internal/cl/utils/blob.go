// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// Blob unit for the utils package.
// Exports helpers such as KzgCommitmentToVersionedHash.
// Miscellaneous consensus-layer utilities.

//go:build n42el

package utils

import "github.com/n42blockchain/N42/internal/cl/depshim/common"

const VERSIONED_HASH_VERSION_KZG byte = byte(1)

func KzgCommitmentToVersionedHash(kzgCommitment common.Bytes48) (common.Hash, error) {
	versionedHash := [32]byte{}
	kzgCommitmentHash := Sha256(kzgCommitment[:])

	versionedHash[0] = VERSIONED_HASH_VERSION_KZG
	copy(versionedHash[1:], kzgCommitmentHash[1:])

	return versionedHash, nil
}
