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
	"github.com/n42blockchain/N42/crypto/bls/common"
	blst "github.com/supranational/blst/bindings/go"
)

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
