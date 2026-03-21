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
