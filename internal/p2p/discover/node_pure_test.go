package discover

import (
	"crypto/elliptic"
	"net"
	"testing"

	"github.com/n42blockchain/N42/crypto"
)

func TestEncodeDecodePubkeyRoundTrip(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	enc := encodePubkey(&key.PublicKey)
	dec, err := decodePubkey(elliptic.P256(), enc[:])
	// The local curve (secp256k1) differs from P256, so this specific pair
	// is expected to fail the on-curve check; verify we get the documented
	// error instead of garbage output.
	if err == nil {
		t.Fatalf("expected decodePubkey with mismatched curve to fail, got %v", dec)
	}

	dec2, err := decodePubkey(key.Curve, enc[:])
	if err != nil {
		t.Fatalf("decodePubkey with matching curve: %v", err)
	}
	if dec2.X.Cmp(key.X) != 0 || dec2.Y.Cmp(key.Y) != 0 {
		t.Fatal("decoded public key does not match original")
	}
}

func TestDecodePubkeyWrongLength(t *testing.T) {
	if _, err := decodePubkey(elliptic.P256(), []byte{1, 2, 3}); err == nil {
		t.Fatal("expected error for wrong-length pubkey data")
	}
}

func TestEncPubkeyID(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	enc := encodePubkey(&key.PublicKey)
	id1 := enc.id()
	id2 := enc.id()
	if id1 != id2 {
		t.Fatal("expected encPubkey.id() to be deterministic")
	}
}

func TestNodeAddrAndString(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	n := wrapNode(nodeAtDistance(encodePubkey(&key.PublicKey).id(), 10, net.ParseIP("1.2.3.4")))
	a := n.addr()
	if a.IP.String() != "1.2.3.4" {
		t.Fatalf("addr().IP = %v, want 1.2.3.4", a.IP)
	}
	if n.String() == "" {
		t.Fatal("expected non-empty String()")
	}
}
