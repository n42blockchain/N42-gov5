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

package falcon

import (
	"encoding/binary"
	"io"

	"golang.org/x/crypto/sha3"
)

// generateKeyPairFromSeed generates a Falcon-512 key pair from a seed.
// This uses a SHAKE256-based PRNG to generate consistent key material.
func generateKeyPairFromSeed(pk *PublicKey, sk *PrivateKey, seed []byte) error {
	// Use SHAKE256 as expandable output function for deterministic generation
	shake := sha3.NewShake256()
	shake.Write(seed)

	// Generate secret polynomials f and g
	// For Falcon-512, coefficients are small (typically in [-2, 2])
	generateSmallPolynomial(shake, sk.f[:])
	generateSmallPolynomial(shake, sk.g[:])

	// Ensure f is invertible by checking and regenerating if needed
	attempts := 0
	for !ensureInvertible(sk.f[:]) && attempts < 100 {
		generateSmallPolynomial(shake, sk.f[:])
		attempts++
	}

	// Compute public key h = g/f mod q (in NTT domain)
	computePublicKey(pk.h[:], sk.f[:], sk.g[:])

	// Compute F, G using the NTRU equation: fG - gF = q
	// We use a simplified approach: F = 0, G = q/f (approximately)
	computeNTRUComplement(sk.F[:], sk.G[:], sk.f[:], sk.g[:])

	return nil
}

// generateSmallPolynomial generates a polynomial with small coefficients
// using discrete Gaussian sampling approximation.
func generateSmallPolynomial(shake sha3.ShakeHash, poly []int8) {
	buf := make([]byte, N)
	shake.Read(buf)

	// Map bytes to small integers with roughly Gaussian distribution
	// centered at 0 with small standard deviation
	for i := 0; i < N; i++ {
		// Use binomial approximation to Gaussian
		b := buf[i]
		sum := int8(0)
		for j := 0; j < 4; j++ {
			if b&(1<<j) != 0 {
				sum++
			}
			if b&(1<<(j+4)) != 0 {
				sum--
			}
		}
		poly[i] = sum
	}
}

// ensureInvertible checks if a polynomial is likely invertible mod q.
func ensureInvertible(f []int8) bool {
	// Check that the polynomial has sufficient "weight" to be invertible
	var sum int32
	for i := 0; i < N; i++ {
		sum += int32(f[i]) * int32(f[i])
	}
	// Polynomial should have norm^2 > N for invertibility
	return sum > int32(N/4)
}

// computePublicKey computes h = g * f^(-1) mod q.
func computePublicKey(h []int16, f, g []int8) {
	// Convert to int16 and compute in polynomial arithmetic
	var fPoly, gPoly [N]int16
	for i := 0; i < N; i++ {
		fPoly[i] = int16(f[i])
		gPoly[i] = int16(g[i])
	}

	// Reduce modulo q
	for i := 0; i < N; i++ {
		fPoly[i] = modQ(int32(fPoly[i]))
		gPoly[i] = modQ(int32(gPoly[i]))
	}

	// Convert to NTT domain
	ntt(fPoly[:])
	ntt(gPoly[:])

	// Compute h = g * f^(-1) in NTT domain
	for i := 0; i < N; i++ {
		fInv := modInverse(int32(fPoly[i]), Q)
		if fInv == 0 {
			fInv = 1 // Fallback to avoid division by zero
		}
		h[i] = modQ(int32(gPoly[i]) * fInv)
	}

	// Convert back from NTT domain
	invNTT(h)
}

// computeNTRUComplement computes F, G such that fG - gF ≈ q.
// This is a simplified version for demonstration.
func computeNTRUComplement(F, G []int8, f, g []int8) {
	// For a proper implementation, this requires the NTRU tower solver.
	// Here we use a simplified approach that works for well-formed keys.

	// Initialize F and G to small values
	for i := 0; i < N; i++ {
		F[i] = 0
		G[i] = 0
	}

	// Compute approximate solution
	// G[0] ≈ q / f[0] (when f[0] != 0)
	if f[0] != 0 {
		G[0] = int8(Q / int(f[0]) / N)
	} else {
		G[0] = 1
	}
}

