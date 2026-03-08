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

// Post-Quantum Precompiled Contracts
// This file implements precompiled contracts for post-quantum cryptographic
// signature verification, enabling smart contracts to verify PQ signatures.
//
// Contract addresses:
// - 0x14: Falcon-512 signature verification
// - 0x15: Dilithium2 signature verification
// - 0x16: Dilithium3 signature verification
// - 0x17: SQIsign-I signature verification (placeholder)

package vm

import (
	"errors"

	"github.com/n42blockchain/N42/common/crypto/dilithium/mode2"
	"github.com/n42blockchain/N42/common/crypto/dilithium/mode3"
	"github.com/n42blockchain/N42/common/crypto/falcon"
	"github.com/n42blockchain/N42/common/types"
)

// PQ precompile addresses
var (
	PQFalconVerifyAddr    = types.BytesToAddress([]byte{0x14})
	PQDilithium2VerifyAddr = types.BytesToAddress([]byte{0x15})
	PQDilithium3VerifyAddr = types.BytesToAddress([]byte{0x16})
	PQSQIsignVerifyAddr   = types.BytesToAddress([]byte{0x17})
)

// Gas costs for PQ signature verification
// These are calibrated based on relative computational costs
const (
	// Falcon-512 verification gas cost
	// Comparable to BN256 pairing (~3500 gas)
	FalconVerifyGas = 3500

	// Dilithium2 verification gas cost
	// Slightly higher due to larger signature size
	Dilithium2VerifyGas = 4000

	// Dilithium3 verification gas cost
	// Higher security level = more computation
	Dilithium3VerifyGas = 5000

	// SQIsign-I verification gas cost
	// Higher due to isogeny-based computation
	SQIsignVerifyGas = 8000
)

// Key and signature size constants
const (
	// PQMessageSize is the fixed message length (hash) for all PQ signature verification.
	PQMessageSize = 32

	// Falcon-512
	FalconPublicKeySize    = 897
	FalconMaxSignatureSize = 690

	// Dilithium2
	Dilithium2PublicKeySize = 1312
	Dilithium2SignatureSize = 2420

	// Dilithium3
	Dilithium3PublicKeySize = 1952
	Dilithium3SignatureSize = 3293

	// SQIsign-I
	SQIsignPublicKeySize = 64
	SQIsignSignatureSize = 177
)

var (
	errPQNotImplemented = errors.New("pq: algorithm not yet implemented")
)

// Compile-time assertions: ensure local size constants match the Dilithium library.
var _ [mode2.PublicKeySize]byte = [Dilithium2PublicKeySize]byte{}
var _ [mode2.SignatureSize]byte = [Dilithium2SignatureSize]byte{}
var _ [mode3.PublicKeySize]byte = [Dilithium3PublicKeySize]byte{}
var _ [mode3.SignatureSize]byte = [Dilithium3SignatureSize]byte{}

// PrecompiledContractsPQ contains the post-quantum precompiled contracts
var PrecompiledContractsPQ = map[types.Address]PrecompiledContract{
	PQFalconVerifyAddr:     &falconVerify{},
	PQDilithium2VerifyAddr: &dilithium2Verify{},
	PQDilithium3VerifyAddr: &dilithium3Verify{},
	PQSQIsignVerifyAddr:    &sqisignVerify{},
}

// falconVerify implements Falcon-512 signature verification as a precompile.
// Input format: pubkey (897B) || message (32B) || signature (variable, max 690B)
// Output: 0x01 (32 bytes, left-padded) if valid, 0x00 otherwise
type falconVerify struct{}

func (c *falconVerify) RequiredGas(input []byte) uint64 {
	return FalconVerifyGas
}

func (c *falconVerify) Run(input []byte) ([]byte, error) {
	// Minimum input length: pubkey (897) + message (32) = 929 bytes
	// Maximum input length: pubkey (897) + message (32) + signature (690) = 1619 bytes
	minLen := FalconPublicKeySize + PQMessageSize
	if len(input) < minLen {
		return fail(), nil
	}

	// Parse input
	pubKeyData := input[:FalconPublicKeySize]
	msgStart := FalconPublicKeySize
	msgEnd := FalconPublicKeySize + PQMessageSize
	message := input[msgStart:msgEnd]

	// Signature is the remaining bytes
	if len(input) <= msgEnd {
		return fail(), nil
	}
	signature := input[msgEnd:]

	// Check signature size bounds
	if len(signature) > FalconMaxSignatureSize {
		return fail(), nil
	}

	// Parse public key
	pk, err := falcon.PublicKeyFromBytes(pubKeyData)
	if err != nil {
		return fail(), nil
	}

	// Verify signature
	if falcon.Verify(pk, message, signature) {
		return success(), nil
	}

	return fail(), nil
}

