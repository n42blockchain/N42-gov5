package secp256r1

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"math/big"
)

// newPublicKey constructs a P-256 public key from the given coordinates.
// Returns nil if the coordinates are invalid or represent the point at infinity.
func newPublicKey(x, y *big.Int) *ecdsa.PublicKey {
	if x == nil || y == nil || !elliptic.P256().IsOnCurve(x, y) {
		return nil
	}
	if x.Sign() == 0 && y.Sign() == 0 {
		return nil
	}
	return &ecdsa.PublicKey{
		Curve: elliptic.P256(),
		X:     x,
		Y:     y,
	}
}
