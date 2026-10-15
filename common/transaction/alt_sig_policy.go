// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package transaction

import (
	"bytes"
	"crypto/ed25519"
	"fmt"

	"filippo.io/edwards25519"

	"github.com/n42blockchain/N42/common/types"
)

// AltSigRuleID names this Go acceptance policy, which is deliberately distinct
// from Rust's cofactored equation. It does not activate runtime transaction rules.
const AltSigRuleID = "gov5-ed25519-canonical-a-stdlib-v1"

func validateAltSigPublicKey(publicKey []byte) error {
	point, err := new(edwards25519.Point).SetBytes(publicKey)
	if err != nil {
		return fmt.Errorf("invalid Ed25519 public point: %w", err)
	}
	// SetBytes deliberately accepts noncanonical aliases; the protocol does not.
	if !bytes.Equal(point.Bytes(), publicKey) {
		return fmt.Errorf("noncanonical Ed25519 public key")
	}
	if new(edwards25519.Point).MultByCofactor(point).Equal(edwards25519.NewIdentityPoint()) == 1 {
		return fmt.Errorf("small-order Ed25519 public key")
	}
	return nil
}

// VerifyAltSigOffline authenticates a carrier under AltSigRuleID and the caller's
// expected chain ID. No runtime Signer, admission path or sender cache is enabled.
// The public point must decode canonically and satisfy [8]A != identity; the
// signature then uses crypto/ed25519.Verify (canonical S and cofactorless equation).
func VerifyAltSigOffline(tx *Transaction, expectedChainID uint64) (types.Address, error) {
	if tx == nil {
		return types.Address{}, fmt.Errorf("missing alt signature transaction")
	}
	inner, ok := tx.inner.(*AltSigTx)
	if !ok {
		return types.Address{}, fmt.Errorf("not an alt signature transaction")
	}
	if inner.ChainID != expectedChainID {
		return types.Address{}, fmt.Errorf("alt signature chain ID mismatch")
	}
	if err := validateAltSigPublicKey(inner.PublicKey[:]); err != nil {
		return types.Address{}, err
	}
	hash, err := AltSigSigningHash(inner)
	if err != nil {
		return types.Address{}, err
	}
	if !ed25519.Verify(inner.PublicKey[:], hash[:], inner.Signature[:]) {
		return types.Address{}, fmt.Errorf("invalid Ed25519 signature")
	}
	return AltSigAddress(inner.PublicKey), nil
}