// dilithium2Verify implements Dilithium2 signature verification as a precompile.
// Input format: pubkey (1312B) || message (32B) || signature (2420B)
// Output: 0x01 (32 bytes, left-padded) if valid, 0x00 otherwise
type dilithium2Verify struct{}

func (c *dilithium2Verify) RequiredGas(input []byte) uint64 {
	return Dilithium2VerifyGas
}

func (c *dilithium2Verify) Run(input []byte) ([]byte, error) {
	// Expected input length: pubkey (1312) + message (32) + signature (2420) = 3764 bytes
	expectedLen := Dilithium2PublicKeySize + PQMessageSize + Dilithium2SignatureSize
	if len(input) < expectedLen {
		return fail(), nil
	}

	// Parse input
	pubKeyData := input[:Dilithium2PublicKeySize]
	msgStart := Dilithium2PublicKeySize
	msgEnd := Dilithium2PublicKeySize + PQMessageSize
	message := input[msgStart:msgEnd]
	sigStart := msgEnd
	sigEnd := sigStart + Dilithium2SignatureSize
	signature := input[sigStart:sigEnd]

	// Unpack public key from bytes
	var pkBuf [mode2.PublicKeySize]byte
	copy(pkBuf[:], pubKeyData)
	var pk mode2.PublicKey
	pk.Unpack(&pkBuf)

	// Verify signature
	if mode2.Verify(&pk, message, signature) {
		return success(), nil
	}

	return fail(), nil
}

// dilithium3Verify implements Dilithium3 signature verification as a precompile.
// Input format: pubkey (1952B) || message (32B) || signature (3293B)
// Output: 0x01 (32 bytes, left-padded) if valid, 0x00 otherwise
type dilithium3Verify struct{}

func (c *dilithium3Verify) RequiredGas(input []byte) uint64 {
	return Dilithium3VerifyGas
}

func (c *dilithium3Verify) Run(input []byte) ([]byte, error) {
	// Expected input length: pubkey (1952) + message (32) + signature (3293) = 5277 bytes
	expectedLen := Dilithium3PublicKeySize + PQMessageSize + Dilithium3SignatureSize
	if len(input) < expectedLen {
		return fail(), nil
	}

	// Parse input
	pubKeyData := input[:Dilithium3PublicKeySize]
	msgStart := Dilithium3PublicKeySize
	msgEnd := Dilithium3PublicKeySize + PQMessageSize
	message := input[msgStart:msgEnd]
	sigStart := msgEnd
	sigEnd := sigStart + Dilithium3SignatureSize
	signature := input[sigStart:sigEnd]

	// Unpack public key from bytes
	var pkBuf [mode3.PublicKeySize]byte
	copy(pkBuf[:], pubKeyData)
	var pk mode3.PublicKey
	pk.Unpack(&pkBuf)

	// Verify signature
	if mode3.Verify(&pk, message, signature) {
		return success(), nil
	}

	return fail(), nil
}

// sqisignVerify implements SQIsign-I signature verification as a precompile.
// Input format: pubkey (64B) || message (32B) || signature (177B)
// Output: 0x01 (32 bytes, left-padded) if valid, 0x00 otherwise
type sqisignVerify struct{}

func (c *sqisignVerify) RequiredGas(input []byte) uint64 {
	return SQIsignVerifyGas
}

func (c *sqisignVerify) Run(input []byte) ([]byte, error) {
	// SQIsign has no stable Go implementation available.
	// Return an explicit error so callers know this is not yet supported,
	// rather than silently returning a verification failure.
	return nil, errPQNotImplemented
}

// success returns a 32-byte value with 1 in the last byte (left-padded).
func success() []byte {
	return types.LeftPadBytes([]byte{1}, 32)
}

// fail returns a 32-byte zero value (all zeros).
func fail() []byte {
	return make([]byte, 32)
}

// GetPQPrecompiles returns the PQ precompiled contracts map.
func GetPQPrecompiles() map[types.Address]PrecompiledContract {
	return PrecompiledContractsPQ
}

// AddPQPrecompiles adds PQ precompiles to an existing precompile map
func AddPQPrecompiles(contracts map[types.Address]PrecompiledContract) map[types.Address]PrecompiledContract {
	result := make(map[types.Address]PrecompiledContract)
	for k, v := range contracts {
		result[k] = v
	}
	for k, v := range PrecompiledContractsPQ {
		result[k] = v
	}
	return result
}

// IsPQPrecompile checks if an address is a PQ precompiled contract
func IsPQPrecompile(addr types.Address) bool {
	_, ok := PrecompiledContractsPQ[addr]
	return ok
}
