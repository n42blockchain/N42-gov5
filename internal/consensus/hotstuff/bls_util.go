// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// BLS signature verification helper for the HotStuff engine.
// VerifyBLSSignature parses raw signature bytes via bls.SignatureFromBytes
// and calls Signature.Verify against a public key and message. Returns
// false on any decode or verification error so callers can treat the
// result as a boolean accept/reject gate in the quorum pipeline.

package hotstuff

import (
	"sync/atomic"

	"github.com/n42blockchain/N42/crypto/bls"
	"github.com/n42blockchain/N42/crypto/bls/common"
)

// VerifyBLSSignature verifies a BLS signature given raw signature bytes, a public key, and message.
func VerifyBLSSignature(sigBytes []byte, pk common.PublicKey, message []byte) bool {
	sig, err := bls.SignatureFromBytes(sigBytes)
	if err != nil {
		return false
	}
	notePairingCheck()
	return sig.Verify(pk, message)
}

// pairingChecks counts the BLS pairing checks (one final exponentiation
// each: a single verify, an aggregate verify, or one batched multi-pairing)
// this package has performed. It is a measurement hook for the scale tests
// (AM22(b)); production code never reads it.
var pairingChecks atomic.Uint64

func notePairingCheck() { pairingChecks.Add(1) }

// PairingChecks returns the process-wide number of BLS pairing checks the
// consensus engine has run so far (test and diagnostics hook).
func PairingChecks() uint64 { return pairingChecks.Load() }
