// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

//go:build n42el

// Package crypto re-exports the Keccak256 helpers and key utilities that
// the cl/ tree expects from erigon's common/crypto. Everything is forwarded
// to N42's lib/crypto, which already provides identical signatures.
package crypto

import (
	"crypto/ecdsa"

	libcrypto "github.com/n42blockchain/N42/lib/crypto"
)

var (
	Keccak256      = libcrypto.Keccak256
	Keccak256Hash  = libcrypto.Keccak256Hash
	NewKeccakState = libcrypto.NewKeccakState
	ToECDSA        = libcrypto.ToECDSA
)

// PubkeyToAddress derives the 20-byte EL address from an ECDSA public key.
// The returned address is compatible with depshim/common.Address (both are
// aliases of lib/common.Address).
func PubkeyToAddress(p ecdsa.PublicKey) [20]byte {
	return libcrypto.PubkeyToAddress(p)
}
