package sync

import (
	"context"
	"testing"

	"github.com/libp2p/go-libp2p/core/test"

	"github.com/n42blockchain/N42/proto/sync_pb"
)

func newSnapTestService(t *testing.T) (*Service, *fakeP2P) {
	t.Helper()
	fp := newFakeP2P(t)
	svc := &Service{ctx: context.Background(), cfg: &config{p2p: fp}}
	svc.rateLimiter = newRateLimiter(fp)
	return svc, fp
}

func newSnapStreamPair(t *testing.T, fp *fakeP2P) (client, server *fakeStream) {
	t.Helper()
	remote, err := test.RandPeerID()
	if err != nil {
		t.Fatalf("RandPeerID: %v", err)
	}
	return newFakeStreamPair(remote, fp.self)
}

func TestAccountRangeRPCHandlerWrongMessageType(t *testing.T) {
	svc, fp := newSnapTestService(t)
	client, server := newSnapStreamPair(t, fp)
	defer client.Close()
	defer server.Close()

	wrong := "nope"
	if err := svc.accountRangeRPCHandler(context.Background(), &wrong, server); err == nil {
		t.Fatal("expected error for wrong message type")
	}
}

func TestAccountRangeRPCHandlerRejectsBadStartLength(t *testing.T) {
	svc, fp := newSnapTestService(t)
	client, server := newSnapStreamPair(t, fp)
	defer client.Close()
	go func() { _, _, _ = ReadStatusCode(client, fp.Encoding()) }()

	req := &sync_pb.GetAccountRangeRequest{Start: make([]byte, 5)}
	if err := svc.accountRangeRPCHandler(context.Background(), req, server); err == nil {
		t.Fatal("expected error for a bad start key length")
	}
}

func TestAccountRangeRPCHandlerRejectsBadEndLength(t *testing.T) {
	svc, fp := newSnapTestService(t)
	client, server := newSnapStreamPair(t, fp)
	defer client.Close()
	go func() { _, _, _ = ReadStatusCode(client, fp.Encoding()) }()

	req := &sync_pb.GetAccountRangeRequest{End: make([]byte, 5)}
	if err := svc.accountRangeRPCHandler(context.Background(), req, server); err == nil {
		t.Fatal("expected error for a bad end key length")
	}
}

func TestAccountRangeRPCHandlerRejectsStartNotLessThanEnd(t *testing.T) {
	svc, fp := newSnapTestService(t)
	client, server := newSnapStreamPair(t, fp)
	defer client.Close()
	go func() { _, _, _ = ReadStatusCode(client, fp.Encoding()) }()

	same := make([]byte, 20)
	req := &sync_pb.GetAccountRangeRequest{Start: same, End: same}
	if err := svc.accountRangeRPCHandler(context.Background(), req, server); err == nil {
		t.Fatal("expected error when start >= end")
	}
}

func TestStorageRangeRPCHandlerWrongMessageType(t *testing.T) {
	svc, fp := newSnapTestService(t)
	client, server := newSnapStreamPair(t, fp)
	defer client.Close()
	defer server.Close()

	wrong := 5
	if err := svc.storageRangeRPCHandler(context.Background(), &wrong, server); err == nil {
		t.Fatal("expected error for wrong message type")
	}
}

func TestStorageRangeRPCHandlerRejectsBadAccountLength(t *testing.T) {
	svc, fp := newSnapTestService(t)
	client, server := newSnapStreamPair(t, fp)
	defer client.Close()
	go func() { _, _, _ = ReadStatusCode(client, fp.Encoding()) }()

	req := &sync_pb.GetStorageRangeRequest{Account: make([]byte, 5)}
	if err := svc.storageRangeRPCHandler(context.Background(), req, server); err == nil {
		t.Fatal("expected error for a bad account length")
	}
}

func TestCodeRPCHandlerWrongMessageType(t *testing.T) {
	svc, fp := newSnapTestService(t)
	client, server := newSnapStreamPair(t, fp)
	defer client.Close()
	defer server.Close()

	wrong := true
	if err := svc.codeRPCHandler(context.Background(), &wrong, server); err == nil {
		t.Fatal("expected error for wrong message type")
	}
}

func TestCodeRPCHandlerRejectsNoHashes(t *testing.T) {
	svc, fp := newSnapTestService(t)
	client, server := newSnapStreamPair(t, fp)
	defer client.Close()
	go func() { _, _, _ = ReadStatusCode(client, fp.Encoding()) }()

	req := &sync_pb.GetCodeRequest{}
	if err := svc.codeRPCHandler(context.Background(), req, server); err == nil {
		t.Fatal("expected error for no code hashes")
	}
}

func TestCodeRPCHandlerRejectsTooManyHashes(t *testing.T) {
	svc, fp := newSnapTestService(t)
	client, server := newSnapStreamPair(t, fp)
	defer client.Close()
	go func() { _, _, _ = ReadStatusCode(client, fp.Encoding()) }()

	hashes := make([][]byte, maxSnapCodeHashes+1)
	for i := range hashes {
		hashes[i] = make([]byte, 32)
	}
	req := &sync_pb.GetCodeRequest{Hashes: hashes}
	if err := svc.codeRPCHandler(context.Background(), req, server); err == nil {
		t.Fatal("expected error for too many code hashes")
	}
}

func TestCodeRPCHandlerRejectsBadHashLength(t *testing.T) {
	svc, fp := newSnapTestService(t)
	client, server := newSnapStreamPair(t, fp)
	defer client.Close()
	go func() { _, _, _ = ReadStatusCode(client, fp.Encoding()) }()

	req := &sync_pb.GetCodeRequest{Hashes: [][]byte{make([]byte, 10)}}
	if err := svc.codeRPCHandler(context.Background(), req, server); err == nil {
		t.Fatal("expected error for a bad hash length")
	}
}
