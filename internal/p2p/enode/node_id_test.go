package enode

import (
	"strings"
	"testing"

	"github.com/n42blockchain/N42/internal/p2p/enr"
)

func TestHexIDAndParseID(t *testing.T) {
	id := ID{1, 2, 3, 4}
	hex := id.String()

	got, err := ParseID(hex)
	if err != nil {
		t.Fatalf("ParseID: %v", err)
	}
	if got != id {
		t.Fatalf("ParseID round trip = %v, want %v", got, id)
	}

	// 0x-prefixed form must also work.
	got2, err := ParseID("0x" + hex)
	if err != nil {
		t.Fatalf("ParseID(0x-prefixed): %v", err)
	}
	if got2 != id {
		t.Fatalf("ParseID(0x-prefixed) = %v, want %v", got2, id)
	}

	if HexID(hex) != id {
		t.Fatal("HexID mismatch")
	}
}

func TestHexIDPanicsOnInvalid(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for invalid hex ID")
		}
	}()
	HexID("not-hex")
}

func TestParseIDErrors(t *testing.T) {
	if _, err := ParseID("zz"); err == nil {
		t.Fatal("expected error for non-hex input")
	}
	if _, err := ParseID("aabb"); err == nil {
		t.Fatal("expected error for wrong-length input")
	}
}

func TestIDStringFormatting(t *testing.T) {
	var id ID
	id[0] = 0xab
	id[1] = 0xcd

	if !strings.HasPrefix(id.String(), "abcd") {
		t.Fatalf("String() = %q, want prefix abcd", id.String())
	}
	if !strings.Contains(id.GoString(), "enode.HexID") {
		t.Fatalf("GoString() = %q, want to contain enode.HexID", id.GoString())
	}
	term := id.TerminalString()
	if len(term) != 16 { // 8 bytes hex-encoded
		t.Fatalf("TerminalString() len = %d, want 16", len(term))
	}
	text, err := id.MarshalText()
	if err != nil {
		t.Fatalf("MarshalText: %v", err)
	}
	var id2 ID
	if err := id2.UnmarshalText(text); err != nil {
		t.Fatalf("UnmarshalText: %v", err)
	}
	if id2 != id {
		t.Fatal("MarshalText/UnmarshalText round trip mismatch")
	}
}

func TestIDBytes(t *testing.T) {
	id := ID{1, 2, 3}
	b := id.Bytes()
	if len(b) != len(id) {
		t.Fatalf("Bytes() len = %d, want %d", len(b), len(id))
	}
}

func TestDistCmp(t *testing.T) {
	target := ID{}
	a := ID{0x01}
	b := ID{0x02}
	if DistCmp(target, a, b) != -1 {
		t.Fatal("expected a to be closer to target (DistCmp = -1)")
	}
	if DistCmp(target, b, a) != 1 {
		t.Fatal("expected b to be farther (DistCmp = 1)")
	}
	if DistCmp(target, a, a) != 0 {
		t.Fatal("expected equal distances to compare 0")
	}
}

func TestLogDist(t *testing.T) {
	a := ID{}
	b := ID{}
	if LogDist(a, b) != 0 {
		t.Fatalf("LogDist(equal) = %d, want 0", LogDist(a, b))
	}
	b[31] = 1
	if LogDist(a, b) != 1 {
		t.Fatalf("LogDist(diff last bit) = %d, want 1", LogDist(a, b))
	}
	b2 := ID{}
	b2[0] = 0x80 // top bit of first byte set => maximal distance
	if got := LogDist(a, b2); got != 256 {
		t.Fatalf("LogDist(top bit) = %d, want 256", got)
	}
}

func TestNullIDSignAndVerify(t *testing.T) {
	id := ID{9, 9, 9}
	var r enr.Record
	n := SignNull(&r, id)
	if n.ID() != id {
		t.Fatalf("SignNull node ID = %v, want %v", n.ID(), id)
	}

	parsed, err := New(NullID{}, n.Record())
	if err != nil {
		t.Fatalf("New(NullID{}): %v", err)
	}
	if parsed.ID() != id {
		t.Fatalf("parsed ID = %v, want %v", parsed.ID(), id)
	}
}

func TestParseEnrRecordWithNullScheme(t *testing.T) {
	var r enr.Record
	id := ID{1}
	n := SignNull(&r, id)
	text := n.String()

	parsed, err := Parse(ValidSchemesForTesting, text)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if parsed.ID() != id {
		t.Fatalf("parsed ID = %v, want %v", parsed.ID(), id)
	}
}

func TestParseMissingPrefix(t *testing.T) {
	if _, err := Parse(ValidSchemes, "not-a-valid-prefix"); err != errMissingPrefix {
		t.Fatalf("Parse() err = %v, want errMissingPrefix", err)
	}
}

func TestMustParsePanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for invalid input")
		}
	}()
	MustParse("garbage")
}
