package sync

import (
	"bytes"
	"context"
	"testing"

	"github.com/libp2p/go-libp2p/core/test"

	block "github.com/n42blockchain/N42/common/block"
	comtypes "github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/proto/sync_pb"
)

func newBlobTestService(t *testing.T) (*Service, *fakeP2P) {
	t.Helper()
	fp := newFakeP2P(t)
	svc := &Service{ctx: context.Background(), cfg: &config{p2p: fp}}
	svc.rateLimiter = newRateLimiter(fp)
	return svc, fp
}

func newBlobStreamPair(t *testing.T, fp *fakeP2P) (client, server *fakeStream) {
	t.Helper()
	remote, err := test.RandPeerID()
	if err != nil {
		t.Fatalf("RandPeerID: %v", err)
	}
	return newFakeStreamPair(remote, fp.self)
}

func TestBlobSidecarsByRangeRPCHandlerWrongMessageType(t *testing.T) {
	svc, fp := newBlobTestService(t)
	client, server := newBlobStreamPair(t, fp)
	defer client.Close()
	defer server.Close()

	wrong := "nope"
	if err := svc.blobSidecarsByRangeRPCHandler(context.Background(), &wrong, server); err == nil {
		t.Fatal("expected error for wrong message type")
	}
}

func TestBlobSidecarsByRangeRPCHandlerRejectsZeroCount(t *testing.T) {
	svc, fp := newBlobTestService(t)
	client, server := newBlobStreamPair(t, fp)
	defer client.Close()
	go func() { _, _, _ = ReadStatusCode(client, fp.Encoding()) }()

	req := &sync_pb.BlobSidecarsByRangeRequest{Count: 0}
	if err := svc.blobSidecarsByRangeRPCHandler(context.Background(), req, server); err == nil {
		t.Fatal("expected error for zero count")
	}
}

func TestBlobSidecarsByRangeRPCHandlerRejectsOversizedCount(t *testing.T) {
	svc, fp := newBlobTestService(t)
	client, server := newBlobStreamPair(t, fp)
	defer client.Close()
	go func() { _, _, _ = ReadStatusCode(client, fp.Encoding()) }()

	req := &sync_pb.BlobSidecarsByRangeRequest{Count: maxBlobRangeCount + 1}
	if err := svc.blobSidecarsByRangeRPCHandler(context.Background(), req, server); err == nil {
		t.Fatal("expected error for count above max")
	}
}

func TestBlobSidecarsByRootRPCHandlerWrongMessageType(t *testing.T) {
	svc, fp := newBlobTestService(t)
	client, server := newBlobStreamPair(t, fp)
	defer client.Close()
	defer server.Close()

	wrong := 7
	if err := svc.blobSidecarsByRootRPCHandler(context.Background(), &wrong, server); err == nil {
		t.Fatal("expected error for wrong message type")
	}
}

func TestBlobSidecarsByRootRPCHandlerRejectsNoIdentifiers(t *testing.T) {
	svc, fp := newBlobTestService(t)
	client, server := newBlobStreamPair(t, fp)
	defer client.Close()
	go func() { _, _, _ = ReadStatusCode(client, fp.Encoding()) }()

	req := &sync_pb.BlobSidecarsByRootRequest{}
	if err := svc.blobSidecarsByRootRPCHandler(context.Background(), req, server); err == nil {
		t.Fatal("expected error for no identifiers")
	}
}

func TestBlobSidecarsByRootRPCHandlerRejectsTooManyIdentifiers(t *testing.T) {
	svc, fp := newBlobTestService(t)
	client, server := newBlobStreamPair(t, fp)
	defer client.Close()
	go func() { _, _, _ = ReadStatusCode(client, fp.Encoding()) }()

	ids := make([]*sync_pb.BlobIdentifierMsg, maxBlobRootIdentifiers+1)
	for i := range ids {
		ids[i] = &sync_pb.BlobIdentifierMsg{BlockRoot: make([]byte, 32)}
	}
	req := &sync_pb.BlobSidecarsByRootRequest{Identifiers: ids}
	if err := svc.blobSidecarsByRootRPCHandler(context.Background(), req, server); err == nil {
		t.Fatal("expected error for too many identifiers")
	}
}

