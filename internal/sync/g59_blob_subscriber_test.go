package sync

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// TestDecodeGossipBlobSidecarRoundTrip covers decodeGossipBlobSidecar's
// success path against rawdb's own encoding.
func TestDecodeGossipBlobSidecarRoundTrip(t *testing.T) {
	sc := &block.BlobSidecar{
		Index:       3,
		BlockNumber: 42,
		BlockHash:   types.Hash{0xAB},
	}
	data, err := rawdb.EncodeBlobSidecars([]*block.BlobSidecar{sc})
	if err != nil {
		t.Fatalf("EncodeBlobSidecars: %v", err)
	}

	got, err := decodeGossipBlobSidecar(data)
	if err != nil {
		t.Fatalf("decodeGossipBlobSidecar: %v", err)
	}
	if got.Index != sc.Index || got.BlockNumber != sc.BlockNumber || got.BlockHash != sc.BlockHash {
		t.Fatalf("decodeGossipBlobSidecar() = %+v, want fields matching %+v", got, sc)
	}
}

// TestDecodeGossipBlobSidecarWrongCount covers the not-exactly-one-sidecar
// guard: gossip must carry a single sidecar per message.
func TestDecodeGossipBlobSidecarWrongCount(t *testing.T) {
	data, err := rawdb.EncodeBlobSidecars(nil)
	if err != nil {
		t.Fatalf("EncodeBlobSidecars(nil): %v", err)
	}
	if _, err := decodeGossipBlobSidecar(data); err == nil {
		t.Fatal("expected an error when the payload carries zero sidecars")
	}
}

// TestDecodeGossipBlobSidecarMalformed covers the rawdb decode-error branch.
func TestDecodeGossipBlobSidecarMalformed(t *testing.T) {
	if _, err := decodeGossipBlobSidecar([]byte{0xff, 0xff, 0xff}); err == nil {
		t.Fatal("expected an error for malformed sidecar bytes")
	}
}

// TestBlobSidecarSubscriberWrongMessageType covers the type-assertion guard
// in blobSidecarSubscriber, which does not need a chain or DB to reach.
func TestBlobSidecarSubscriberWrongMessageType(t *testing.T) {
	svc := &Service{cfg: &config{}}
	if err := svc.blobSidecarSubscriber(context.Background(), "not-raw-bytes"); err != errWrongMessage {
		t.Fatalf("blobSidecarSubscriber() err = %v, want %v", err, errWrongMessage)
	}
}

// TestBlobSidecarSubscriberDecodeError covers the decode-failure branch,
// also reachable without a chain or DB (the error is returned before any
// DB access).
func TestBlobSidecarSubscriberDecodeError(t *testing.T) {
	svc := &Service{cfg: &config{}}
	err := svc.blobSidecarSubscriber(context.Background(), &rawSSZBytes{data: []byte{0xff, 0xff, 0xff}})
	if err == nil {
		t.Fatal("expected an error for an undecodable blob sidecar payload")
	}
}
