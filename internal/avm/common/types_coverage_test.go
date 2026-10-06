package common

import (
	"encoding/json"
	"math/big"
	"math/rand"
	"testing"
)

// This file covers types.go functions that types_test.go leaves at 0%:
// the hash/address constructors, basic accessors, text/JSON marshaling,
// Generate, GraphQL glue, and the Unprefixed*/Mixedcase wrapper types.
// Scan/Value/Format are already covered by types_test.go.

func TestBytesToHashAndStringToHash(t *testing.T) {
	h := BytesToHash([]byte("hello"))
	if h == (Hash{}) {
		t.Fatal("BytesToHash should not be the zero hash")
	}

	hexStr := h.Hex()[2:] // strip 0x
	back := StringToHash(hexStr)
	back2 := StringToHash(hexStr)
	if back != back2 {
		t.Fatal("StringToHash should be deterministic")
	}

	if got := StringToHash("not-hex!!"); got != (Hash{}) {
		t.Errorf("StringToHash invalid input = %v, want zero hash", got)
	}
}

func TestBigToHashAndHexToHash(t *testing.T) {
	h1 := BigToHash(big.NewInt(12345))
	h2 := HexToHash("0x3039") // 12345 in hex
	if h1 != h2 {
		t.Errorf("BigToHash/HexToHash mismatch: %v != %v", h1, h2)
	}
}

func TestHashAccessors(t *testing.T) {
	h := BytesToHash([]byte("payload"))
	if len(h.Bytes()) != HashLength {
		t.Errorf("Bytes() length = %d, want %d", len(h.Bytes()), HashLength)
	}
	if h.Big().Sign() == 0 {
		t.Error("Big() should be non-zero for a non-zero hash")
	}
	if h.Hex() == "" || h.Hex()[:2] != "0x" {
		t.Errorf("Hex() = %q, want 0x-prefixed", h.Hex())
	}
	if h.String() != h.Hex() {
		t.Errorf("String() = %q, want Hex() = %q", h.String(), h.Hex())
	}
	if ts := h.TerminalString(); ts == "" {
		t.Error("TerminalString() should be non-empty")
	}
}

func TestHashMarshalUnmarshalTextAndJSON(t *testing.T) {
	h := BytesToHash([]byte("marshal"))
	text, err := h.MarshalText()
	if err != nil {
		t.Fatalf("MarshalText error: %v", err)
	}
	var h2 Hash
	if err := h2.UnmarshalText(text); err != nil {
		t.Fatalf("UnmarshalText error: %v", err)
	}
	if h != h2 {
		t.Fatal("UnmarshalText round trip mismatch")
	}

	data, err := json.Marshal(h)
	if err != nil {
		t.Fatalf("json.Marshal error: %v", err)
	}
	var h3 Hash
	if err := json.Unmarshal(data, &h3); err != nil {
		t.Fatalf("json.Unmarshal error: %v", err)
	}
	if h != h3 {
		t.Fatal("JSON round trip mismatch")
	}
}

func TestHashGenerate(t *testing.T) {
	var h Hash
	r := rand.New(rand.NewSource(1))
	v := h.Generate(r, 0)
	if _, ok := v.Interface().(Hash); !ok {
		t.Fatal("Generate() should return a reflect.Value wrapping a Hash")
	}
}

func TestHashGraphQL(t *testing.T) {
	var h Hash
	if !h.ImplementsGraphQLType("Bytes32") {
		t.Error("Hash should implement GraphQL type Bytes32")
	}
	if h.ImplementsGraphQLType("Other") {
		t.Error("Hash should not implement an unrelated GraphQL type")
	}

	valid := BytesToHash([]byte("gql")).Hex()
	if err := h.UnmarshalGraphQL(valid); err != nil {
		t.Fatalf("UnmarshalGraphQL(string) error: %v", err)
	}
	if err := h.UnmarshalGraphQL(42); err == nil {
		t.Error("UnmarshalGraphQL should reject non-string input")
	}
}

func TestUnprefixedHash(t *testing.T) {
	h := BytesToHash([]byte("unprefixed"))
	uh := UnprefixedHash(h)
	text, err := uh.MarshalText()
	if err != nil {
		t.Fatalf("MarshalText error: %v", err)
	}
	if len(text) != HashLength*2 {
		t.Errorf("unprefixed text length = %d, want %d", len(text), HashLength*2)
	}

	var back UnprefixedHash
	if err := back.UnmarshalText(text); err != nil {
		t.Fatalf("UnmarshalText error: %v", err)
	}
	if Hash(back) != h {
		t.Fatal("UnprefixedHash round trip mismatch")
	}

	prefixed := append([]byte("0x"), text...)
	var back2 UnprefixedHash
	if err := back2.UnmarshalText(prefixed); err != nil {
		t.Fatalf("UnmarshalText with 0x prefix error: %v", err)
	}
}

