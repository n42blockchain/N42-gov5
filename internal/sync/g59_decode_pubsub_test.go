package sync

import (
	"bytes"
	"testing"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
	pb "github.com/libp2p/go-libp2p-pubsub/pb"

	"github.com/n42blockchain/N42/internal/p2p/encoder"
)

func newDecodeTestService() *Service {
	return &Service{cfg: &config{p2p: &fakeP2P{}}}
}

func TestDecodePubsubMessageNilGuards(t *testing.T) {
	svc := newDecodeTestService()
	if _, err := svc.decodePubsubMessage(nil); err != errNilPubsubMessage {
		t.Fatalf("decodePubsubMessage(nil) err = %v, want %v", err, errNilPubsubMessage)
	}
	if _, err := svc.decodePubsubMessage(&pubsub.Message{Message: &pb.Message{}}); err != errNilPubsubMessage {
		t.Fatalf("decodePubsubMessage(no topic) err = %v, want %v", err, errNilPubsubMessage)
	}
	empty := ""
	if _, err := svc.decodePubsubMessage(&pubsub.Message{Message: &pb.Message{Topic: &empty}}); err != errNilPubsubMessage {
		t.Fatalf("decodePubsubMessage(empty topic) err = %v, want %v", err, errNilPubsubMessage)
	}
}

func TestDecodePubsubMessageInvalidGossipDigest(t *testing.T) {
	svc := newDecodeTestService()
	topic := "not-a-gossip-topic"
	if _, err := svc.decodePubsubMessage(&pubsub.Message{Message: &pb.Message{Topic: &topic}}); err == nil {
		t.Fatal("expected an error for a topic outside the gossip prefix")
	}
}

func TestDecodePubsubMessageUnhandledTopic(t *testing.T) {
	svc := newDecodeTestService()
	topic := "/n42/00000000/unknown-topic/ssz_snappy"
	_, err := svc.decodePubsubMessage(&pubsub.Message{Message: &pb.Message{Topic: &topic}})
	if err == nil {
		t.Fatal("expected errUnhandledTopic for an unregistered topic")
	}
}

func TestDecodePubsubMessageDecodesBlockTopic(t *testing.T) {
	svc := newDecodeTestService()
	topic := "/n42/00000000/block/ssz_snappy"

	var wire bytes.Buffer
	enc := encoder.SszNetworkEncoder{}
	payload := []byte("hello-block")
	if _, err := enc.EncodeGossip(&wire, &rawSSZBytes{data: payload}); err != nil {
		t.Fatalf("EncodeGossip: %v", err)
	}

	msg := &pubsub.Message{Message: &pb.Message{Topic: &topic, Data: wire.Bytes()}}
	decoded, err := svc.decodePubsubMessage(msg)
	if err != nil {
		t.Fatalf("decodePubsubMessage: %v", err)
	}
	raw, ok := decoded.(*rawSSZBytes)
	if !ok || !bytes.Equal(raw.data, payload) {
		t.Fatalf("decodePubsubMessage() = %#v, want payload %q", decoded, payload)
	}
}