func TestBlobSidecarsByRootRPCHandlerRejectsShortBlockRoot(t *testing.T) {
	svc, fp := newBlobTestService(t)
	client, server := newBlobStreamPair(t, fp)
	defer client.Close()
	go func() { _, _, _ = ReadStatusCode(client, fp.Encoding()) }()

	req := &sync_pb.BlobSidecarsByRootRequest{Identifiers: []*sync_pb.BlobIdentifierMsg{
		{BlockRoot: make([]byte, 10)},
	}}
	if err := svc.blobSidecarsByRootRPCHandler(context.Background(), req, server); err == nil {
		t.Fatal("expected error for a short block root")
	}
}

func TestBlobSidecarsByRootRPCHandlerRejectsOversizedIndex(t *testing.T) {
	svc, fp := newBlobTestService(t)
	client, server := newBlobStreamPair(t, fp)
	defer client.Close()
	go func() { _, _, _ = ReadStatusCode(client, fp.Encoding()) }()

	req := &sync_pb.BlobSidecarsByRootRequest{Identifiers: []*sync_pb.BlobIdentifierMsg{
		{BlockRoot: make([]byte, 32), Index: block.MaxBlobsPerBlock},
	}}
	if err := svc.blobSidecarsByRootRPCHandler(context.Background(), req, server); err == nil {
		t.Fatal("expected error for an out-of-range blob index")
	}
}

func TestBlobSidecarToMsgRoundTrip(t *testing.T) {
	sc := &block.BlobSidecar{
		Index:       3,
		BlockNumber: 42,
		BlockHash:   comtypes.Hash{0xAA},
		TxHash:      comtypes.Hash{0xBB},
	}
	sc.Blob[0] = 0x11
	sc.KZGCommitment[0] = 0x22
	sc.KZGProof[0] = 0x33

	msg := blobSidecarToMsg(sc)
	got, err := msgToBlobSidecar(msg)
	if err != nil {
		t.Fatalf("msgToBlobSidecar: %v", err)
	}
	if got.Index != sc.Index || got.BlockNumber != sc.BlockNumber {
		t.Fatalf("round trip mismatch: got %+v, want %+v", got, sc)
	}
	if got.BlockHash != sc.BlockHash || got.TxHash != sc.TxHash {
		t.Fatalf("hash round trip mismatch")
	}
	if !bytes.Equal(got.Blob[:], sc.Blob[:]) {
		t.Fatal("blob bytes did not round trip")
	}
}

func TestMsgToBlobSidecarRejectsBadBlobSize(t *testing.T) {
	msg := &sync_pb.BlobSidecarMsg{
		Blob:          []byte{0x01},
		KZGCommitment: make([]byte, block.KZGCommitmentSize),
		KZGProof:      make([]byte, block.KZGProofSize),
		BlockHash:     make([]byte, 32),
	}
	if _, err := msgToBlobSidecar(msg); err == nil {
		t.Fatal("expected error for bad blob size")
	}
}

func TestMsgToBlobSidecarRejectsBadCommitmentSize(t *testing.T) {
	msg := &sync_pb.BlobSidecarMsg{
		Blob:          make([]byte, block.BlobSize),
		KZGCommitment: []byte{0x01},
		KZGProof:      make([]byte, block.KZGProofSize),
		BlockHash:     make([]byte, 32),
	}
	if _, err := msgToBlobSidecar(msg); err == nil {
		t.Fatal("expected error for bad KZG commitment size")
	}
}

func TestMsgToBlobSidecarRejectsBadProofSize(t *testing.T) {
	msg := &sync_pb.BlobSidecarMsg{
		Blob:          make([]byte, block.BlobSize),
		KZGCommitment: make([]byte, block.KZGCommitmentSize),
		KZGProof:      []byte{0x01},
		BlockHash:     make([]byte, 32),
	}
	if _, err := msgToBlobSidecar(msg); err == nil {
		t.Fatal("expected error for bad KZG proof size")
	}
}

func TestMsgToBlobSidecarRejectsBadBlockHashSize(t *testing.T) {
	msg := &sync_pb.BlobSidecarMsg{
		Blob:          make([]byte, block.BlobSize),
		KZGCommitment: make([]byte, block.KZGCommitmentSize),
		KZGProof:      make([]byte, block.KZGProofSize),
		BlockHash:     []byte{0x01},
	}
	if _, err := msgToBlobSidecar(msg); err == nil {
		t.Fatal("expected error for bad block hash size")
	}
}
