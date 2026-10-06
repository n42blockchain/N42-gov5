package sync

import (
	"context"
	"errors"
	"testing"

	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/core/test"

	ssztype "github.com/n42blockchain/N42/common/types/ssz"
	"github.com/n42blockchain/N42/internal/p2p"
	"github.com/n42blockchain/N42/internal/p2p/p2ptypes"
	"github.com/n42blockchain/N42/proto/sync_pb"
)

// newPingTestService builds a minimal *Service suitable for exercising
// pingHandler / goodbyeRPCHandler over a fake stream pair.
func newPingTestService(t *testing.T) (*Service, *fakeP2P) {
	t.Helper()
	fp := newFakeP2P(t)
	svc := &Service{ctx: context.Background(), cfg: &config{p2p: fp}}
	svc.rateLimiter = newRateLimiter(fp)
	return svc, fp
}

func TestPingHandlerWrongMessageType(t *testing.T) {
	svc, fp := newPingTestService(t)
	remote, err := test.RandPeerID()
	if err != nil {
		t.Fatalf("RandPeerID: %v", err)
	}
	client, server := newFakeStreamPair(remote, fp.self)
	defer client.Close()

	wrong := "not-a-uint64"
	if err := svc.pingHandler(context.Background(), &wrong, server); err == nil {
		t.Fatal("expected error for wrong message type")
	}
}

func TestPingHandlerValidSequence(t *testing.T) {
	svc, fp := newPingTestService(t)
	remote, err := test.RandPeerID()
	if err != nil {
		t.Fatalf("RandPeerID: %v", err)
	}
	fp.addConnectedPeer(t, remote, 1)

	client, server := newFakeStreamPair(remote, fp.self)
	defer client.Close()
	_ = server.SetProtocol(protocol.ID(p2p.RPCPingTopicV1 + fp.Encoding().ProtocolSuffix()))

	seq := ssztype.SSZUint64(0)
	done := make(chan error, 1)
	go func() { done <- svc.pingHandler(context.Background(), &seq, server) }()

	// Drain the response byte + encoded ping the handler writes to the client side.
	buf := make([]byte, 1)
	if _, err := client.Read(buf); err != nil {
		t.Fatalf("client read response code: %v", err)
	}
	if buf[0] != responseCodeSuccess {
		t.Fatalf("response code = %d, want success", buf[0])
	}
	var resp ssztype.SSZUint64
	if err := fp.Encoding().DecodeWithMaxLength(client, &resp); err != nil {
		t.Fatalf("decode ping response: %v", err)
	}

	if err := <-done; err != nil {
		t.Fatalf("pingHandler returned error: %v", err)
	}
	if md, err := fp.Peers().GetPing(remote); err != nil || md == nil {
		t.Fatalf("expected ping metadata recorded for peer, err=%v md=%v", err, md)
	}
}

func TestValidateSequenceNumUnregisteredPeerErrors(t *testing.T) {
	svc, _ := newPingTestService(t)
	remote, err := test.RandPeerID()
	if err != nil {
		t.Fatalf("RandPeerID: %v", err)
	}
	// A peer that was never Add()-ed to the Status store is unknown to
	// GetPing, so validateSequenceNum must surface that error rather than
	// silently treating it as valid.
	if _, err := svc.validateSequenceNum(ssztype.SSZUint64(5), remote); err == nil {
		t.Fatal("expected error for an unregistered peer")
	}
}

func TestValidateSequenceNumNoStoredPingIsValid(t *testing.T) {
	svc, fp := newPingTestService(t)
	remote, err := test.RandPeerID()
	if err != nil {
		t.Fatalf("RandPeerID: %v", err)
	}
	fp.addConnectedPeer(t, remote, 1)

	valid, err := svc.validateSequenceNum(ssztype.SSZUint64(5), remote)
	if err != nil {
		t.Fatalf("validateSequenceNum: %v", err)
	}
	if !valid {
		t.Fatal("expected valid=true for a registered peer with no stored ping metadata")
	}
}

func TestValidateSequenceNumRejectsStaleSeq(t *testing.T) {
	svc, fp := newPingTestService(t)
	remote, err := test.RandPeerID()
	if err != nil {
		t.Fatalf("RandPeerID: %v", err)
	}
	fp.addConnectedPeer(t, remote, 1)
	fp.Peers().SetPing(remote, &sync_pb.Ping{SeqNumber: 10})

	valid, err := svc.validateSequenceNum(ssztype.SSZUint64(3), remote)
	if !errors.Is(err, p2ptypes.ErrInvalidSequenceNum) {
		t.Fatalf("expected ErrInvalidSequenceNum, got valid=%v err=%v", valid, err)
	}
}
