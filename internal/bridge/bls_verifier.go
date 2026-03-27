// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package bridge

import (
	"fmt"

	"github.com/n42blockchain/N42/crypto/bls"
	blscommon "github.com/n42blockchain/N42/crypto/bls/common"
)

// BLSVerifierImpl implements SyncCommitteeBLSVerifier using the N42 BLS12-381 stack.
// Used by the ETH light client to verify sync committee aggregate signatures.
type BLSVerifierImpl struct{}

// VerifySyncCommitteeSignature verifies a BLS12-381 aggregate signature
// from ETH sync committee members against the signing root.
func (v *BLSVerifierImpl) VerifySyncCommitteeSignature(
	pubKeys []blscommon.PublicKey,
	sigBytes []byte,
	signingRoot [32]byte,
) error {
	if len(pubKeys) == 0 {
		return fmt.Errorf("no public keys provided")
	}
	if len(sigBytes) == 0 {
		return fmt.Errorf("empty signature")
	}

	sig, err := bls.SignatureFromBytes(sigBytes)
	if err != nil {
		return fmt.Errorf("deserialize BLS signature: %w", err)
	}

	if !sig.FastAggregateVerify(pubKeys, signingRoot) {
		return fmt.Errorf("BLS aggregate signature verification failed (%d signers)", len(pubKeys))
	}

	return nil
}
