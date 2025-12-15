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

package vm

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"errors"
	"math/big"
)

// =============================================================================
// EIP-7212/EIP-7951: secp256r1 (P-256) Precompile
// Address: 0x100 (proposed) or 0x0b (some proposals)
//
// This precompile verifies ECDSA signatures on the secp256r1 (P-256/prime256v1)
// curve, which is widely used in secure enclaves, passkeys, and WebAuthn.
//
// Input format (160 bytes):
//   - [0:32]   message hash
//   - [32:64]  r component of signature
//   - [64:96]  s component of signature
//   - [96:128] x coordinate of public key
//   - [128:160] y coordinate of public key
//
// Output:
//   - 32 bytes: 0x01 if valid, empty if invalid
// =============================================================================

const (
	// P256VerifyGas is the gas cost for P256VERIFY precompile
	P256VerifyGas = 3450

	// P256VerifyInputLength is the expected input length
	P256VerifyInputLength = 160
)

var (
	// p256Curve is the secp256r1 curve
	p256Curve = elliptic.P256()

	// p256N is the order of the curve
	p256N = p256Curve.Params().N

	// p256HalfN is N/2, used for malleability check
	p256HalfN = new(big.Int).Rsh(p256N, 1)

	// errP256InvalidSignature is returned for invalid signatures
	errP256InvalidSignature = errors.New("invalid P-256 signature")

	// errP256InvalidPublicKey is returned for invalid public keys
	errP256InvalidPublicKey = errors.New("invalid P-256 public key")
)

// p256Verify implements the secp256r1 signature verification precompile.
// EIP-7212: secp256r1 signature verification precompile
type p256Verify struct{}

// RequiredGas returns the gas required to execute the precompile.
func (c *p256Verify) RequiredGas(input []byte) uint64 {
	return P256VerifyGas
}

// Run executes the precompile.
func (c *p256Verify) Run(input []byte) ([]byte, error) {
	// Pad input to expected length
	if len(input) < P256VerifyInputLength {
		padded := make([]byte, P256VerifyInputLength)
		copy(padded, input)
		input = padded
	}

	// Extract components
	hash := input[0:32]
	r := new(big.Int).SetBytes(input[32:64])
	s := new(big.Int).SetBytes(input[64:96])
	x := new(big.Int).SetBytes(input[96:128])
	y := new(big.Int).SetBytes(input[128:160])

	// Validate signature components
	// r and s must be in [1, N-1]
	if r.Sign() <= 0 || r.Cmp(p256N) >= 0 {
		return nil, nil // Invalid signature returns empty, not error
	}
	if s.Sign() <= 0 || s.Cmp(p256N) >= 0 {
		return nil, nil
	}

	// Optional: Check for signature malleability (s <= N/2)
	// Some implementations require this, some don't
	// Uncomment if needed:
	// if s.Cmp(p256HalfN) > 0 {
	//     return nil, nil
	// }

	// Validate public key is on curve
	if !p256Curve.IsOnCurve(x, y) {
		return nil, nil
	}

	// Construct public key
	pubKey := &ecdsa.PublicKey{
		Curve: p256Curve,
		X:     x,
		Y:     y,
	}

	// Verify signature
	if ecdsa.Verify(pubKey, hash, r, s) {
		// Return 1 as 32-byte big-endian
		result := make([]byte, 32)
		result[31] = 1
		return result, nil
	}

	// Invalid signature returns empty output
	return nil, nil
}

// GetP256Verify returns a new p256Verify precompile instance.
func GetP256Verify() PrecompiledContract {
	return &p256Verify{}
}

// =============================================================================
// EIP-7951: P-256 ECRECOVER variant
// Similar to ecrecover but for P-256 curve
// =============================================================================

// p256Ecrecover implements P-256 public key recovery from signature.
// This is similar to ecrecover but for the P-256 curve.
type p256Ecrecover struct{}

// RequiredGas returns the gas required to execute the precompile.
func (c *p256Ecrecover) RequiredGas(input []byte) uint64 {
	return P256VerifyGas
}

