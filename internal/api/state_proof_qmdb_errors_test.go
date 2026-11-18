package api

import (
	"testing"

	"github.com/n42blockchain/N42/internal"
)

// QMDB provider errors (out-of-window heights, root-check failures) must reach
// the caller; the keccak placeholder must never stand in for a QMDB proof.
func TestQMDBProofErrorsAreFatal(t *testing.T) {
	qp := internal.NewQMDBStateProofProvider(nil)
	if !proofProviderErrorsAreFatal(qp) {
		t.Fatal("QMDB provider errors must be fatal")
	}
	if !proofProviderIsQMDB(qp) {
		t.Fatal("QMDB provider not detected")
	}
	if proofProviderErrorsAreFatal(nil) || proofProviderIsQMDB(nil) {
		t.Fatal("nil provider must keep the hash-only fallback")
	}
}
