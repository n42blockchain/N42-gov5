package ed2k

import (
	"encoding/hex"
	"strings"
	"testing"
)

func TestBridgeComputeAndMap(t *testing.T) {
	bridge := NewBridge()
	var contentHash [32]byte
	for i := range contentHash {
		contentHash[i] = byte(i)
	}
	data := []byte("test content for ed2k bridge")

	mapping, err := bridge.ComputeAndMap(contentHash, "test.txt", data)
	if err != nil {
		t.Fatal(err)
	}
	if mapping.Size != int64(len(data)) {
		t.Fatalf("size = %d, want %d", mapping.Size, len(data))
	}
	if mapping.Name != "test.txt" {
		t.Fatalf("name = %q", mapping.Name)
	}

	// Verify ed2k hash matches direct computation
	expected := Hash(data)
	if mapping.Ed2kHash != expected {
		t.Fatal("ed2k hash mismatch")
	}
}

func TestBridgeComputeAndMapEmpty(t *testing.T) {
	bridge := NewBridge()
	var contentHash [32]byte
	_, err := bridge.ComputeAndMap(contentHash, "test.txt", nil)
	if err == nil {
		t.Fatal("expected error for empty data")
	}
}

func TestBridgeGetMapping(t *testing.T) {
	bridge := NewBridge()
	var contentHash [32]byte
	contentHash[0] = 0xAA

	data := []byte("some data")
	bridge.ComputeAndMap(contentHash, "file.dat", data)

	m, ok := bridge.GetMapping(contentHash)
	if !ok {
		t.Fatal("mapping not found")
	}
	if m.ContentHash != contentHash {
		t.Fatal("content hash mismatch")
	}
}

func TestBridgeReverseLookup(t *testing.T) {
	bridge := NewBridge()
	var contentHash [32]byte
	contentHash[0] = 0xBB

	data := []byte("reverse lookup test")
	mapping, _ := bridge.ComputeAndMap(contentHash, "rev.txt", data)

	found, ok := bridge.ContentHashFromEd2k(mapping.Ed2kHash)
	if !ok {
		t.Fatal("reverse lookup failed")
	}
	if found != contentHash {
		t.Fatal("content hash mismatch")
	}
}

func TestBridgeFormatLink(t *testing.T) {
	bridge := NewBridge()
	var contentHash [32]byte
	contentHash[0] = 0xCC

	data := []byte("link test data")
	bridge.ComputeAndMap(contentHash, "linked.txt", data)

	link, err := bridge.FormatLink(contentHash)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(link, "ed2k://|file|") {
		t.Fatalf("unexpected link format: %s", link)
	}
}

func TestBridgeMappingCount(t *testing.T) {
	bridge := NewBridge()
	if bridge.MappingCount() != 0 {
		t.Fatal("expected 0 mappings")
	}

	for i := 0; i < 5; i++ {
		var h [32]byte
		h[0] = byte(i)
		bridge.ComputeAndMap(h, "file.dat", []byte("data"))
	}
	if bridge.MappingCount() != 5 {
		t.Fatalf("count = %d, want 5", bridge.MappingCount())
	}
}

func TestBridgeAddMapping(t *testing.T) {
	bridge := NewBridge()
	m := &HashMapping{
		ContentHash: [32]byte{1, 2, 3},
		Ed2kHash:    [HashSize]byte{4, 5, 6},
		Size:        100,
		Name:        "external.dat",
	}
	bridge.AddMapping(m)

	got, ok := bridge.GetMapping(m.ContentHash)
	if !ok {
		t.Fatal("added mapping not found")
	}
	if got.Name != "external.dat" {
		t.Fatalf("name = %q", got.Name)
	}
}

func TestBridgeEd2kHashFromContentHash(t *testing.T) {
	bridge := NewBridge()
	var contentHash [32]byte
	contentHash[0] = 0xDD

	_, ok := bridge.Ed2kHashFromContentHash(contentHash)
	if ok {
		t.Fatal("should not find unmapped hash")
	}

	data := []byte("test data")
	bridge.ComputeAndMap(contentHash, "test.dat", data)

	ed2kHash, ok := bridge.Ed2kHashFromContentHash(contentHash)
	if !ok {
		t.Fatal("should find mapped hash")
	}

	expected := Hash(data)
	if ed2kHash != expected {
		t.Fatalf("ed2k hash: %s != %s",
			hex.EncodeToString(ed2kHash[:]),
			hex.EncodeToString(expected[:]))
	}
}