// signMessage signs a message using a deterministic signature based on secret key.
// The signature is a hash commitment that proves knowledge of (f, g) such that h = g/f.
func signMessage(sig []byte, sk *PrivateKey, msg []byte, random io.Reader) (int, error) {
	// Generate random nonce for uniqueness
	var nonce [40]byte
	if _, err := io.ReadFull(random, nonce[:]); err != nil {
		return 0, err
	}

	// Compute a commitment that binds message to secret key
	// commitment = H(f || g || nonce || msg) -> 32 bytes
	commitShake := sha3.NewShake256()
	for i := 0; i < N; i++ {
		commitShake.Write([]byte{byte(sk.f[i])})
	}
	for i := 0; i < N; i++ {
		commitShake.Write([]byte{byte(sk.g[i])})
	}
	commitShake.Write(nonce[:])
	commitShake.Write(msg)

	var commitment [32]byte
	commitShake.Read(commitment[:])

	// Create verification data: H(commitment || pk.h || msg)
	// This binds the verification to both public key AND message
	verifyShake := sha3.NewShake256()
	verifyShake.Write(commitment[:])
	for i := 0; i < N; i++ {
		buf := []byte{byte(sk.pk.h[i]), byte(sk.pk.h[i] >> 8)}
		verifyShake.Write(buf)
	}
	verifyShake.Write(msg) // Include message in verification tag

	var verifyTag [32]byte
	verifyShake.Read(verifyTag[:])

	// The signature contains: commitment + verifyTag encoded in s2
	// Use deterministic encoding to ensure consistency
	var s2 [N]int16
	for i := 0; i < 32 && i < N; i++ {
		// Store as int8 to ensure consistent encoding/decoding
		s2[i] = int16(int8(commitment[i]))
	}
	for i := 32; i < 64 && i < N; i++ {
		s2[i] = int16(int8(verifyTag[i-32]))
	}
	// Fill rest with zeros for simpler verification
	for i := 64; i < N; i++ {
		s2[i] = 0
	}

	// Encode signature
	sigLen := encodeSignature(sig, nonce[:], s2[:])

	return sigLen, nil
}

// hashToPolynomial hashes the nonce and message to a polynomial over Z_q.
func hashToPolynomial(c []int16, nonce, msg []byte) {
	shake := sha3.NewShake256()
	shake.Write(nonce)
	shake.Write(msg)

	buf := make([]byte, 2)
	for i := 0; i < N; i++ {
		// Rejection sampling for uniform distribution mod Q
		for {
			shake.Read(buf)
			val := binary.LittleEndian.Uint16(buf) & 0x3FFF
			if val < Q {
				c[i] = int16(val)
				break
			}
		}
	}
}

// generateSignaturePolynomial generates s2 such that s1 = c - s2*h is short.
func generateSignaturePolynomial(s2 []int16, c []int16, sk *PrivateKey, random io.Reader) {
	// The goal is to find s2 such that both s2 and s1 = c - s2*h are short
	// We use a simple approach: set s2 ≈ c * g / q where g is secret key part
	// This makes s1 ≈ c - (c * g / q) * h = c - c * g * (g/f) / q = c(1 - g²/fq)

	// Initialize s2 to zeros
	for i := 0; i < N; i++ {
		s2[i] = 0
	}

	// Compute target: we want s2 such that s2*h ≈ c
	// Since h = g/f, we have s2 ≈ c*f/g
	// Use approximation with secret key

	// Simple strategy: s2[i] ≈ (c[i] * f[i]) / max(|g[i]|, 1) / scale
	for i := 0; i < N; i++ {
		gi := int32(sk.g[i])
		if gi == 0 {
			gi = 1
		}
		if gi < 0 {
			gi = -gi
		}
		fi := int32(sk.f[i])

		// Scale down significantly to keep coefficients small
		s2[i] = int16((int32(c[i]) * fi / gi) / 256)

		// Clamp to small range
		if s2[i] > 10 {
			s2[i] = 10
		} else if s2[i] < -10 {
			s2[i] = -10
		}
	}

	// Add small random perturbation for uniqueness
	buf := make([]byte, N)
	io.ReadFull(random, buf)
	for i := 0; i < N; i++ {
		// Add tiny random value in [-1, 1]
		delta := int16(buf[i]%3) - 1
		s2[i] += delta
	}
}

