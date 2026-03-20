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
	"errors"
	"math/big"

	gnarkbls12381 "github.com/consensys/gnark-crypto/ecc/bls12-381"
	"github.com/consensys/gnark-crypto/ecc/bls12-381/fp"

	"github.com/n42blockchain/N42/common/crypto/bls12381"
	"github.com/n42blockchain/N42/params"
)

var (
	errBLS12381InvalidInputLength          = errors.New("invalid input length")
	errBLS12381InvalidFieldElementTopBytes = errors.New("invalid field element top bytes")
	errBLS12381G1PointSubgroup             = errors.New("g1 point is not on correct subgroup")
	errBLS12381G2PointSubgroup             = errors.New("g2 point is not on correct subgroup")
)

func decodeG1PointInSubgroup(g *bls12381.G1, in []byte) (*bls12381.PointG1, error) {
	point, err := g.DecodePoint(in)
	if err != nil {
		return nil, err
	}
	if !g.InCorrectSubgroup(point) {
		return nil, errBLS12381G1PointSubgroup
	}
	return point, nil
}

func decodeG2PointInSubgroup(g *bls12381.G2, in []byte) (*bls12381.PointG2, error) {
	point, err := g.DecodePoint(in)
	if err != nil {
		return nil, err
	}
	if !g.InCorrectSubgroup(point) {
		return nil, errBLS12381G2PointSubgroup
	}
	return point, nil
}

func bls12381MultiExpGas(inputLen, pairSize int, mulGas uint64, discounts []uint64) uint64 {
	k := inputLen / pairSize
	if k == 0 {
		return 0
	}

	discountIndex := k
	if discountIndex >= len(discounts) {
		discountIndex = len(discounts) - 1
	}
	return (uint64(k) * mulGas * discounts[discountIndex]) / 1000
}

// bls12381G1Add implements EIP-2537 G1Add precompile.
type bls12381G1Add struct{}

func (c *bls12381G1Add) RequiredGas(input []byte) uint64 {
	return params.Bls12381G1AddGas
}

func (c *bls12381G1Add) Run(input []byte) ([]byte, error) {
	if len(input) != 256 {
		return nil, errBLS12381InvalidInputLength
	}
	g := bls12381.NewG1()
	p0, err := g.DecodePoint(input[:128])
	if err != nil {
		return nil, err
	}
	p1, err := g.DecodePoint(input[128:])
	if err != nil {
		return nil, err
	}

	r := g.New()
	g.Add(r, p0, p1)
	return g.EncodePoint(r), nil
}

// bls12381G1Mul implements EIP-2537 G1Mul precompile.
type bls12381G1Mul struct{}

func (c *bls12381G1Mul) RequiredGas(input []byte) uint64 {
	return params.Bls12381G1MulGas
}

func (c *bls12381G1Mul) Run(input []byte) ([]byte, error) {
	if len(input) != 160 {
		return nil, errBLS12381InvalidInputLength
	}
	g := bls12381.NewG1()
	p0, err := decodeG1PointInSubgroup(g, input[:128])
	if err != nil {
		return nil, err
	}
	e := new(big.Int).SetBytes(input[128:])

	r := g.New()
	g.MulScalar(r, p0, e)
	return g.EncodePoint(r), nil
}

// bls12381G1MultiExp implements EIP-2537 G1MultiExp precompile.
type bls12381G1MultiExp struct{}

func (c *bls12381G1MultiExp) RequiredGas(input []byte) uint64 {
	return bls12381MultiExpGas(len(input), 160, params.Bls12381G1MulGas, params.Bls12381G1MultiExpDiscountTable[:])
}

func (c *bls12381G1MultiExp) Run(input []byte) ([]byte, error) {
	k := len(input) / 160
	if len(input) == 0 || len(input)%160 != 0 {
		return nil, errBLS12381InvalidInputLength
	}

	g := bls12381.NewG1()
	points := make([]*bls12381.PointG1, k)
	scalars := make([]*big.Int, k)
	for i := 0; i < k; i++ {
		off := 160 * i
		t0, t1, t2 := off, off+128, off+160
		point, err := decodeG1PointInSubgroup(g, input[t0:t1])
		if err != nil {
			return nil, err
		}
		points[i] = point
		scalars[i] = new(big.Int).SetBytes(input[t1:t2])
	}

	r := g.New()
	if _, err := g.MultiExp(r, points, scalars); err != nil {
		return nil, err
	}
	return g.EncodePoint(r), nil
}

// bls12381G2Add implements EIP-2537 G2Add precompile.
type bls12381G2Add struct{}

func (c *bls12381G2Add) RequiredGas(input []byte) uint64 {
	return params.Bls12381G2AddGas
}

func (c *bls12381G2Add) Run(input []byte) ([]byte, error) {
	if len(input) != 512 {
		return nil, errBLS12381InvalidInputLength
	}
	g := bls12381.NewG2()
	r := g.New()
	p0, err := g.DecodePoint(input[:256])
	if err != nil {
		return nil, err
	}
	p1, err := g.DecodePoint(input[256:])
	if err != nil {
		return nil, err
	}

	g.Add(r, p0, p1)
	return g.EncodePoint(r), nil
}

// bls12381G2Mul implements EIP-2537 G2Mul precompile.
type bls12381G2Mul struct{}

func (c *bls12381G2Mul) RequiredGas(input []byte) uint64 {
	return params.Bls12381G2MulGas
}

