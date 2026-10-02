package enode

import (
	"crypto/ecdsa"
	"encoding/hex"
	"net"
	"testing"

	"github.com/n42blockchain/N42/crypto"
)

func fmtHex(b []byte) string { return hex.EncodeToString(b) }

func testKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	return key
}

func TestNewV4AndURLv4RoundTrip(t *testing.T) {
	key := testKey(t)
	n := NewV4(&key.PublicKey, net.ParseIP("10.0.0.1"), 30303, 30301)

	if n.IP().String() != "10.0.0.1" {
		t.Fatalf("IP() = %v, want 10.0.0.1", n.IP())
	}
	if n.TCP() != 30303 {
		t.Fatalf("TCP() = %d, want 30303", n.TCP())
	}
	if n.UDP() != 30301 {
		t.Fatalf("UDP() = %d, want 30301", n.UDP())
	}
	if n.Incomplete() {
		t.Fatal("expected a complete node (has IP)")
	}

	url := n.URLv4()
	parsed, err := ParseV4(url)
	if err != nil {
		t.Fatalf("ParseV4(%q): %v", url, err)
	}
	if parsed.ID() != n.ID() {
		t.Fatalf("round-tripped ID mismatch: got %v, want %v", parsed.ID(), n.ID())
	}
	if parsed.IP().String() != "10.0.0.1" {
		t.Fatalf("round-tripped IP = %v, want 10.0.0.1", parsed.IP())
	}
	if parsed.TCP() != 30303 || parsed.UDP() != 30301 {
		t.Fatalf("round-tripped ports = tcp:%d udp:%d, want 30303/30301", parsed.TCP(), parsed.UDP())
	}
}

func TestNewV4Incomplete(t *testing.T) {
	key := testKey(t)
	n := NewV4(&key.PublicKey, nil, 0, 0)
	if !n.Incomplete() {
		t.Fatal("expected incomplete node with no IP")
	}
	url := n.URLv4()
	parsed, err := ParseV4(url)
	if err != nil {
		t.Fatalf("ParseV4(%q): %v", url, err)
	}
	if parsed.ID() != n.ID() {
		t.Fatal("ID mismatch for incomplete node round-trip")
	}
}

func TestParseV4BareHexID(t *testing.T) {
	key := testKey(t)
	hexKey := fmtHex(crypto.FromECDSAPub(&key.PublicKey)[1:])

	// A bare hex public key (no "enode://" prefix, no host) parses as an
	// incomplete node.
	parsed, err := ParseV4(hexKey)
	if err != nil {
		t.Fatalf("ParseV4(bare hex): %v", err)
	}
	if !parsed.Incomplete() {
		t.Fatal("expected incomplete node from bare hex ID")
	}
}

func TestMustParseV4Panics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for invalid URL")
		}
	}()
	MustParseV4("not-a-valid-url")
}

func TestParseV4Errors(t *testing.T) {
	cases := []string{
		"",
		"http://example.com",     // wrong scheme
		"enode://zz@127.0.0.1:1", // invalid pubkey hex
	}
	for _, c := range cases {
		if _, err := ParseV4(c); err == nil {
			t.Errorf("ParseV4(%q): expected error", c)
		}
	}
}

func TestIsNewV4(t *testing.T) {
	key := testKey(t)
	n := NewV4(&key.PublicKey, net.ParseIP("1.2.3.4"), 1, 2)
	if !isNewV4(n) {
		t.Fatal("expected isNewV4 to be true for a NewV4-constructed node")
	}
}

func TestPubkeyToIDV4Deterministic(t *testing.T) {
	key := testKey(t)
	id1 := PubkeyToIDV4(&key.PublicKey)
	id2 := PubkeyToIDV4(&key.PublicKey)
	if id1 != id2 {
		t.Fatal("expected PubkeyToIDV4 to be deterministic")
	}
}