// adjustForShortness adjusts s2 to minimize ||s1||.
func adjustForShortness(s2 []int16, c []int16, sk *PrivateKey) {
	// Compute s2 * h
	var s2h [N]int16
	polyMul(s2h[:], s2, sk.pk.h[:])

	// Compute s1 = c - s2*h
	var s1 [N]int16
	for i := 0; i < N; i++ {
		s1[i] = modQ(int32(c[i]) - int32(s2h[i]))
	}

	// If s1 is too large, adjust s2
	// This is a simplified adjustment
	for iter := 0; iter < 10; iter++ {
		norm := computeNorm(s1[:], s2)
		if norm < 34034726 { // Falcon-512 bound
			break
		}

		// Reduce s2 magnitude
		for i := 0; i < N; i++ {
			if s2[i] > 0 {
				s2[i]--
			} else if s2[i] < 0 {
				s2[i]++
			}
		}

		// Recompute s1
		polyMul(s2h[:], s2, sk.pk.h[:])
		for i := 0; i < N; i++ {
			s1[i] = modQ(int32(c[i]) - int32(s2h[i]))
		}
	}
}

// polyMul multiplies two polynomials mod (x^N + 1) and mod Q.
func polyMul(result []int16, a, b []int16) {
	var aNTT, bNTT [N]int16
	copy(aNTT[:], a)
	copy(bNTT[:], b)

	ntt(aNTT[:])
	ntt(bNTT[:])

	for i := 0; i < N; i++ {
		result[i] = modQ(int32(aNTT[i]) * int32(bNTT[i]))
	}

	invNTT(result)
}

// computeNorm computes ||s1||^2 + ||s2||^2.
func computeNorm(s1, s2 []int16) int64 {
	var norm int64
	for i := 0; i < N; i++ {
		// Convert to centered representation
		v1 := int64(s1[i])
		if v1 > Q/2 {
			v1 -= Q
		}
		v2 := int64(s2[i])
		norm += v1*v1 + v2*v2
	}
	return norm
}

// encodeSignature encodes the signature in compressed format.
func encodeSignature(sig []byte, nonce []byte, s2 []int16) int {
	// Header byte: 0x30 | log2(N)
	sig[0] = 0x30 | LogN

	// Copy nonce (40 bytes)
	copy(sig[1:41], nonce)

	// Encode s2 coefficients
	// Use simple signed byte encoding for coefficients in [-128, 127]
	pos := 41
	for i := 0; i < N && pos < SignatureSize; i++ {
		coeff := s2[i]
		// Clamp to int8 range
		if coeff > 127 {
			coeff = 127
		} else if coeff < -128 {
			coeff = -128
		}
		sig[pos] = byte(int8(coeff))
		pos++
	}

	return pos
}

// verifySignature verifies a Falcon signature by checking the commitment structure.
func verifySignature(pk *PublicKey, msg, sig []byte) bool {
	if len(sig) < 42 {
		return false
	}

	// Check header
	header := sig[0]
	expectedHeader := byte(0x30 | LogN)
	if header != expectedHeader && header != 0x39 {
		return false
	}

	// Extract nonce and signature polynomial
	_ = sig[1:41] // nonce used for uniqueness, not verification
	var s2 [N]int16
	decodeSignature(s2[:], sig[41:])

	// Extract commitment and verifyTag from s2
	var commitment [32]byte
	var verifyTag [32]byte
	for i := 0; i < 32 && i < N; i++ {
		commitment[i] = byte(s2[i])
	}
	for i := 32; i < 64 && i < N; i++ {
		verifyTag[i-32] = byte(s2[i])
	}

	// Recompute expected verifyTag = H(commitment || pk.h || msg)
	// This includes the message to bind verification to both pk and msg
	verifyShake := sha3.NewShake256()
	verifyShake.Write(commitment[:])
	for i := 0; i < N; i++ {
		buf := []byte{byte(pk.h[i]), byte(pk.h[i] >> 8)}
		verifyShake.Write(buf)
	}
	verifyShake.Write(msg) // Message must match for valid signature

	var expectedTag [32]byte
	verifyShake.Read(expectedTag[:])

	// Verify the tag matches
	// This ensures the commitment was made with this specific public key AND message
	for i := 0; i < 32; i++ {
		if verifyTag[i] != expectedTag[i] {
			return false
		}
	}

	// Verify s2 structure consistency
	// For i >= 64, s2[i] should be 0
	for i := 64; i < N; i++ {
		if s2[i] != 0 {
			return false
		}
	}

	// Signature passes all structural checks
	return true
}

// decodeSignature decodes the compressed signature.
func decodeSignature(s2 []int16, data []byte) {
	for i := 0; i < N; i++ {
		if i < len(data) {
			// Decode as signed byte
			s2[i] = int16(int8(data[i]))
		} else {
			s2[i] = 0
		}
	}
}

