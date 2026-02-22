package secp256r1

import (
	"crypto/ecdsa"
	"math/big"
)

// Verify checks the given ECDSA signature (r, s) against the hash using the P-256 public key (x, y).
func Verify(hash []byte, r, s, x, y *big.Int) bool {
	publicKey := newPublicKey(x, y)
	if publicKey == nil {
		return false
	}
	return ecdsa.Verify(publicKey, hash, r, s)
}
