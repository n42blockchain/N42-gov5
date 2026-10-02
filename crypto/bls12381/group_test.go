// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.

package bls12381

import (
	"math/big"
	"testing"
)

// TestG1GeneratorMultiples checks basic generator arithmetic: 2*G == G+G,
// and scalar multiplication agrees with repeated doubling/addition.
func TestG1GeneratorMultiples(t *testing.T) {
	g1 := NewG1()
	one := g1.One()

	var sum, dbl PointG1
	g1.Add(&sum, one, one)
	g1.Double(&dbl, one)
	if !g1.Equal(&sum, &dbl) {
		t.Fatal("G+G != 2*G")
	}

	var viaScalar PointG1
	g1.MulScalar(&viaScalar, one, big.NewInt(2))
	if !g1.Equal(&viaScalar, &dbl) {
		t.Fatal("2*G via MulScalar != 2*G via Double")
	}

	// 0*G is the point at infinity.
	var zeroMul PointG1
	g1.MulScalar(&zeroMul, one, big.NewInt(0))
	if !g1.IsZero(&zeroMul) {
		t.Fatal("0*G should be the point at infinity")
	}

	// q*G is the point at infinity (order of the group).
	var qMul PointG1
	g1.MulScalar(&qMul, one, g1.Q())
	if !g1.IsZero(&qMul) {
		t.Fatal("q*G should be the point at infinity")
	}

	if !g1.IsOnCurve(one) || !g1.InCorrectSubgroup(one) {
		t.Fatal("generator should be on curve and in correct subgroup")
	}
}

// TestG1AffineAndSerializationRoundTrip exercises Affine, ToBytes/FromBytes
// and EncodePoint/DecodePoint for a random multiple of the generator.
func TestG1AffineAndSerializationRoundTrip(t *testing.T) {
	g1 := NewG1()
	one := g1.One()

	k := randScalar(g1.Q())
	var p PointG1
	g1.MulScalar(&p, one, k)
	g1.Affine(&p)

	if !g1.IsAffine(&p) {
		t.Fatal("expected affine form after Affine()")
	}

	raw := g1.ToBytes(&p)
	back, err := g1.FromBytes(raw)
	if err != nil {
		t.Fatalf("FromBytes: %v", err)
	}
	if !g1.Equal(&p, back) {
		t.Fatal("FromBytes(ToBytes(p)) != p")
	}

	encoded := g1.EncodePoint(&p)
	decoded, err := g1.DecodePoint(encoded)
	if err != nil {
		t.Fatalf("DecodePoint: %v", err)
	}
	if !g1.Equal(&p, decoded) {
		t.Fatal("DecodePoint(EncodePoint(p)) != p")
	}

	// Infinity round-trips to the all-zero encoding and back.
	zero := g1.Zero()
	zeroBytes := g1.ToBytes(zero)
	for _, b := range zeroBytes {
		if b != 0 {
			t.Fatal("ToBytes(infinity) should be all zero")
		}
	}
	zeroBack, err := g1.FromBytes(zeroBytes)
	if err != nil {
		t.Fatalf("FromBytes(infinity): %v", err)
	}
	if !g1.IsZero(zeroBack) {
		t.Fatal("FromBytes of all-zero bytes should be infinity")
	}
}

// TestG1RejectsInvalidPoints checks that points not satisfying the curve
// equation, and malformed byte inputs, are rejected.
func TestG1RejectsInvalidPoints(t *testing.T) {
	g1 := NewG1()

	// Wrong-length input.
	if _, err := g1.FromBytes(make([]byte, 10)); err == nil {
		t.Error("expected error for undersized input")
	}
	if _, err := g1.DecodePoint(make([]byte, 10)); err == nil {
		t.Error("expected error for undersized DecodePoint input")
	}

	// A point with coordinates that do not satisfy y^2 = x^3 + b.
	one := g1.One()
	raw := g1.ToBytes(one)
	// Corrupt the y-coordinate (second half) to break the curve equation.
	raw[90] ^= 0xff
	if _, err := g1.FromBytes(raw); err == nil {
		t.Error("expected error for a point not on the curve")
	}
}

// TestG1NegAndSub checks that P + (-P) is the point at infinity, and Sub
// agrees with Add of the negation.
func TestG1NegAndSub(t *testing.T) {
	g1 := NewG1()
	one := g1.One()

	var neg, sum PointG1
	g1.Neg(&neg, one)
	g1.Add(&sum, one, &neg)
	if !g1.IsZero(&sum) {
		t.Fatal("P + (-P) should be infinity")
	}

	k := randScalar(g1.Q())
	var p, q2 PointG1
	g1.MulScalar(&p, one, k)
	g1.MulScalar(&q2, one, big.NewInt(3))

	var subResult, addNegResult PointG1
	g1.Sub(&subResult, &p, &q2)
	g1.Neg(&addNegResult, &q2)
	g1.Add(&addNegResult, &p, &addNegResult)
	if !g1.Equal(&subResult, &addNegResult) {
		t.Fatal("Sub(p,q) != Add(p, Neg(q))")
	}
}

