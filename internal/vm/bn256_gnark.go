package vm

import (
	"bytes"
	"errors"
	"math/big"

	bn254 "github.com/consensys/gnark-crypto/ecc/bn254"
	"github.com/consensys/gnark-crypto/ecc/bn254/fp"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
)

// BN254 precompiles (0x06 ecAdd, 0x07 ecMul, 0x08 ecPairing) on gnark-crypto.
// Measured on the replay box against the cloudflare port: pairing check with
// four pairs 2.32ms → 0.67ms, point addition 6.8µs → 1.3µs, scalar
// multiplication 52µs → 32µs, parsing included. The wire semantics are the
// cloudflare ones: 32-byte big-endian coordinates that must be below the
// field modulus, (0,0) is the point at infinity, G1 must be on the curve,
// G2 on the curve and in the r-torsion subgroup, and G2 coordinates are
// encoded imaginary part first.
var (
	errBn256Coordinate = errors.New("bn256: coordinate exceeds modulus")
	errBn256Malformed  = errors.New("bn256: malformed point")
	bn254FpModBytes    = func() (b [32]byte) { fp.Modulus().FillBytes(b[:]); return }()
)

func bn256Coordinate(z *fp.Element, b []byte) error {
	if bytes.Compare(b, bn254FpModBytes[:]) >= 0 {
		return errBn256Coordinate
	}
	z.SetBytes(b)
	return nil
}

// bn256G1 decodes a 64-byte G1 point.
func bn256G1(b []byte) (p bn254.G1Affine, err error) {
	if err = bn256Coordinate(&p.X, b[:32]); err != nil {
		return
	}
	if err = bn256Coordinate(&p.Y, b[32:64]); err != nil {
		return
	}
	if p.X.IsZero() && p.Y.IsZero() {
		return // infinity
	}
	if !p.IsOnCurve() {
		err = errBn256Malformed
	}
	return
}

// bn256G2 decodes a 128-byte G2 point (x.imag, x.real, y.imag, y.real).
func bn256G2(b []byte) (p bn254.G2Affine, err error) {
	if err = bn256Coordinate(&p.X.A1, b[:32]); err != nil {
		return
	}
	if err = bn256Coordinate(&p.X.A0, b[32:64]); err != nil {
		return
	}
	if err = bn256Coordinate(&p.Y.A1, b[64:96]); err != nil {
		return
	}
	if err = bn256Coordinate(&p.Y.A0, b[96:128]); err != nil {
		return
	}
	if p.X.IsZero() && p.Y.IsZero() {
		return
	}
	if !p.IsOnCurve() || !p.IsInSubGroup() {
		err = errBn256Malformed
	}
	return
}

func bn256EncodeG1(p *bn254.G1Affine) []byte {
	out := make([]byte, 64)
	if p.IsInfinity() {
		return out
	}
	x, y := p.X.Bytes(), p.Y.Bytes()
	copy(out[:32], x[:])
	copy(out[32:], y[:])
	return out
}

func runBn256Add(input []byte) ([]byte, error) {
	x, err := bn256G1(getData(input, 0, 64))
	if err != nil {
		return nil, err
	}
	y, err := bn256G1(getData(input, 64, 64))
	if err != nil {
		return nil, err
	}
	var res bn254.G1Affine
	res.Add(&x, &y)
	return bn256EncodeG1(&res), nil
}

func runBn256ScalarMul(input []byte) ([]byte, error) {
	p, err := bn256G1(getData(input, 0, 64))
	if err != nil {
		return nil, err
	}
	// Every G1 point has order r, so the scalar can be reduced first.
	k := new(big.Int).SetBytes(getData(input, 64, 32))
	k.Mod(k, fr.Modulus())
	var res bn254.G1Affine
	if k.Sign() != 0 && !p.IsInfinity() {
		res.ScalarMultiplication(&p, k)
	}
	return bn256EncodeG1(&res), nil
}

func runBn256Pairing(input []byte) ([]byte, error) {
	if len(input)%192 > 0 {
		return nil, errBadPairingInput
	}
	n := len(input) / 192
	ps := make([]bn254.G1Affine, 0, n)
	qs := make([]bn254.G2Affine, 0, n)
	for i := 0; i < len(input); i += 192 {
		p, err := bn256G1(input[i : i+64])
		if err != nil {
			return nil, err
		}
		q, err := bn256G2(input[i+64 : i+192])
		if err != nil {
			return nil, err
		}
		if p.IsInfinity() || q.IsInfinity() {
			continue // e(O, Q) = e(P, O) = 1
		}
		ps = append(ps, p)
		qs = append(qs, q)
	}
	if len(ps) == 0 {
		return true32Byte, nil // empty product is 1
	}
	ok, err := bn254.PairingCheck(ps, qs)
	if err != nil {
		return nil, err
	}
	if ok {
		return true32Byte, nil
	}
	return false32Byte, nil
}
