package ed2k

import (
	"encoding/hex"
	"strings"
	"testing"
)

func TestParseLinkBasic(t *testing.T) {
	uri := "ed2k://|file|test.txt|12345|0123456789ABCDEF0123456789ABCDEF|/"
	link, err := ParseLink(uri)
	if err != nil {
		t.Fatal(err)
	}
	if link.Name != "test.txt" {
		t.Fatalf("name = %q, want test.txt", link.Name)
	}
	if link.Size != 12345 {
		t.Fatalf("size = %d, want 12345", link.Size)
	}
	expectedHash := "0123456789abcdef0123456789abcdef"
	if hex.EncodeToString(link.Hash[:]) != expectedHash {
		t.Fatalf("hash = %s, want %s", hex.EncodeToString(link.Hash[:]), expectedHash)
	}
}

func TestParseLinkWithExtensions(t *testing.T) {
	uri := "ed2k://|file|test.txt|100|0123456789ABCDEF0123456789ABCDEF|h=AABBCCDD|s=http://example.com|/"
	link, err := ParseLink(uri)
	if err != nil {
		t.Fatal(err)
	}
	if len(link.RootHash) == 0 {
		t.Fatal("expected root hash")
	}
	if link.Sources != "http://example.com" {
		t.Fatalf("sources = %q", link.Sources)
	}
}

func TestParseLinkErrors(t *testing.T) {
	tests := []struct {
		name string
		uri  string
	}{
		{"not ed2k", "http://example.com"},
		{"too few parts", "ed2k://|file|name|/"},
		{"invalid size", "ed2k://|file|name|abc|0123456789ABCDEF0123456789ABCDEF|/"},
		{"short hash", "ed2k://|file|name|100|0123|/"},
		{"empty name", "ed2k://|file||100|0123456789ABCDEF0123456789ABCDEF|/"},
		{"negative size", "ed2k://|file|name|-1|0123456789ABCDEF0123456789ABCDEF|/"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseLink(tt.uri)
			if err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestFormatLink(t *testing.T) {
	var hash [HashSize]byte
	for i := range hash {
		hash[i] = byte(i * 17)
	}
	link := FormatLink("test file.txt", 9999, hash)
	if !strings.HasPrefix(link, "ed2k://|file|") {
		t.Fatalf("link doesn't start with ed2k://|file|: %s", link)
	}
	if !strings.HasSuffix(link, "|/") {
		t.Fatalf("link doesn't end with |/: %s", link)
	}
	if !strings.Contains(link, "9999") {
		t.Fatal("link missing size")
	}
}

func TestLinkRoundTrip(t *testing.T) {
	var hash [HashSize]byte
	for i := range hash {
		hash[i] = byte(i)
	}
	original := &Link{
		Name: "my_file.dat",
		Size: 1048576,
		Hash: hash,
	}
	uri := original.String()
	parsed, err := ParseLink(uri)
	if err != nil {
		t.Fatalf("parse round-trip: %v", err)
	}
	if parsed.Name != original.Name {
		t.Fatalf("name: %q != %q", parsed.Name, original.Name)
	}
	if parsed.Size != original.Size {
		t.Fatalf("size: %d != %d", parsed.Size, original.Size)
	}
	if parsed.Hash != original.Hash {
		t.Fatal("hash mismatch")
	}
}

func TestLinkURLEncodedName(t *testing.T) {
	var hash [HashSize]byte
	link := &Link{
		Name: "file with spaces & symbols.txt",
		Size: 100,
		Hash: hash,
	}
	uri := link.String()
	parsed, err := ParseLink(uri)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Name != link.Name {
		t.Fatalf("name = %q, want %q", parsed.Name, link.Name)
	}
}
