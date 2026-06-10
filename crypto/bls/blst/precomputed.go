// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Shared-hash batch signing. When many committee members sign the SAME message
// (the BLS re-seal use case: 512 members all sign SigningMessage(view,hash) per
// block), the expensive hash-to-curve (HashToG2, ~half of blst sign cost) is
// identical for every signer. PrecomputeHash hashes the message to G2 once; each
// signer then only performs the scalar multiplication sk * H(msg). The result is
// byte-identical to calling sk.Sign(msg) individually (a BLS signature is exactly
// sk * H(msg)). q is read-only after construction, so a single PrecomputedHash is
// safe to share across signing goroutines.

package blst

import (
	"math/big"

	"github.com/n42blockchain/N42/crypto/bls/common"
	blst "github.com/supranational/blst/bindings/go"
)

// blsScalarOrder is r, the BLS12-381 scalar field modulus (the group order of
// G1/G2). Secret keys are scalars mod r.
var blsScalarOrder, _ = new(big.Int).SetString(
	"73eda753299d7d483339d80809a1d80553bda402fffe5bfeffffffff00000001", 16)

// PrecomputedHash is a message pre-hashed to G2, reusable across many signers of
// the same message.
type PrecomputedHash struct {
	q   *blst.P2
	msg []byte
}

// PrecomputeHash hashes msg to G2 once (the dominant cost in BLS signing) so it
// can be reused by every signer of the same message.
func PrecomputeHash(msg []byte) *PrecomputedHash {
	return &PrecomputedHash{q: blst.HashToG2(msg, dst), msg: msg}
}

// SignWith returns sk * H(msg) using the precomputed hash point. It is
// byte-identical to sk.Sign(msg). Safe to call concurrently with the same
// PrecomputedHash: q is only read, each call writes its own accumulator.
// Falls back to sk.Sign(msg) for any non-blst secret key implementation.
func (h *PrecomputedHash) SignWith(sk common.SecretKey) common.Signature {
	k, ok := sk.(*bls12SecretKey)
	if !ok {
		return sk.Sign(h.msg)
	}
	acc := new(blst.P2)
	acc.MultNAccumulate(h.q, k.p)
	return &Signature{s: acc.ToAffine()}
}

// AggregateSignWith returns the AGGREGATE signature of all sks over the
// precomputed message hash using a single curve multiplication:
//
//	Σ(sk_i · H(m)) == (Σ sk_i mod r) · H(m)
//
// — the scalar sum costs only field additions, so signing a 512-member
// committee collapses from 512 G2 multiplications to one. The result is
// byte-identical to aggregating the individual SignWith signatures (the
// aggregate of points equals the point of the summed scalars; asserted by
// tests). This shortcut requires holding EVERY participant's secret key, so it
// is only valid for the reseal SIMULATOR — a real distributed committee cannot
// do this, and the produced chain is indistinguishable either way.

// SignWithScalarSum signs with an already-summed committee scalar (32-byte
// big-endian, reduced mod r) — the zero-serialization fast path for callers
// that cache per-key scalars (the reseal simulator caches the whole pool's
// scalars as big.Ints at construction; per block it only does field additions).
// Byte-identical to AggregateSignWith over the same keys (asserted by tests).
func (h *PrecomputedHash) SignWithScalarSum(sum32 [32]byte) common.Signature {
	var sc blst.Scalar
	sc.Deserialize(sum32[:])
	acc := new(blst.P2)
	acc.MultNAccumulate(h.q, &sc)
	return &Signature{s: acc.ToAffine()}
}

// SumKeyScalars reduces a scalar sum into the 32-byte form SignWithScalarSum
// expects. sum may exceed r; it is reduced here.
func SumKeyScalars(sum *big.Int) [32]byte {
	var out [32]byte
	new(big.Int).Mod(sum, blsScalarOrder).FillBytes(out[:])
	return out
}

// AggregateSignWith aggregates via the scalar sum (see package comment above);
// falls back to individual signing + aggregation if any key is not blst-backed.
func (h *PrecomputedHash) AggregateSignWith(sks []common.SecretKey) common.Signature {
	sum := new(big.Int)
	for _, sk := range sks {
		k, ok := sk.(*bls12SecretKey)
		if !ok {
			sigs := make([]common.Signature, len(sks))
			for i, s := range sks {
				sigs[i] = h.SignWith(s)
			}
			return AggregateSignatures(sigs)
		}
		sum.Add(sum, new(big.Int).SetBytes(k.Marshal()))
	}
	sum.Mod(sum, blsScalarOrder)
	var sc blst.Scalar
	sc.Deserialize(sum.FillBytes(make([]byte, 32)))
	acc := new(blst.P2)
	acc.MultNAccumulate(h.q, &sc)
	return &Signature{s: acc.ToAffine()}
}
