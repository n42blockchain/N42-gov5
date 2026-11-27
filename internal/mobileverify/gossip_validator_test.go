// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package mobileverify

import (
	"context"
	"testing"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
	pb "github.com/libp2p/go-libp2p-pubsub/pb"
	"github.com/libp2p/go-libp2p/core/peer"
	mocknet "github.com/libp2p/go-libp2p/p2p/net/mock"

	"github.com/n42blockchain/N42/cmd/evmsdk"
)

func mvPubsubMsg(data []byte, local bool) *pubsub.Message {
	return &pubsub.Message{Message: &pb.Message{Data: data}, Local: local}
}

// AM36 on the packet topic: an undecodable or forged packet is rejected, a
// packet outside the head window ignored, a good one accepted and cached by
// the receive path from the validator's result.
func TestPacketTopicValidator(t *testing.T) {
	svc := NewPacketService(NewPacketCache(8), nil, "/test")
	svc.SetHeadNumber(func() uint64 { return 100 })
	ctx := context.Background()

	if r := svc.topicValidator(ctx, "p", mvPubsubMsg([]byte{1, 2, 3}, false)); r != pubsub.ValidationReject {
		t.Fatalf("garbage: %v, want reject", r)
	}
	_, other := buildTestPacket(t, 99)
	pkt, err := decodePacketForTest(other)
	if err != nil {
		t.Fatal(err)
	}
	pkt.BlockHash[0] ^= 0xff
	forged, err := evmsdk.EncodeStreamPacket(pkt)
	if err != nil {
		t.Fatal(err)
	}
	if r := svc.topicValidator(ctx, "p", mvPubsubMsg(forged, false)); r != pubsub.ValidationReject {
		t.Fatalf("packet not matching its header: %v, want reject", r)
	}
	_, far := buildTestPacket(t, 50)
	if r := svc.topicValidator(ctx, "p", mvPubsubMsg(far, false)); r != pubsub.ValidationIgnore {
		t.Fatalf("stale packet: %v, want ignore", r)
	}
	if r := svc.topicValidator(ctx, "p", mvPubsubMsg(far, true)); r != pubsub.ValidationAccept {
		t.Fatalf("own packet: %v, want accept", r)
	}
	hash, good := buildTestPacket(t, 100)
	m := mvPubsubMsg(good, false)
	if r := svc.topicValidator(ctx, "p", m); r != pubsub.ValidationAccept {
		t.Fatalf("good packet: %v, want accept", r)
	}
	vp, ok := m.ValidatorData.(*validatedPacket)
	if !ok || vp.hash != hash || vp.number != 100 {
		t.Fatalf("validator data %#v, want hash %x number 100", m.ValidatorData, hash)
	}
}

// AM36 on the registration topic: anything but pubkey(48) || pop(96) is
// rejected; a well-formed announcement is accepted (its PoP is checked on
// adoption).
func TestRegistrationTopicValidator(t *testing.T) {
	d := newDevice(t)
	enc := encodeRegistration(d.pubkey, d.pop())
	ctx := context.Background()
	if r := registrationTopicValidator(ctx, "p", mvPubsubMsg(enc, false)); r != pubsub.ValidationAccept {
		t.Fatalf("registration: %v, want accept", r)
	}
	for _, bad := range [][]byte{nil, enc[:len(enc)-1], append(append([]byte(nil), enc...), 0)} {
		if r := registrationTopicValidator(ctx, "p", mvPubsubMsg(bad, false)); r != pubsub.ValidationReject {
			t.Fatalf("%d-byte registration: %v, want reject", len(bad), r)
		}
	}
}

// mvP2P is a real gossipsub router that exposes PubSub(), as p2p.Service does.
type mvP2P struct{ ps *pubsub.PubSub }

func (p mvP2P) PublishToTopic(ctx context.Context, topic string, data []byte, opts ...pubsub.PubOpt) error {
	return nil
}

func (p mvP2P) SubscribeToTopic(topic string, opts ...pubsub.SubOpt) (*pubsub.Subscription, error) {
	th, err := p.ps.Join(topic)
	if err != nil {
		return nil, err
	}
	return th.Subscribe(opts...)
}

func (p mvP2P) PubSub() *pubsub.PubSub { return p.ps }

// The validators are registered only when enabled (profile 1), before the
// subscription, and removed on Stop.
func TestMobileTopicValidatorsRegisteredWhenEnabled(t *testing.T) {
	mn, err := mocknet.FullMeshConnected(1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mn.Close() })
	ps, err := pubsub.NewGossipSub(context.Background(), mn.Hosts()[0])
	if err != nil {
		t.Fatal(err)
	}
	p := mvP2P{ps: ps}
	noop := func(context.Context, peer.ID, *pubsub.Message) pubsub.ValidationResult {
		return pubsub.ValidationAccept
	}
	registered := func(topic string) bool {
		if err := ps.RegisterTopicValidator(topic, noop); err != nil {
			return true // duplicate: the service's validator is in place
		}
		_ = ps.UnregisterTopicValidator(topic)
		return false
	}

	legacy := NewPacketService(NewPacketCache(8), p, "/mv/legacy")
	if err := legacy.Start(); err != nil {
		t.Fatal(err)
	}
	if registered("/mv/legacy") {
		t.Fatal("validator registered without EnableGossipValidation")
	}
	legacy.Stop()

	pkts := NewPacketService(NewPacketCache(8), p, "/mv/packet")
	pkts.EnableGossipValidation()
	if err := pkts.Start(); err != nil {
		t.Fatal(err)
	}
	if !registered("/mv/packet") {
		t.Fatal("packet topic validator not registered")
	}
	pkts.Stop()
	if registered("/mv/packet") {
		t.Fatal("packet topic validator left after Stop")
	}

	regs := NewRegistrationService(NewRegistry(), p, "/mv/reg")
	regs.EnableGossipValidation()
	if err := regs.Start(); err != nil {
		t.Fatal(err)
	}
	if !registered("/mv/reg") {
		t.Fatal("registration topic validator not registered")
	}
	regs.Stop()
	if registered("/mv/reg") {
		t.Fatal("registration topic validator left after Stop")
	}
}
