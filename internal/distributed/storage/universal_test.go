package storage

import (
	"context"
	"fmt"
	"testing"

	"github.com/n42blockchain/N42/internal/distributed/storage/ed2k"
)

func TestUniversalResolverCAS(t *testing.T) {
	// Mock CAS that returns known data
	casStore := map[[32]byte][]byte{}
	var contentHash [32]byte
	contentHash[0] = 0xAA
	casStore[contentHash] = []byte("hello from CAS")

	resolver := NewUniversalResolver(nil, nil, nil, nil, func(h [32]byte) ([]byte, error) {
		data, ok := casStore[h]
		if !ok {
			return nil, fmt.Errorf("not found")
		}
		return data, nil
	})

	result, err := resolver.Resolve(context.Background(), contentHash)
	if err != nil {
		t.Fatal(err)
	}
	if result.Protocol != ProtocolCAS {
		t.Fatalf("protocol = %s, want cas", result.Protocol)
	}
	if string(result.Data) != "hello from CAS" {
		t.Fatalf("data = %q", string(result.Data))
	}
}

func TestUniversalResolverNotFound(t *testing.T) {
	resolver := NewUniversalResolver(nil, nil, nil, nil, func(h [32]byte) ([]byte, error) {
		return nil, fmt.Errorf("not found")
	})

	var h [32]byte
	_, err := resolver.Resolve(context.Background(), h)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestUniversalResolverRegister(t *testing.T) {
	eb := ed2k.NewBridge()
	cb := NewContentBridge(nil, false)

	resolver := NewUniversalResolver(nil, nil, eb, cb, nil)

	var contentHash [32]byte
	contentHash[0] = 0xBB
	data := []byte("register test content")

	info, err := resolver.Register(contentHash, "test.txt", data, 0)
	if err != nil {
		t.Fatal(err)
	}
	if info.CID == "" {
		t.Fatal("expected CID")
	}
	if info.Ed2kHash == "" {
		t.Fatal("expected ed2k hash")
	}
	if info.Ed2kLink == "" {
		t.Fatal("expected ed2k link")
	}
}

func TestUniversalResolverGetInfo(t *testing.T) {
	eb := ed2k.NewBridge()
	cb := NewContentBridge(nil, false)

	resolver := NewUniversalResolver(nil, nil, eb, cb, nil)

	var contentHash [32]byte
	contentHash[0] = 0xCC
	data := []byte("info test")

	// Register first
	resolver.Register(contentHash, "info.dat", data, 0)

	// Get info
	info := resolver.GetInfo(contentHash)
	if info.CID == "" {
		t.Fatal("expected CID")
	}
	if info.Ed2kHash == "" {
		t.Fatal("expected ed2k hash")
	}
}

func TestUniversalResolverResolveByURIHash(t *testing.T) {
	casStore := map[[32]byte][]byte{}
	var contentHash [32]byte
	contentHash[0] = 0xDD
	casStore[contentHash] = []byte("found by hash")

	resolver := NewUniversalResolver(nil, nil, nil, nil, func(h [32]byte) ([]byte, error) {
		data, ok := casStore[h]
		if !ok {
			return nil, fmt.Errorf("not found")
		}
		return data, nil
	})

	uri := "0xdd00000000000000000000000000000000000000000000000000000000000000"
	result, err := resolver.ResolveByURI(context.Background(), uri)
	if err != nil {
		t.Fatal(err)
	}
	if string(result.Data) != "found by hash" {
		t.Fatalf("data = %q", string(result.Data))
	}
}

func TestUniversalResolverResolveByURIEd2k(t *testing.T) {
	eb := ed2k.NewBridge()

	casStore := map[[32]byte][]byte{}
	var contentHash [32]byte
	contentHash[0] = 0xEE
	data := []byte("ed2k resolve test")
	casStore[contentHash] = data

	// Register with ed2k bridge
	eb.ComputeAndMap(contentHash, "test.dat", data)

	resolver := NewUniversalResolver(nil, nil, eb, nil, func(h [32]byte) ([]byte, error) {
		d, ok := casStore[h]
		if !ok {
			return nil, fmt.Errorf("not found")
		}
		return d, nil
	})

	// Get ed2k link
	link, _ := eb.FormatLink(contentHash)
	result, err := resolver.ResolveByURI(context.Background(), link)
	if err != nil {
		t.Fatal(err)
	}
	if result.Protocol != ProtocolCAS {
		t.Fatalf("protocol = %s, want cas", result.Protocol)
	}
	if string(result.Data) != "ed2k resolve test" {
		t.Fatalf("data = %q", string(result.Data))
	}
}

func TestUniversalResolverResolveByURIUnknown(t *testing.T) {
	resolver := NewUniversalResolver(nil, nil, nil, nil, nil)
	_, err := resolver.ResolveByURI(context.Background(), "unknown://test")
	if err == nil {
		t.Fatal("expected error for unknown URI")
	}
}

func TestProtocolConstants(t *testing.T) {
	if ProtocolCAS != "cas" {
		t.Fatal("wrong CAS protocol")
	}
	if ProtocolIPFS != "ipfs" {
		t.Fatal("wrong IPFS protocol")
	}
	if ProtocolBitTorrent != "bittorrent" {
		t.Fatal("wrong BitTorrent protocol")
	}
	if ProtocolEd2k != "ed2k" {
		t.Fatal("wrong ed2k protocol")
	}
}

func TestUniversalResolverStats(t *testing.T) {
	eb := ed2k.NewBridge()
	cb := NewContentBridge(nil, false)

	casGet := func(h [32]byte) ([]byte, error) { return nil, fmt.Errorf("not found") }

	// Resolver with some backends set, some nil.
	resolver := NewUniversalResolver(nil, nil, eb, cb, casGet)
	stats := resolver.Stats()

	if stats["hasIPFS"] != false {
		t.Fatal("expected hasIPFS=false")
	}
	if stats["hasTorrent"] != false {
		t.Fatal("expected hasTorrent=false")
	}
	if stats["hasEd2k"] != true {
		t.Fatal("expected hasEd2k=true")
	}
	if stats["hasCAS"] != true {
		t.Fatal("expected hasCAS=true")
	}
}

func TestUniversalResolverStatsAllNil(t *testing.T) {
	resolver := NewUniversalResolver(nil, nil, nil, nil, nil)
	stats := resolver.Stats()

	for _, key := range []string{"hasIPFS", "hasTorrent", "hasEd2k", "hasCAS"} {
		if stats[key] != false {
			t.Fatalf("expected %s=false with nil resolver", key)
		}
	}
}

func TestUniversalResolverHasProtocol(t *testing.T) {
	eb := ed2k.NewBridge()
	casGet := func(h [32]byte) ([]byte, error) { return nil, nil }

	resolver := NewUniversalResolver(nil, nil, eb, nil, casGet)

	if resolver.HasProtocol(ProtocolCAS) != true {
		t.Fatal("expected CAS protocol")
	}
	if resolver.HasProtocol(ProtocolIPFS) != false {
		t.Fatal("expected no IPFS protocol")
	}
	if resolver.HasProtocol(ProtocolBitTorrent) != false {
		t.Fatal("expected no BitTorrent protocol")
	}
	if resolver.HasProtocol(ProtocolEd2k) != true {
		t.Fatal("expected ed2k protocol")
	}
	if resolver.HasProtocol(Protocol("unknown")) != false {
		t.Fatal("expected false for unknown protocol")
	}
}

func TestUniversalResolverResolveNotFoundAllProtocols(t *testing.T) {
	// Resolver with no backends at all.
	resolver := NewUniversalResolver(nil, nil, nil, nil, nil)

	var h [32]byte
	h[0] = 0xFF
	_, err := resolver.Resolve(context.Background(), h)
	if err == nil {
		t.Fatal("expected error when no backends are configured")
	}
}

func TestUniversalResolverResolveByURIInvalid(t *testing.T) {
	resolver := NewUniversalResolver(nil, nil, nil, nil, nil)

	invalidURIs := []string{
		"",
		"://",
		"ftp://example.com/file",
		"not-a-uri",
		"0xinvalid", // looks like hex but wrong length
		"0x" + "zz00000000000000000000000000000000000000000000000000000000000000", // right length but bad hex
	}
	for _, uri := range invalidURIs {
		_, err := resolver.ResolveByURI(context.Background(), uri)
		if err == nil {
			t.Fatalf("expected error for invalid URI %q", uri)
		}
	}
}

func TestUniversalResolverRegisterNilBackends(t *testing.T) {
	// All backends nil -- should not panic.
	resolver := NewUniversalResolver(nil, nil, nil, nil, nil)

	var contentHash [32]byte
	contentHash[0] = 0x11
	data := []byte("nil backends test")

	info, err := resolver.Register(contentHash, "test.txt", data, 0)
	if err != nil {
		t.Fatal(err)
	}
	// With nil content bridge, CID should be empty.
	if info.CID != "" {
		t.Fatalf("expected empty CID, got %q", info.CID)
	}
	// With nil ed2k bridge, hash should be empty.
	if info.Ed2kHash != "" {
		t.Fatalf("expected empty Ed2kHash, got %q", info.Ed2kHash)
	}
	// Size should still be set.
	if info.Size != int64(len(data)) {
		t.Fatalf("size = %d, want %d", info.Size, len(data))
	}
}
