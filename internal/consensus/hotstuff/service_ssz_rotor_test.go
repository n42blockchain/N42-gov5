package hotstuff

import (
	"context"
	"testing"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/n42blockchain/N42/internal/p2p/encoder"
)

// TestRawSSZMarshaler covers the small ssz.Marshaler/Unmarshaler adapter used
// for gossip compression.
func TestRawSSZMarshaler(t *testing.T) {
	r := &rawSSZMarshaler{data: []byte{0x01, 0x02, 0x03}}

	got, err := r.MarshalSSZ()
	if err != nil || string(got) != string(r.data) {
		t.Fatalf("MarshalSSZ: got %v err %v", got, err)
	}

	buf, err := r.MarshalSSZTo([]byte{0xFF})
	if err != nil || string(buf) != string([]byte{0xFF, 0x01, 0x02, 0x03}) {
		t.Fatalf("MarshalSSZTo: got %v err %v", buf, err)
	}

	if n := r.SizeSSZ(); n != 3 {
		t.Fatalf("SizeSSZ: expected 3, got %d", n)
	}

	var r2 rawSSZMarshaler
	if err := r2.UnmarshalSSZ([]byte{0x09, 0x08}); err != nil {
		t.Fatalf("UnmarshalSSZ: %v", err)
	}
	if len(r2.data) != 2 || r2.data[0] != 0x09 || r2.data[1] != 0x08 {
		t.Fatalf("UnmarshalSSZ: unexpected data %v", r2.data)
	}
}

// fakeDirectSender is a minimal P2PDirectSender for exercising serviceSender.
type fakeDirectSender struct {
	gotData  []byte
	gotTopic string
	gotPid   peer.ID
}

func (f *fakeDirectSender) SendRawBytes(_ context.Context, data []byte, topic string, pid peer.ID) error {
	f.gotData = data
	f.gotTopic = topic
	f.gotPid = pid
	return nil
}

func (f *fakeDirectSender) PublishToTopic(_ context.Context, _ string, _ []byte, _ ...pubsub.PubOpt) error {
	return nil
}

func (f *fakeDirectSender) SubscribeToTopic(_ string, _ ...pubsub.SubOpt) (*pubsub.Subscription, error) {
	return nil, nil
}

func (f *fakeDirectSender) Encoding() encoder.NetworkEncoding { return nil }

func (f *fakeDirectSender) SetStreamHandler(string, func(data []byte, from peer.ID)) {}

func (f *fakeDirectSender) ConnectedPeers() []peer.ID { return nil }

// TestServiceSender_SendRawDirect covers the P2PDirectSender adapter.
func TestServiceSender_SendRawDirect(t *testing.T) {
	fake := &fakeDirectSender{}
	ss := &serviceSender{sender: fake}

	if err := ss.SendRawDirect(context.Background(), []byte("hi"), "topic-x", peer.ID("p1")); err != nil {
		t.Fatalf("SendRawDirect: unexpected error: %v", err)
	}
	if string(fake.gotData) != "hi" || fake.gotTopic != "topic-x" || fake.gotPid != peer.ID("p1") {
		t.Fatalf("SendRawDirect did not forward arguments correctly: %+v", fake)
	}
}

// TestService_RotorAccessor covers the exported Rotor() getter.
func TestService_RotorAccessor(t *testing.T) {
	r := NewRotor(2)
	s := &Service{rotor: r}
	if s.Rotor() != r {
		t.Fatalf("Rotor(): expected the stored rotor instance back")
	}
}
