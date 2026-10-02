package sync

import (
	"context"
	"testing"

	"github.com/libp2p/go-libp2p/core/test"

	"github.com/n42blockchain/N42/proto/sync_pb"
)

func newSnapshotTestService(t *testing.T) (*Service, *fakeP2P) {
	t.Helper()
	fp := newFakeP2P(t)
	svc := &Service{ctx: context.Background(), cfg: &config{p2p: fp}}
	svc.rateLimiter = newRateLimiter(fp)
	return svc, fp
}

func newSnapshotStreamPair(t *testing.T, fp *fakeP2P) (client, server *fakeStream) {
	t.Helper()
	remote, err := test.RandPeerID()
	if err != nil {
		t.Fatalf("RandPeerID: %v", err)
	}
	return newFakeStreamPair(remote, fp.self)
}

func TestSnapshotInfoRPCHandlerWrongMessageType(t *testing.T) {
	svc, fp := newSnapshotTestService(t)
	client, server := newSnapshotStreamPair(t, fp)
	defer client.Close()
	defer server.Close()

	wrong := "nope"
	if err := svc.snapshotInfoRPCHandler(context.Background(), &wrong, server); err == nil {
		t.Fatal("expected error for wrong message type")
	}
}

func TestSnapshotAccountRangeRPCHandlerWrongMessageType(t *testing.T) {
	svc, fp := newSnapshotTestService(t)
	client, server := newSnapshotStreamPair(t, fp)
	defer client.Close()
	defer server.Close()

	wrong := 1
	if err := svc.snapshotAccountRangeRPCHandler(context.Background(), &wrong, server); err == nil {
		t.Fatal("expected error for wrong message type")
	}
}

func TestSnapshotAccountRangeRPCHandlerRejectsZeroSnapshotBlock(t *testing.T) {
	svc, fp := newSnapshotTestService(t)
	client, server := newSnapshotStreamPair(t, fp)
	defer client.Close()
	go func() { _, _, _ = ReadStatusCode(client, fp.Encoding()) }()

	req := &sync_pb.GetSnapshotAccountRangeRequest{SnapshotBlock: 0}
	if err := svc.snapshotAccountRangeRPCHandler(context.Background(), req, server); err == nil {
		t.Fatal("expected error for a zero snapshot block")
	}
}

func TestSnapshotAccountRangeRPCHandlerRejectsBadStartLength(t *testing.T) {
	svc, fp := newSnapshotTestService(t)
	client, server := newSnapshotStreamPair(t, fp)
	defer client.Close()
	go func() { _, _, _ = ReadStatusCode(client, fp.Encoding()) }()

	req := &sync_pb.GetSnapshotAccountRangeRequest{SnapshotBlock: 1, Start: make([]byte, 5)}
	if err := svc.snapshotAccountRangeRPCHandler(context.Background(), req, server); err == nil {
		t.Fatal("expected error for a bad start key length")
	}
}

func TestSnapshotStorageRangeRPCHandlerWrongMessageType(t *testing.T) {
	svc, fp := newSnapshotTestService(t)
	client, server := newSnapshotStreamPair(t, fp)
	defer client.Close()
	defer server.Close()

	wrong := true
	if err := svc.snapshotStorageRangeRPCHandler(context.Background(), &wrong, server); err == nil {
		t.Fatal("expected error for wrong message type")
	}
}

func TestSnapshotStorageRangeRPCHandlerRejectsZeroSnapshotBlock(t *testing.T) {
	svc, fp := newSnapshotTestService(t)
	client, server := newSnapshotStreamPair(t, fp)
	defer client.Close()
	go func() { _, _, _ = ReadStatusCode(client, fp.Encoding()) }()

	req := &sync_pb.GetSnapshotStorageRangeRequest{SnapshotBlock: 0, Account: make([]byte, 20)}
	if err := svc.snapshotStorageRangeRPCHandler(context.Background(), req, server); err == nil {
		t.Fatal("expected error for a zero snapshot block")
	}
}

func TestSnapshotStorageRangeRPCHandlerRejectsBadAccountLength(t *testing.T) {
	svc, fp := newSnapshotTestService(t)
	client, server := newSnapshotStreamPair(t, fp)
	defer client.Close()
	go func() { _, _, _ = ReadStatusCode(client, fp.Encoding()) }()

	req := &sync_pb.GetSnapshotStorageRangeRequest{SnapshotBlock: 1, Account: make([]byte, 5)}
	if err := svc.snapshotStorageRangeRPCHandler(context.Background(), req, server); err == nil {
		t.Fatal("expected error for a bad account length")
	}
}

func TestChangeSetRangeRPCHandlerWrongMessageType(t *testing.T) {
	svc, fp := newSnapshotTestService(t)
	client, server := newSnapshotStreamPair(t, fp)
	defer client.Close()
	defer server.Close()

	wrong := 3.14
	if err := svc.changeSetRangeRPCHandler(context.Background(), &wrong, server); err == nil {
		t.Fatal("expected error for wrong message type")
	}
}

func TestChangeSetRangeRPCHandlerRejectsInvertedRange(t *testing.T) {
	svc, fp := newSnapshotTestService(t)
	client, server := newSnapshotStreamPair(t, fp)
	defer client.Close()
	go func() { _, _, _ = ReadStatusCode(client, fp.Encoding()) }()

	req := &sync_pb.GetChangeSetRangeRequest{FromBlock: 10, ToBlock: 5}
	if err := svc.changeSetRangeRPCHandler(context.Background(), req, server); err == nil {
		t.Fatal("expected error when fromBlock >= toBlock")
	}
}

func TestChangeSetRangeRPCHandlerRejectsEqualRange(t *testing.T) {
	svc, fp := newSnapshotTestService(t)
	client, server := newSnapshotStreamPair(t, fp)
	defer client.Close()
	go func() { _, _, _ = ReadStatusCode(client, fp.Encoding()) }()

	req := &sync_pb.GetChangeSetRangeRequest{FromBlock: 5, ToBlock: 5}
	if err := svc.changeSetRangeRPCHandler(context.Background(), req, server); err == nil {
		t.Fatal("expected error when fromBlock == toBlock")
	}
}