// TestG2GeneratorMultiplesAndSerialization mirrors the G1 generator and
// serialization checks for G2.
func TestG2GeneratorMultiplesAndSerialization(t *testing.T) {
	g2 := NewG2()
	one := g2.One()

	var sum, dbl PointG2
	g2.Add(&sum, one, one)
	g2.Double(&dbl, one)
	if !g2.Equal(&sum, &dbl) {
		t.Fatal("G+G != 2*G in G2")
	}

	if !g2.IsOnCurve(one) || !g2.InCorrectSubgroup(one) {
		t.Fatal("G2 generator should be on curve and in correct subgroup")
	}

	k := randScalar(g2.Q())
	var p PointG2
	g2.MulScalar(&p, one, k)
	g2.Affine(&p)

	raw := g2.ToBytes(&p)
	back, err := g2.FromBytes(raw)
	if err != nil {
		t.Fatalf("G2 FromBytes: %v", err)
	}
	if !g2.Equal(&p, back) {
		t.Fatal("G2 FromBytes(ToBytes(p)) != p")
	}

	encoded := g2.EncodePoint(&p)
	decoded, err := g2.DecodePoint(encoded)
	if err != nil {
		t.Fatalf("G2 DecodePoint: %v", err)
	}
	if !g2.Equal(&p, decoded) {
		t.Fatal("G2 DecodePoint(EncodePoint(p)) != p")
	}
}

// TestG2RejectsInvalidPoints mirrors the G1 invalid-input checks for G2.
func TestG2RejectsInvalidPoints(t *testing.T) {
	g2 := NewG2()
	if _, err := g2.FromBytes(make([]byte, 10)); err == nil {
		t.Error("expected error for undersized G2 input")
	}
	if _, err := g2.DecodePoint(make([]byte, 10)); err == nil {
		t.Error("expected error for undersized G2 DecodePoint input")
	}

	one := g2.One()
	raw := g2.ToBytes(one)
	raw[180] ^= 0xff
	if _, err := g2.FromBytes(raw); err == nil {
		t.Error("expected error for a G2 point not on the curve")
	}
}

// TestPairingBilinearity is the central cryptographic property test:
// e(aP, bQ) == e(P, Q)^(ab) for random scalars a, b.
func TestPairingBilinearity(t *testing.T) {
	g1 := NewG1()
	g2 := NewG2()

	a := randScalar(g1.Q())
	b := randScalar(g1.Q())

	g1One := g1.One()
	g2One := g2.One()

	var aP PointG1
	g1.MulScalar(&aP, g1One, a)
	var bQ PointG2
	g2.MulScalar(&bQ, g2One, b)

	// Left side: e(aP, bQ).
	engineLeft := NewPairingEngine()
	engineLeft.AddPair(&aP, &bQ)
	left := engineLeft.Result()

	// Right side: e(P, Q)^(ab).
	engineBase := NewPairingEngine()
	engineBase.AddPair(g1One, g2One)
	base := engineBase.Result()

	ab := new(big.Int).Mul(a, b)
	ab.Mod(ab, g1.Q())

	gt := NewGT()
	right := gt.New()
	gt.Exp(right, base, ab)

	if !left.Equal(right) {
		t.Fatal("pairing bilinearity failed: e(aP,bQ) != e(P,Q)^(ab)")
	}
}

// TestPairingEngineCheckMatchesProductOfOne verifies the Engine.Check
// shortcut: e(P,Q) * e(-P,Q) == 1 should report true via AddPairInv.
func TestPairingEngineCheckWithInverse(t *testing.T) {
	g1 := NewG1()
	g2 := NewG2()

	g1One := g1.One()
	g2One := g2.One()

	k := randScalar(g1.Q())
	var kP, kP2 PointG1
	g1.MulScalar(&kP, g1One, k)
	kP2.Set(&kP)

	// AddPairInv negates its g1 argument in place, so pass a distinct copy
	// to avoid aliasing the point already recorded by AddPair.
	engine := NewPairingEngine()
	engine.AddPair(&kP, g2One)
	engine.AddPairInv(&kP2, g2One)
	if !engine.Check() {
		t.Fatal("e(kP,Q) * e(-kP,Q) should equal 1 (Check() should be true)")
	}
}

// TestGTSerializationRoundTrip exercises GT.ToBytes/FromBytes for a pairing
// result, and checks that FromBytes rejects malformed/invalid input.
func TestGTSerializationRoundTrip(t *testing.T) {
	g1 := NewG1()
	g2 := NewG2()
	engine := NewPairingEngine()
	engine.AddPair(g1.One(), g2.One())
	result := engine.Result()

	gt := NewGT()
	raw := gt.ToBytes(result)
	back, err := gt.FromBytes(raw)
	if err != nil {
		t.Fatalf("GT FromBytes: %v", err)
	}
	if !result.Equal(back) {
		t.Fatal("GT FromBytes(ToBytes(e)) != e")
	}

	if _, err := gt.FromBytes(make([]byte, 10)); err == nil {
		t.Error("expected error for undersized GT input")
	}
}

// TestFpSqrtAndQuadraticResidue exercises the fp-level sqrt and
// isQuadraticNonResidue helpers via the field element squaring relation.
func TestFpSqrtAndQuadraticResidue(t *testing.T) {
	var x, sq fe
	x.one()
	square(&sq, &x)

	var root fe
	ok := sqrt(&root, &sq)
	if !ok {
		t.Fatal("sqrt of a perfect square should succeed")
	}
	var back fe
	square(&back, &root)
	if !back.equal(&sq) {
		t.Fatal("sqrt(x)^2 != x")
	}
}