func (c *bls12381G2Mul) Run(input []byte) ([]byte, error) {
	if len(input) != 288 {
		return nil, errBLS12381InvalidInputLength
	}
	g := bls12381.NewG2()
	p0, err := decodeG2PointInSubgroup(g, input[:256])
	if err != nil {
		return nil, err
	}
	e := new(big.Int).SetBytes(input[256:])

	r := g.New()
	g.MulScalar(r, p0, e)
	return g.EncodePoint(r), nil
}

// bls12381G2MultiExp implements EIP-2537 G2MultiExp precompile.
type bls12381G2MultiExp struct{}

func (c *bls12381G2MultiExp) RequiredGas(input []byte) uint64 {
	return bls12381MultiExpGas(len(input), 288, params.Bls12381G2MulGas, params.Bls12381G2MultiExpDiscountTable[:])
}

func (c *bls12381G2MultiExp) Run(input []byte) ([]byte, error) {
	k := len(input) / 288
	if len(input) == 0 || len(input)%288 != 0 {
		return nil, errBLS12381InvalidInputLength
	}

	g := bls12381.NewG2()
	points := make([]*bls12381.PointG2, k)
	scalars := make([]*big.Int, k)
	for i := 0; i < k; i++ {
		off := 288 * i
		t0, t1, t2 := off, off+256, off+288
		point, err := decodeG2PointInSubgroup(g, input[t0:t1])
		if err != nil {
			return nil, err
		}
		points[i] = point
		scalars[i] = new(big.Int).SetBytes(input[t1:t2])
	}

	r := g.New()
	if _, err := g.MultiExp(r, points, scalars); err != nil {
		return nil, err
	}
	return g.EncodePoint(r), nil
}

// bls12381Pairing implements EIP-2537 Pairing precompile.
type bls12381Pairing struct{}

func (c *bls12381Pairing) RequiredGas(input []byte) uint64 {
	return params.Bls12381PairingBaseGas + uint64(len(input)/384)*params.Bls12381PairingPerPairGas
}

func (c *bls12381Pairing) Run(input []byte) ([]byte, error) {
	k := len(input) / 384
	if len(input) == 0 || len(input)%384 != 0 {
		return nil, errBLS12381InvalidInputLength
	}

	e := bls12381.NewPairingEngine()
	g1, g2 := e.G1, e.G2
	for i := 0; i < k; i++ {
		off := 384 * i
		t0, t1, t2 := off, off+128, off+384
		p1, err := g1.DecodePoint(input[t0:t1])
		if err != nil {
			return nil, err
		}
		p2, err := g2.DecodePoint(input[t1:t2])
		if err != nil {
			return nil, err
		}
		if !g1.InCorrectSubgroup(p1) {
			return nil, errBLS12381G1PointSubgroup
		}
		if !g2.InCorrectSubgroup(p2) {
			return nil, errBLS12381G2PointSubgroup
		}
		e.AddPair(p1, p2)
	}

	out := make([]byte, 32)
	if e.Check() {
		out[31] = 1
	}
	return out, nil
}

// bls12381MapG1 implements EIP-2537 MapG1 precompile.
type bls12381MapG1 struct{}

func (c *bls12381MapG1) RequiredGas(input []byte) uint64 {
	return params.Bls12381MapG1Gas
}

func (c *bls12381MapG1) Run(input []byte) ([]byte, error) {
	if len(input) != 64 {
		return nil, errBLS12381InvalidInputLength
	}
	fe, err := decodeBLS12381FieldElementGnark(input)
	if err != nil {
		return nil, err
	}
	r := gnarkbls12381.MapToG1(fe)
	return encodePointG1Gnark(&r), nil
}

// bls12381MapG2 implements EIP-2537 MapG2 precompile.
type bls12381MapG2 struct{}

func (c *bls12381MapG2) RequiredGas(input []byte) uint64 {
	return params.Bls12381MapG2Gas
}

func (c *bls12381MapG2) Run(input []byte) ([]byte, error) {
	if len(input) != 128 {
		return nil, errBLS12381InvalidInputLength
	}
	c0, err := decodeBLS12381FieldElementGnark(input[:64])
	if err != nil {
		return nil, err
	}
	c1, err := decodeBLS12381FieldElementGnark(input[64:])
	if err != nil {
		return nil, err
	}
	fe := gnarkbls12381.E2{A0: c0, A1: c1}
	r := gnarkbls12381.MapToG2(fe)
	return encodePointG2Gnark(&r), nil
}

func decodeBLS12381FieldElementGnark(in []byte) (fp.Element, error) {
	if len(in) != 64 {
		return fp.Element{}, errors.New("invalid field element length")
	}
	for i := 0; i < 16; i++ {
		if in[i] != byte(0x00) {
			return fp.Element{}, errBLS12381InvalidFieldElementTopBytes
		}
	}
	var res [48]byte
	copy(res[:], in[16:])
	return fp.BigEndian.Element(&res)
}

func encodePointG1Gnark(p *gnarkbls12381.G1Affine) []byte {
	out := make([]byte, 128)
	fp.BigEndian.PutElement((*[fp.Bytes]byte)(out[16:64]), p.X)
	fp.BigEndian.PutElement((*[fp.Bytes]byte)(out[80:128]), p.Y)
	return out
}

func encodePointG2Gnark(p *gnarkbls12381.G2Affine) []byte {
	out := make([]byte, 256)
	fp.BigEndian.PutElement((*[fp.Bytes]byte)(out[16:64]), p.X.A0)
	fp.BigEndian.PutElement((*[fp.Bytes]byte)(out[80:128]), p.X.A1)
	fp.BigEndian.PutElement((*[fp.Bytes]byte)(out[144:192]), p.Y.A0)
	fp.BigEndian.PutElement((*[fp.Bytes]byte)(out[208:256]), p.Y.A1)
	return out
}
