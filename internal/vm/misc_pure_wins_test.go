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
	"crypto/rand"
	"crypto/sha256"
	"testing"
)

// TestVMTContentStoreRunStateless verifies the stateless contentStore.Run
// always rejects with errCASNoDB, since the precompile requires a
// ContentStoreDB wired in at block-processing time.
func TestVMTContentStoreRunStateless(t *testing.T) {
	cs := &contentStore{}
	if _, err := cs.Run([]byte{casStore, 0x01}); err != errCASNoDB {
		t.Fatalf("expected errCASNoDB, got %v", err)
	}
}

// TestVMTBlobSchedules covers the pure default blob schedule constructors.
func TestVMTBlobSchedules(t *testing.T) {
	cancun := DefaultCancunBlobSchedule()
	if cancun.TargetBlobsPerBlock == 0 {
		t.Fatalf("expected a nonzero Cancun target blobs per block")
	}
	pectra := DefaultPectraBlobSchedule()
	if pectra.MaxBlobsPerBlock == 0 {
		t.Fatalf("expected a nonzero Pectra max blobs per block")
	}
	if pectra.MaxBlobsPerBlock < cancun.MaxBlobsPerBlock {
		t.Fatalf("expected Pectra's blob cap to be >= Cancun's: pectra=%d cancun=%d", pectra.MaxBlobsPerBlock, cancun.MaxBlobsPerBlock)
	}
}

// TestVMTValidateInitCodeSize covers both the Fusaka and pre-Fusaka
// init-code size limits.
func TestVMTValidateInitCodeSize(t *testing.T) {
	if err := ValidateInitCodeSize(1, false); err != nil {
		t.Fatalf("small init code should pass pre-Fusaka: %v", err)
	}
	if err := ValidateInitCodeSize(1, true); err != nil {
		t.Fatalf("small init code should pass under Fusaka: %v", err)
	}
	if err := ValidateInitCodeSize(MaxInitCodeSizeFusaka+1, true); err != ErrMaxInitCodeSizeExceeded {
		t.Fatalf("expected ErrMaxInitCodeSizeExceeded under Fusaka, got %v", err)
	}
}

// TestVMTP256EcrecoverHappyPath generates a real P-256 keypair, signs a
// hash with it, and drives both p256Verify.Run and p256Ecrecover.Run (via
// their exported constructors) end to end, including the recovered public
// key matching the signer's.
func TestVMTP256EcrecoverHappyPath(t *testing.T) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey failed: %v", err)
	}
	msg := sha256.Sum256([]byte("vmT p256 test message"))

	r, s, err := ecdsa.Sign(rand.Reader, priv, msg[:])
	if err != nil {
		t.Fatalf("Sign failed: %v", err)
	}

	// p256Verify.Run: msgHash(32) | r(32) | s(32) | x(32) | y(32)
	verifyInput := make([]byte, 160)
	msg32 := msg
	copy(verifyInput[0:32], msg32[:])
	r.FillBytes(verifyInput[32:64])
	s.FillBytes(verifyInput[64:96])
	priv.PublicKey.X.FillBytes(verifyInput[96:128])
	priv.PublicKey.Y.FillBytes(verifyInput[128:160])

	verifier := GetP256Verify()
	out, err := verifier.Run(verifyInput)
	if err != nil {
		t.Fatalf("p256Verify.Run failed: %v", err)
	}
	if len(out) != 32 || out[31] != 1 {
		t.Fatalf("expected a valid-signature result (32-byte 0x01), got %x", out)
	}
	if gas := verifier.RequiredGas(verifyInput); gas != P256VerifyGas {
		t.Fatalf("expected RequiredGas=%d, got %d", P256VerifyGas, gas)
	}

	// p256Ecrecover.Run: msgHash(32) | r(32) | s(32) | v(1)
	for _, v := range []byte{0, 1} {
		ecrecoverInput := make([]byte, 97)
		copy(ecrecoverInput[0:32], msg32[:])
		r.FillBytes(ecrecoverInput[32:64])
		s.FillBytes(ecrecoverInput[64:96])
		ecrecoverInput[96] = v

		ecrecover := GetP256Ecrecover()
		recovered, err := ecrecover.Run(ecrecoverInput)
		if err != nil {
			t.Fatalf("p256Ecrecover.Run failed for v=%d: %v", v, err)
		}
		if len(recovered) != 64 {
			// Not every v necessarily recovers this exact key (the other
			// candidate's x may not be on-curve, or may recover the wrong
			// key) — this is a best-effort recovery path, so only fail if
			// NEITHER v value recovers the signer's key (checked below).
			continue
		}
		recoveredX := recovered[0:32]
		var expectedX [32]byte
		priv.PublicKey.X.FillBytes(expectedX[:])
		if string(recoveredX) == string(expectedX[:]) {
			return // success: at least one v value recovered the right key
		}
	}
	t.Fatalf("p256Ecrecover did not recover the signer's public key for either v value")
}

// TestVMTP256EcrecoverInvalidV verifies the v-must-be-0-or-1 guard.
func TestVMTP256EcrecoverInvalidV(t *testing.T) {
	input := make([]byte, 97)
	input[96] = 2 // invalid
	ecrecover := GetP256Ecrecover()
	out, err := ecrecover.Run(input)
	if err != nil {
		t.Fatalf("expected a nil-error/nil-output rejection, got err=%v", err)
	}
	if out != nil {
		t.Fatalf("expected nil output for invalid v, got %x", out)
	}
}
