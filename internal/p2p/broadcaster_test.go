// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package p2p

import (
	"context"
	"fmt"
	"testing"
	"time"

	ssztype "github.com/n42blockchain/N42/common/types/ssz"
)

func TestRawGossipBytesRoundTrip(t *testing.T) {
	r := &rawGossipBytes{data: []byte("payload")}
	if r.SizeSSZ() != len("payload") {
		t.Fatalf("SizeSSZ() = %d, want %d", r.SizeSSZ(), len("payload"))
	}
	marshaled, err := r.MarshalSSZ()
	if err != nil {
		t.Fatalf("MarshalSSZ: %v", err)
	}
	if string(marshaled) != "payload" {
		t.Fatalf("MarshalSSZ = %q, want %q", marshaled, "payload")
	}
	buf, err := r.MarshalSSZTo([]byte("prefix:"))
	if err != nil {
		t.Fatalf("MarshalSSZTo: %v", err)
	}
	if string(buf) != "prefix:payload" {
		t.Fatalf("MarshalSSZTo = %q, want %q", buf, "prefix:payload")
	}

	var r2 rawGossipBytes
	if err := r2.UnmarshalSSZ([]byte("roundtrip")); err != nil {
		t.Fatalf("UnmarshalSSZ: %v", err)
	}
	if string(r2.data) != "roundtrip" {
		t.Fatalf("after UnmarshalSSZ, data = %q, want %q", r2.data, "roundtrip")
	}
}

func TestBroadcastBlockOverMocknet(t *testing.T) {
	a, b := newLinkedServicePair(t)

	digest, err := a.currentForkDigest()
	if err != nil {
		t.Fatalf("currentForkDigest: %v", err)
	}
	blockTopic := fmt.Sprintf(BlockTopicFormat, digest) + a.Encoding().ProtocolSuffix()

	sub, err := b.SubscribeToTopic(blockTopic)
	if err != nil {
		t.Fatalf("SubscribeToTopic: %v", err)
	}
	topicHandle, err := a.JoinTopic(blockTopic)
	if err != nil {
		t.Fatalf("JoinTopic: %v", err)
	}
	waitForTopicPeer(t, topicHandle)

	payload := []byte{0xde, 0xad, 0xbe, 0xef}
	if err := a.BroadcastBlock(context.Background(), payload); err != nil {
		t.Fatalf("BroadcastBlock: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	msg, err := sub.Next(ctx)
	if err != nil {
		t.Fatalf("sub.Next: %v", err)
	}
	// The payload travels through the SSZ+snappy EncodeGossip envelope, so
	// decode it the same way the receiver would via the raw-bytes wrapper.
	var decoded rawGossipBytes
	if err := a.Encoding().DecodeGossip(msg.Data, &decoded); err != nil {
		t.Fatalf("DecodeGossip: %v", err)
	}
	if string(decoded.data) != string(payload) {
		t.Fatalf("decoded payload = %x, want %x", decoded.data, payload)
	}
}

func TestBroadcastTransactionOverMocknet(t *testing.T) {
	a, b := newLinkedServicePair(t)

	digest, err := a.currentForkDigest()
	if err != nil {
		t.Fatalf("currentForkDigest: %v", err)
	}
	txTopic := fmt.Sprintf(TransactionTopicFormat, digest) + a.Encoding().ProtocolSuffix()

	sub, err := b.SubscribeToTopic(txTopic)
	if err != nil {
		t.Fatalf("SubscribeToTopic: %v", err)
	}
	topicHandle, err := a.JoinTopic(txTopic)
	if err != nil {
		t.Fatalf("JoinTopic: %v", err)
	}
	waitForTopicPeer(t, topicHandle)

	payload := []byte{0x01, 0x02, 0x03}
	if err := a.BroadcastTransaction(context.Background(), payload); err != nil {
		t.Fatalf("BroadcastTransaction: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := sub.Next(ctx); err != nil {
		t.Fatalf("sub.Next: %v", err)
	}
}

func TestVerifyTopicMapping(t *testing.T) {
	if err := VerifyTopicMapping(RPCPingTopicV1, new(ssztype.SSZUint64)); err != nil {
		t.Fatalf("VerifyTopicMapping with the correct type: %v", err)
	}
	if err := VerifyTopicMapping("/rpc/not_registered/1", new(ssztype.SSZUint64)); err == nil {
		t.Fatal("expected an error for an unregistered topic")
	}
	if err := VerifyTopicMapping(RPCPingTopicV1, "wrong type"); err == nil {
		t.Fatal("expected an error for a mismatched message type")
	}
}
