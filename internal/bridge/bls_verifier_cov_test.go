// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// bls_verifier_cov_test.go exercises BLSVerifierImpl's error paths (empty
// pubkeys, empty signature, malformed signature bytes) and the happy path
// using the real N42 BLS12-381 stack — all in-process, no network.

package bridge

import (
	"testing"

	"github.com/n42blockchain/N42/crypto/bls"
	blscommon "github.com/n42blockchain/N42/crypto/bls/common"
)

func TestBLSVerifierImpl_EmptyPubKeys(t *testing.T) {
	v := &BLSVerifierImpl{}
	err := v.VerifySyncCommitteeSignature(nil, []byte{0x1}, [32]byte{})
	if err == nil || err.Error() != "no public keys provided" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBLSVerifierImpl_EmptySignature(t *testing.T) {
	v := &BLSVerifierImpl{}
	sk, err := bls.RandKey()
	if err != nil {
		t.Fatalf("bls.RandKey: %v", err)
	}
	err = v.VerifySyncCommitteeSignature([]blscommon.PublicKey{sk.PublicKey()}, nil, [32]byte{})
	if err == nil || err.Error() != "empty signature" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBLSVerifierImpl_MalformedSignature(t *testing.T) {
	v := &BLSVerifierImpl{}
	sk, err := bls.RandKey()
	if err != nil {
		t.Fatalf("bls.RandKey: %v", err)
	}
	err = v.VerifySyncCommitteeSignature([]blscommon.PublicKey{sk.PublicKey()}, []byte{0x1, 0x2}, [32]byte{})
	if err == nil {
		t.Fatal("expected deserialization error for malformed signature")
	}
}

func TestBLSVerifierImpl_SignAndVerify(t *testing.T) {
	v := &BLSVerifierImpl{}
	sk, err := bls.RandKey()
	if err != nil {
		t.Fatalf("bls.RandKey: %v", err)
	}
	var signingRoot [32]byte
	signingRoot[0] = 0x55

	sig := sk.Sign(signingRoot[:])
	if err := v.VerifySyncCommitteeSignature([]blscommon.PublicKey{sk.PublicKey()}, sig.Marshal(), signingRoot); err != nil {
		t.Fatalf("expected valid signature to verify: %v", err)
	}

	// Wrong signing root should fail.
	var wrongRoot [32]byte
	wrongRoot[0] = 0x99
	if err := v.VerifySyncCommitteeSignature([]blscommon.PublicKey{sk.PublicKey()}, sig.Marshal(), wrongRoot); err == nil {
		t.Fatal("expected verification failure for wrong signing root")
	}
}