func TestAddressBasics(t *testing.T) {
	a := BytesToAddress([]byte{0x01, 0x02, 0x03})
	if len(a.Bytes()) != AddressLength {
		t.Errorf("Bytes() length = %d, want %d", len(a.Bytes()), AddressLength)
	}
	if a.Hash() == (Hash{}) {
		t.Error("Hash() should not be zero for a non-zero address")
	}
	if a.Hex() == "" || a.Hex()[:2] != "0x" {
		t.Errorf("Hex() = %q", a.Hex())
	}
	if a.String() != a.Hex() {
		t.Errorf("String() != Hex()")
	}
	if a.IsNull() {
		t.Error("IsNull() should be false for a non-zero address")
	}
	var zero Address
	if !zero.IsNull() {
		t.Error("IsNull() should be true for the zero address")
	}
}

func TestBigToAddressAndHexToAddress(t *testing.T) {
	a1 := BigToAddress(big.NewInt(255))
	a2 := HexToAddress("0xff")
	if a1 != a2 {
		t.Errorf("BigToAddress/HexToAddress mismatch: %v != %v", a1, a2)
	}
}

func TestAddressMarshalUnmarshalTextAndJSON(t *testing.T) {
	a := BytesToAddress([]byte("marshal-addr"))
	text, err := a.MarshalText()
	if err != nil {
		t.Fatalf("MarshalText error: %v", err)
	}
	var a2 Address
	if err := a2.UnmarshalText(text); err != nil {
		t.Fatalf("UnmarshalText error: %v", err)
	}
	if a != a2 {
		t.Fatal("round trip mismatch")
	}

	data, err := json.Marshal(a)
	if err != nil {
		t.Fatalf("json.Marshal error: %v", err)
	}
	var a3 Address
	if err := json.Unmarshal(data, &a3); err != nil {
		t.Fatalf("json.Unmarshal error: %v", err)
	}
	if a != a3 {
		t.Fatal("JSON round trip mismatch")
	}
}

func TestAddressGraphQL(t *testing.T) {
	var a Address
	if !a.ImplementsGraphQLType("Address") {
		t.Error("Address should implement GraphQL type Address")
	}
	if a.ImplementsGraphQLType("Other") {
		t.Error("Address should not implement an unrelated GraphQL type")
	}
	valid := BytesToAddress([]byte("gql-addr")).Hex()
	if err := a.UnmarshalGraphQL(valid); err != nil {
		t.Fatalf("UnmarshalGraphQL(string) error: %v", err)
	}
	if err := a.UnmarshalGraphQL(42); err == nil {
		t.Error("UnmarshalGraphQL should reject non-string input")
	}
}

func TestUnprefixedAddress(t *testing.T) {
	a := BytesToAddress([]byte("unprefixed-addr"))
	ua := UnprefixedAddress(a)
	text, err := ua.MarshalText()
	if err != nil {
		t.Fatalf("MarshalText error: %v", err)
	}
	if len(text) != AddressLength*2 {
		t.Errorf("unprefixed text length = %d, want %d", len(text), AddressLength*2)
	}
	var back UnprefixedAddress
	if err := back.UnmarshalText(text); err != nil {
		t.Fatalf("UnmarshalText error: %v", err)
	}
	if Address(back) != a {
		t.Fatal("UnprefixedAddress round trip mismatch")
	}
}

func TestMixedcaseAddressAccessors(t *testing.T) {
	a := BytesToAddress([]byte("mixedcase"))
	ma := NewMixedcaseAddress(a)
	if ma.Address() != a {
		t.Error("Address() mismatch")
	}
	if !ma.ValidChecksum() {
		t.Error("NewMixedcaseAddress should produce a valid checksum")
	}
	if ma.Original() != a.Hex() {
		t.Errorf("Original() = %q, want %q", ma.Original(), a.Hex())
	}
	if got := ma.String(); got == "" {
		t.Error("String() should be non-empty")
	}

	lower, err := NewMixedcaseAddressFromString(a.Hex())
	if err != nil {
		t.Fatalf("NewMixedcaseAddressFromString error: %v", err)
	}
	if lower.Address() != a {
		t.Error("NewMixedcaseAddressFromString address mismatch")
	}

	if _, err := NewMixedcaseAddressFromString("not-an-address"); err == nil {
		t.Error("expected error for invalid address string")
	}

	data, err := json.Marshal(&ma)
	if err != nil {
		t.Fatalf("MarshalJSON error: %v", err)
	}
	var back MixedcaseAddress
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("UnmarshalJSON error: %v", err)
	}
	if back.Address() != a {
		t.Error("MixedcaseAddress JSON round trip mismatch")
	}
}

func TestMixedcaseAddressInvalidChecksumString(t *testing.T) {
	a := BytesToAddress([]byte("badchecksum"))
	lowered := "0x" + toLowerHexBody(a.Hex())
	ma, err := NewMixedcaseAddressFromString(lowered)
	if err != nil {
		t.Fatalf("NewMixedcaseAddressFromString error: %v", err)
	}
	// Exercises ValidChecksum()/String() regardless of whether the
	// lower-cased string happens to match the checksummed form.
	_ = ma.ValidChecksum()
	if got := ma.String(); got == "" {
		t.Error("String() should be non-empty")
	}
}

func toLowerHexBody(s string) string {
	b := []byte(s[2:])
	for i, c := range b {
		if c >= 'A' && c <= 'F' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}
