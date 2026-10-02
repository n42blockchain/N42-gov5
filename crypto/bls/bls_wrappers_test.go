//go:build ((linux && amd64) || (linux && arm64) || (darwin && amd64) || (darwin && arm64) || (windows && amd64)) && !blst_disabled

// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.

package bls

import (
	"testing"

	"github.com/n42blockchain/N42/crypto/bls/common"
	"github.com/stretchr/testify/require"
)

// TestPublicKeyFromBytesWrapper exercises the thin wrapper functions in
// bls.go that were previously untested (they just forward to the blst
// package, but zero coverage here meant a renamed/removed blst function
// would not be caught at this layer).
func TestPublicKeyFromBytesWrapper(t *testing.T) {
	priv, err := RandKey()
	require.NoError(t, err)
	pubBytes := priv.PublicKey().Marshal()

	pub, err := PublicKeyFromBytes(pubBytes)
	require.NoError(t, err)
	require.Equal(t, pubBytes, pub.Marshal())

	if _, err := PublicKeyFromBytes([]byte{0x01, 0x02}); err == nil {
		t.Error("expected error for undersized public key bytes")
	}
}

func TestPrecomputeHashWrapper(t *testing.T) {
	msg := []byte("precompute me")
	ph := PrecomputeHash(msg)
	if ph == nil {
		t.Fatal("PrecomputeHash returned nil")
	}
}

func TestAggregatePublicKeysWrapper(t *testing.T) {
	var raw [][]byte
	var pubs []common.PublicKey
	for i := 0; i < 5; i++ {
		priv, err := RandKey()
		require.NoError(t, err)
		pub := priv.PublicKey()
		raw = append(raw, pub.Marshal())
		pubs = append(pubs, pub)
	}

	agg, err := AggregatePublicKeys(raw)
	require.NoError(t, err)

	agg2 := AggregateMultiplePubkeys(pubs)
	require.Equal(t, agg.Marshal(), agg2.Marshal())

	if _, err := AggregatePublicKeys([][]byte{{0x01}}); err == nil {
		t.Error("expected error aggregating malformed public key bytes")
	}
}

func TestSecretKeyFromRandom32ByteWrapper(t *testing.T) {
	var ikm [32]byte
	for i := range ikm {
		ikm[i] = byte(i + 1)
	}
	sk1, err := SecretKeyFromRandom32Byte(ikm)
	require.NoError(t, err)
	sk2, err := SecretKeyFromRandom32Byte(ikm)
	require.NoError(t, err)
	require.Equal(t, sk1.Marshal(), sk2.Marshal(), "same seed should produce the same key deterministically")

	var otherIkm [32]byte
	otherIkm[0] = 0xff
	sk3, err := SecretKeyFromRandom32Byte(otherIkm)
	require.NoError(t, err)
	require.NotEqual(t, sk1.Marshal(), sk3.Marshal())
}

func TestSignatureBatchJoinAndVerify(t *testing.T) {
	set1 := NewSet()
	set2 := NewSet()

	for _, set := range []*SignatureBatch{set1, set2} {
		for i := 0; i < 3; i++ {
			priv, err := RandKey()
			require.NoError(t, err)
			var msg [32]byte
			msg[0] = byte(i + 1)
			sig := priv.Sign(msg[:])
			set.Signatures = append(set.Signatures, sig.Marshal())
			set.PublicKeys = append(set.PublicKeys, priv.PublicKey())
			set.Messages = append(set.Messages, msg)
		}
	}

	joined := set1.Join(set2)
	if len(joined.Signatures) != 6 {
		t.Fatalf("Join: got %d signatures, want 6", len(joined.Signatures))
	}

	ok, err := joined.Verify()
	require.NoError(t, err)
	if !ok {
		t.Error("expected joined batch to verify")
	}

	// Corrupt one message; verification must now fail.
	corrupted := joined.Copy()
	corrupted.Messages[0][0] ^= 0xff
	ok, err = corrupted.Verify()
	if err == nil && ok {
		t.Error("expected corrupted batch to fail verification")
	}
}

// TestLegacySelfCheckFunctions calls the lbs_ntest.go self-check helpers
// directly. These are ordinary (error-returning) functions, not go test
// functions despite their "Test...2" names and file name; they duplicate
// the assertions in lbs_test.go using panic/assert.Equal-based
// self-verification. Running them here is the only way to exercise that
// code path under `go test -cover`.
func TestLegacySelfCheckFunctions(t *testing.T) {
	checks := map[string]func() error{
		"SignVerify2":                                     TestSignVerify2,
		"AggregateVerify2":                                TestAggregateVerify2,
		"AggregateVerify_CompressedSignatures2":           TestAggregateVerify_CompressedSignatures2,
		"FastAggregateVerify2":                            TestFastAggregateVerify2,
		"VerifyCompressed2":                                TestVerifyCompressed2,
		"MultipleSignatureVerification2":                  TestMultipleSignatureVerification2,
		"FastAggregateVerify_ReturnsFalseOnEmptyPubKeyList2": TestFastAggregateVerify_ReturnsFalseOnEmptyPubKeyList2,
		"Eth2FastAggregateVerify2":                         TestEth2FastAggregateVerify2,
		"Eth2FastAggregateVerify_ReturnsFalseOnEmptyPubKeyList2": TestEth2FastAggregateVerify_ReturnsFalseOnEmptyPubKeyList2,
		"Eth2FastAggregateVerify_ReturnsTrueOnG2PointAtInfinity2": TestEth2FastAggregateVerify_ReturnsTrueOnG2PointAtInfinity2,
		"SignatureFromBytes2":                              TestSignatureFromBytes2,
		"MultipleSignatureFromBytes2":                      TestMultipleSignatureFromBytes2,
		"Copy2":                                            TestCopy2,
		"SecretKeyFromBytes2":                               TestSecretKeyFromBytes2,
	}
	for name, fn := range checks {
		t.Run(name, func(t *testing.T) {
			if err := fn(); err != nil {
				t.Errorf("%s() = %v, want nil", name, err)
			}
		})
	}
}
