// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Crypto unit for the crypto package.
// Exports helpers such as PubkeyToAddress.
// Crypto primitive shims used by CL code.

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

	// secp256k1 node-key helpers used by cl/p2p to load-or-generate the
	// stable Caplin node key (mirrors erigon common/crypto). All forwarded to
	// lib/crypto, which provides identical signatures.
	GenerateKey = libcrypto.GenerateKey
	LoadECDSA   = libcrypto.LoadECDSA
	SaveECDSA   = libcrypto.SaveECDSA
)

// HashData is the EIP-7928 / EIP-7843 helper that mirrors erigon's
// crypto.HashData: keccak256 over an arbitrary byte slice. Caplin
// uses it inside Gloas-fork-guarded paths to compute the
// BlockAccessListHash. Returns a depshim/common.Hash (aliased to
// lib/common.Hash) for type compatibility with the Caplin tree.
func HashData(data []byte) [32]byte {
	return libcrypto.Keccak256Hash(data)
}

// PubkeyToAddress derives the 20-byte EL address from an ECDSA public key.
// The returned address is compatible with depshim/common.Address (both are
// aliases of lib/common.Address).
func PubkeyToAddress(p ecdsa.PublicKey) [20]byte {
	return libcrypto.PubkeyToAddress(p)
}
