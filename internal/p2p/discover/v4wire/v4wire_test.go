package v4wire

import "testing"

func TestEncodePubkeyNilReturnsZeroValue(t *testing.T) {
	got := EncodePubkey(nil)
	if got != (Pubkey{}) {
		t.Fatalf("EncodePubkey(nil) = %x, want zero value", got)
	}
}
