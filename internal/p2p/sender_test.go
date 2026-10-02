// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package p2p

import (
	"context"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	ssztype "github.com/n42blockchain/N42/common/types/ssz"
)

func TestSendOverMocknet(t *testing.T) {
	a, b := newLinkedServicePair(t)

	received := make(chan struct{}, 1)
	b.SetStreamHandler(RPCPingTopicV1+b.Encoding().ProtocolSuffix(), func(stream network.Stream) {
		// Read until the sender's EncodeWithMaxLength + CloseWrite signal
		// EOF. Closing this end early (even after the first chunk) races
		// the sender's remaining writes on a mock stream and surfaces as a
		// spurious "stream closed" error on the Send side, so read to
		// completion first and let the test's cleanup tear the stream down.
		buf := make([]byte, 256)
		for {
			_, err := stream.Read(buf)
			if err != nil {
				break
			}
		}
		received <- struct{}{}
	})

	msg := new(ssztype.SSZUint64)
	*msg = 7

	stream, err := a.Send(context.Background(), msg, RPCPingTopicV1, b.host.ID())
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	defer stream.Close()

	select {
	case <-received:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for the peer to receive the sent message")
	}
}

func TestSendRejectsWrongMessageType(t *testing.T) {
	a, b := newLinkedServicePair(t)
	// A plain string does not satisfy ssz.Marshaler, and the topic mapping
	// for RPCPingTopicV1 expects *ssztype.SSZUint64 -- VerifyTopicMapping
	// should reject the mismatch before any stream is opened.
	if _, err := a.Send(context.Background(), "not-the-right-type", RPCPingTopicV1, b.host.ID()); err == nil {
		t.Fatal("expected an error for a message type that fails VerifyTopicMapping")
	}
}