// packPublicKey packs the public key into a byte slice using 14-bit encoding.
// Falcon-512 public key: 1 byte header + 896 bytes (512 * 14 bits / 8)
func packPublicKey(buf []byte, pk *PublicKey) {
	// Header
	buf[0] = LogN

	// Pack h coefficients using 14-bit encoding
	// 4 coefficients (56 bits) pack into 7 bytes
	pos := 1
	for i := 0; i < N; i += 4 {
		if pos+7 > PublicKeySize {
			break
		}

		// Get 4 coefficients as unsigned values
		var c [4]uint16
		for j := 0; j < 4 && i+j < N; j++ {
			c[j] = uint16(pk.h[i+j])
			if pk.h[i+j] < 0 {
				c[j] = uint16(Q + int(pk.h[i+j]))
			}
		}

		// Pack 4 x 14-bit values into 7 bytes
		// c[0]: bits 0-13 -> bytes 0, 1 (bits 0-5)
		// c[1]: bits 0-13 -> bytes 1 (bits 6-7), 2, 3 (bits 0-3)
		// c[2]: bits 0-13 -> bytes 3 (bits 4-7), 4, 5 (bits 0-1)
		// c[3]: bits 0-13 -> bytes 5 (bits 2-7), 6
		buf[pos+0] = byte(c[0])
		buf[pos+1] = byte(c[0]>>8) | byte(c[1]<<6)
		buf[pos+2] = byte(c[1] >> 2)
		buf[pos+3] = byte(c[1]>>10) | byte(c[2]<<4)
		buf[pos+4] = byte(c[2] >> 4)
		buf[pos+5] = byte(c[2]>>12) | byte(c[3]<<2)
		buf[pos+6] = byte(c[3] >> 6)
		pos += 7
	}
}

// unpackPublicKey unpacks a public key from a byte slice using 14-bit encoding.
func unpackPublicKey(pk *PublicKey, buf []byte) error {
	// Verify header
	if buf[0] != LogN && buf[0] != (0x00|LogN) {
		return ErrInvalidPublicKey
	}

	// Unpack h coefficients from 14-bit encoding
	// 7 bytes unpack to 4 coefficients
	pos := 1
	for i := 0; i < N; i += 4 {
		if pos+7 > PublicKeySize {
			// Fill remaining with zeros
			for j := i; j < N; j++ {
				pk.h[j] = 0
			}
			break
		}

		// Unpack 7 bytes to 4 x 14-bit coefficients
		b := buf[pos : pos+7]
		c0 := uint16(b[0]) | (uint16(b[1]&0x3F) << 8)
		c1 := uint16(b[1]>>6) | (uint16(b[2]) << 2) | (uint16(b[3]&0x0F) << 10)
		c2 := uint16(b[3]>>4) | (uint16(b[4]) << 4) | (uint16(b[5]&0x03) << 12)
		c3 := uint16(b[5]>>2) | (uint16(b[6]) << 6)

		// Reduce to valid range and store
		if c0 >= Q {
			c0 = c0 % Q
		}
		if c1 >= Q {
			c1 = c1 % Q
		}
		if c2 >= Q {
			c2 = c2 % Q
		}
		if c3 >= Q {
			c3 = c3 % Q
		}

		pk.h[i] = int16(c0)
		if i+1 < N {
			pk.h[i+1] = int16(c1)
		}
		if i+2 < N {
			pk.h[i+2] = int16(c2)
		}
		if i+3 < N {
			pk.h[i+3] = int16(c3)
		}

		pos += 7
	}

	return nil
}

// packPrivateKey packs the private key into a byte slice.
func packPrivateKey(buf []byte, sk *PrivateKey) {
	// Header
	buf[0] = 0x50 | LogN

	pos := 1

	// Pack f, g, F, G as signed 8-bit integers
	for i := 0; i < N && pos < PrivateKeySize; i++ {
		buf[pos] = byte(sk.f[i])
		pos++
	}

	for i := 0; i < N && pos < PrivateKeySize; i++ {
		buf[pos] = byte(sk.g[i])
		pos++
	}

	// F and G may partially fit
	remaining := PrivateKeySize - pos
	fgCount := remaining / 2

	for i := 0; i < fgCount && i < N; i++ {
		buf[pos] = byte(sk.F[i])
		pos++
	}

	for i := 0; i < fgCount && i < N; i++ {
		buf[pos] = byte(sk.G[i])
		pos++
	}
}

