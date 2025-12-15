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

package precompiles

import (
	"github.com/n42blockchain/N42/internal/vm"
)

// =============================================================================
// Precompile Factory Functions
//
// These functions create precompiled contract instances.
// They wrap the existing implementations in internal/vm/contracts.go
// to avoid code duplication during the migration period.
// =============================================================================

// NewEcrecover creates an ecrecover precompile (address 0x01).
// Recovers the address associated with the public key from elliptic curve signature.
func NewEcrecover() PrecompiledContract {
	return vm.GetEcrecover()
}

// NewSha256 creates a SHA256 precompile (address 0x02).
func NewSha256() PrecompiledContract {
	return vm.GetSha256()
}

// NewRipemd160 creates a RIPEMD160 precompile (address 0x03).
func NewRipemd160() PrecompiledContract {
	return vm.GetRipemd160()
}

// NewDataCopy creates a data copy precompile (address 0x04).
// Simply copies input data to output (identity function).
func NewDataCopy() PrecompiledContract {
	return vm.GetDataCopy()
}

// NewBigModExp creates a big integer modular exponentiation precompile (address 0x05).
// eip2565 enables the EIP-2565 gas repricing.
func NewBigModExp(eip2565 bool) PrecompiledContract {
	return vm.GetBigModExp(eip2565)
}

// NewBn256Add creates a BN256 curve point addition precompile (address 0x06).
// istanbul uses Istanbul gas costs (reduced from Byzantium).
func NewBn256Add(istanbul bool) PrecompiledContract {
	return vm.GetBn256Add(istanbul)
}

// NewBn256ScalarMul creates a BN256 scalar multiplication precompile (address 0x07).
// istanbul uses Istanbul gas costs.
func NewBn256ScalarMul(istanbul bool) PrecompiledContract {
	return vm.GetBn256ScalarMul(istanbul)
}

// NewBn256Pairing creates a BN256 pairing check precompile (address 0x08).
// istanbul uses Istanbul gas costs.
func NewBn256Pairing(istanbul bool) PrecompiledContract {
	return vm.GetBn256Pairing(istanbul)
}

// NewBlake2F creates a BLAKE2b F compression function precompile (address 0x09).
// Added in Istanbul (EIP-152).
func NewBlake2F() PrecompiledContract {
	return vm.GetBlake2F()
}

// =============================================================================
// BLS12-381 Precompiles (EIP-2537)
// =============================================================================

// NewBls12381G1Add creates a BLS12-381 G1 addition precompile (address 0x0a).
func NewBls12381G1Add() PrecompiledContract {
	return vm.GetBls12381G1Add()
}

// NewBls12381G1Mul creates a BLS12-381 G1 multiplication precompile (address 0x0b).
func NewBls12381G1Mul() PrecompiledContract {
	return vm.GetBls12381G1Mul()
}

// NewBls12381G1MultiExp creates a BLS12-381 G1 multi-exponentiation precompile (address 0x0c).
func NewBls12381G1MultiExp() PrecompiledContract {
	return vm.GetBls12381G1MultiExp()
}

// NewBls12381G2Add creates a BLS12-381 G2 addition precompile (address 0x0d).
func NewBls12381G2Add() PrecompiledContract {
	return vm.GetBls12381G2Add()
}

// NewBls12381G2Mul creates a BLS12-381 G2 multiplication precompile (address 0x0e).
func NewBls12381G2Mul() PrecompiledContract {
	return vm.GetBls12381G2Mul()
}

// NewBls12381G2MultiExp creates a BLS12-381 G2 multi-exponentiation precompile (address 0x0f).
func NewBls12381G2MultiExp() PrecompiledContract {
	return vm.GetBls12381G2MultiExp()
}

// NewBls12381Pairing creates a BLS12-381 pairing precompile (address 0x10).
func NewBls12381Pairing() PrecompiledContract {
	return vm.GetBls12381Pairing()
}

// NewBls12381MapG1 creates a BLS12-381 map to G1 precompile (address 0x11).
func NewBls12381MapG1() PrecompiledContract {
	return vm.GetBls12381MapG1()
}

// NewBls12381MapG2 creates a BLS12-381 map to G2 precompile (address 0x12).
func NewBls12381MapG2() PrecompiledContract {
	return vm.GetBls12381MapG2()
}

// =============================================================================
// secp256r1 (P-256) Precompiles (EIP-7212/EIP-7951)
// =============================================================================

// NewP256Verify creates a P-256 ECDSA signature verification precompile (address 0x100).
// This verifies signatures on the secp256r1 (P-256/prime256v1) curve.
func NewP256Verify() PrecompiledContract {
	return vm.GetP256Verify()
}

// NewP256Ecrecover creates a P-256 public key recovery precompile.
func NewP256Ecrecover() PrecompiledContract {
	return vm.GetP256Ecrecover()
}