// Run executes the precompile.
// Input format (129 bytes):
//   - [0:32]  message hash
//   - [32:64] r component of signature
//   - [64:96] s component of signature
//   - [96]    v (recovery id, 0 or 1)
//
// Output:
//   - 64 bytes: x and y coordinates of recovered public key, or empty if recovery fails
func (c *p256Ecrecover) Run(input []byte) ([]byte, error) {
	const p256EcrecoverInputLength = 97

	if len(input) < p256EcrecoverInputLength {
		padded := make([]byte, p256EcrecoverInputLength)
		copy(padded, input)
		input = padded
	}

	// Extract components
	hash := input[0:32]
	r := new(big.Int).SetBytes(input[32:64])
	s := new(big.Int).SetBytes(input[64:96])
	v := input[96]

	// v must be 0 or 1
	if v > 1 {
		return nil, nil
	}

	// Validate r and s
	if r.Sign() <= 0 || r.Cmp(p256N) >= 0 {
		return nil, nil
	}
	if s.Sign() <= 0 || s.Cmp(p256N) >= 0 {
		return nil, nil
	}

	// Recover public key
	pubX, pubY := recoverP256PublicKey(hash, r, s, int(v))
	if pubX == nil || pubY == nil {
		return nil, nil
	}

	// Return x || y (64 bytes total)
	result := make([]byte, 64)
	pubX.FillBytes(result[0:32])
	pubY.FillBytes(result[32:64])
	return result, nil
}

// recoverP256PublicKey recovers a P-256 public key from a signature.
// This is a simplified implementation - production code should use
// a well-tested library.
func recoverP256PublicKey(hash []byte, r, s *big.Int, v int) (*big.Int, *big.Int) {
	curve := p256Curve
	params := curve.Params()
	
	// Calculate the two possible x coordinates
	// x = r + kN for k = 0, 1
	x := new(big.Int).Set(r)
	
	// Try to find y such that (x, y) is on the curve
	// y² = x³ - 3x + b (mod p)
	y := calculateP256Y(x, params)
	if y == nil {
		return nil, nil
	}
	
	// Choose the correct y based on v
	if y.Bit(0) != uint(v) {
		y.Sub(params.P, y)
	}
	
	// Verify the point is on the curve
	if !curve.IsOnCurve(x, y) {
		return nil, nil
	}
	
	// Calculate e = hash as integer
	e := new(big.Int).SetBytes(hash)
	
	// Calculate the public key: Q = r⁻¹ * (s*R - e*G)
	rInv := new(big.Int).ModInverse(r, params.N)
	if rInv == nil {
		return nil, nil
	}
	
	// sR
	sRx, sRy := curve.ScalarMult(x, y, s.Bytes())
	
	// eG
	eGx, eGy := curve.ScalarBaseMult(e.Bytes())
	
	// -eG
	negEGy := new(big.Int).Sub(params.P, eGy)
	
	// sR - eG
	diffX, diffY := curve.Add(sRx, sRy, eGx, negEGy)
	
	// r⁻¹ * (sR - eG)
	pubX, pubY := curve.ScalarMult(diffX, diffY, rInv.Bytes())
	
	return pubX, pubY
}

// calculateP256Y calculates y for a given x on P-256.
// y² = x³ - 3x + b (mod p)
func calculateP256Y(x *big.Int, params *elliptic.CurveParams) *big.Int {
	// x³
	x3 := new(big.Int).Mul(x, x)
	x3.Mul(x3, x)
	x3.Mod(x3, params.P)
	
	// 3x
	threeX := new(big.Int).Mul(big.NewInt(3), x)
	
	// x³ - 3x
	x3.Sub(x3, threeX)
	x3.Mod(x3, params.P)
	
	// x³ - 3x + b
	x3.Add(x3, params.B)
	x3.Mod(x3, params.P)
	
	// y = sqrt(x³ - 3x + b)
	y := new(big.Int).ModSqrt(x3, params.P)
	return y
}

// GetP256Ecrecover returns a new p256Ecrecover precompile instance.
func GetP256Ecrecover() PrecompiledContract {
	return &p256Ecrecover{}
}