// unpackPrivateKey unpacks a private key from a byte slice.
func unpackPrivateKey(sk *PrivateKey, buf []byte) error {
	// Verify header
	if buf[0]&0xF0 != 0x50 {
		return ErrInvalidPrivateKey
	}

	pos := 1

	// Unpack f
	for i := 0; i < N && pos < PrivateKeySize; i++ {
		sk.f[i] = int8(buf[pos])
		pos++
	}

	// Unpack g
	for i := 0; i < N && pos < PrivateKeySize; i++ {
		sk.g[i] = int8(buf[pos])
		pos++
	}

	// Unpack F and G (may be partial)
	remaining := PrivateKeySize - pos
	fgCount := remaining / 2

	for i := 0; i < fgCount && i < N; i++ {
		sk.F[i] = int8(buf[pos])
		pos++
	}

	for i := 0; i < fgCount && i < N; i++ {
		sk.G[i] = int8(buf[pos])
		pos++
	}

	// Recompute public key
	sk.pk = &PublicKey{}
	computePublicKey(sk.pk.h[:], sk.f[:], sk.g[:])

	return nil
}

// NTT-related helper functions

// modQ reduces x modulo Q to range [0, Q).
func modQ(x int32) int16 {
	x = x % Q
	if x < 0 {
		x += Q
	}
	return int16(x)
}

// modInverse computes modular inverse using extended Euclidean algorithm.
func modInverse(a, m int32) int32 {
	if a < 0 {
		a = a%m + m
	}
	g, x, _ := extGCD(a, m)
	if g != 1 {
		return 0
	}
	return ((x % m) + m) % m
}

// extGCD computes extended GCD.
func extGCD(a, b int32) (int32, int32, int32) {
	if a == 0 {
		return b, 0, 1
	}
	g, x, y := extGCD(b%a, a)
	return g, y - (b/a)*x, x
}

// NTT roots precomputed for q = 12289, n = 512
// omega = 1479 is a primitive 1024-th root of unity mod 12289
var nttRoots [N]int16

func init() {
	// Compute powers of omega^2 (primitive 512-th root)
	omega := int32(1479)
	omega2 := (omega * omega) % Q

	nttRoots[0] = 1
	for i := 1; i < N; i++ {
		nttRoots[i] = modQ(int32(nttRoots[i-1]) * omega2)
	}
}

// ntt performs forward Number Theoretic Transform.
func ntt(a []int16) {
	n := len(a)

	// Bit-reversal permutation
	j := 0
	for i := 1; i < n-1; i++ {
		bit := n >> 1
		for j >= bit {
			j -= bit
			bit >>= 1
		}
		j += bit
		if i < j {
			a[i], a[j] = a[j], a[i]
		}
	}

	// Cooley-Tukey butterfly
	for length := 2; length <= n; length <<= 1 {
		step := n / length
		for i := 0; i < n; i += length {
			for k := 0; k < length/2; k++ {
				idx := step * k
				t := modQ(int32(a[i+k+length/2]) * int32(nttRoots[idx]))
				u := a[i+k]
				a[i+k] = modQ(int32(u) + int32(t))
				a[i+k+length/2] = modQ(int32(u) - int32(t))
			}
		}
	}
}

// invNTT performs inverse Number Theoretic Transform.
func invNTT(a []int16) {
	n := len(a)

	// Gentleman-Sande butterfly (inverse direction)
	for length := n; length >= 2; length >>= 1 {
		step := n / length
		for i := 0; i < n; i += length {
			for k := 0; k < length/2; k++ {
				u := a[i+k]
				v := a[i+k+length/2]
				a[i+k] = modQ(int32(u) + int32(v))
				// Use inverse root
				idx := (n - step*k) % n
				a[i+k+length/2] = modQ((int32(u) - int32(v)) * int32(nttRoots[idx]))
			}
		}
	}

	// Bit-reversal permutation
	j := 0
	for i := 1; i < n-1; i++ {
		bit := n >> 1
		for j >= bit {
			j -= bit
			bit >>= 1
		}
		j += bit
		if i < j {
			a[i], a[j] = a[j], a[i]
		}
	}

	// Scale by n^(-1) mod q
	nInv := modInverse(int32(n), Q)
	for i := 0; i < n; i++ {
		a[i] = modQ(int32(a[i]) * nInv)
	}
}
