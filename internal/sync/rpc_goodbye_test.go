package sync

import (
	"context"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/core/test"

	ssztype "github.com/n42blockchain/N42/common/types/ssz"
	"github.com/n42blockchain/N42/internal/p2p"
	"github.com/n42blockchain/N42/internal/p2p/p2ptypes"
)

func TestGoodbyeRPCHandlerWrongMessageType(t *testing.T) {
	svc, fp := newPingTestService(t)
	remote, err := test.RandPeerID()
	if err != nil {
		t.Fatalf("RandPeerID: %v", err)
	}
	client, server := newFakeStreamPair(remote, fp.self)
	defer client.Close()

	wrong := "not-a-uint64"
	if err := svc.goodbyeRPCHandler(context.Background(), &wrong, server); err == nil {
		t.Fatal("expected error for wrong message type")
	}
}

func TestGoodbyeRPCHandlerDisconnects(t *testing.T) {
	svc, fp := newPingTestService(t)
	remote, err := test.RandPeerID()
	if err != nil {
		t.Fatalf("RandPeerID: %v", err)
	}
	fp.addConnectedPeer(t, remote, 1)

	client, server := newFakeStreamPair(remote, fp.self)
	defer client.Close()
	_ = server.SetProtocol(protocol.ID(p2p.RPCGoodByeTopicV1 + fp.Encoding().ProtocolSuffix()))

	code := ssztype.SSZUint64(p2ptypes.GoodbyeCodeClientShutdown)
	if err := svc.goodbyeRPCHandler(context.Background(), &code, server); err != nil {
		t.Fatalf("goodbyeRPCHandler: %v", err)
	}

	if len(fp.disconnected) != 1 || fp.disconnected[0] != remote {
		t.Fatalf("expected Disconnect(remote) to be called, got %v", fp.disconnected)
	}
	nextValid, err := fp.Peers().NextValidTime(remote)
	if err != nil {
		t.Fatalf("NextValidTime: %v", err)
	}
	if !nextValid.After(time.Now()) {
		t.Fatalf("expected backoff to be set in the future for ClientShutdown, got %v", nextValid)
	}
}

func TestGoodbyeMessageKnownAndUnknownCodes(t *testing.T) {
	if msg := goodbyeMessage(p2ptypes.GoodbyeCodeClientShutdown); msg == "" {
		t.Fatal("expected a non-empty message for a known goodbye code")
	}
	if msg := goodbyeMessage(p2ptypes.RPCGoodbyeCode(9999)); msg == "" {
		t.Fatal("expected a fallback message for an unknown goodbye code")
	}
}

func TestGoodByeBackoffKnownAndUnknownCodes(t *testing.T) {
	if backoff := goodByeBackoff(p2ptypes.GoodbyeCodeTooManyPeers); !backoff.After(time.Now()) {
		t.Fatalf("expected a future backoff for a known code, got %v", backoff)
	}
	if backoff := goodByeBackoff(p2ptypes.RPCGoodbyeCode(9999)); !backoff.IsZero() {
		t.Fatalf("expected zero backoff for an unknown code, got %v", backoff)
	}
}

func TestDisconnectBadPeerSkipsGoodPeers(t *testing.T) {
	svc, fp := newPingTestService(t)
	remote, err := test.RandPeerID()
	if err != nil {
		t.Fatalf("RandPeerID: %v", err)
	}
	fp.addConnectedPeer(t, remote, 1)

	svc.disconnectBadPeer(context.Background(), remote)
	if len(fp.disconnected) != 0 {
		t.Fatalf("expected no disconnect for a peer that is not bad, got %v", fp.disconnected)
	}
}
