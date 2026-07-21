// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package p2p

import "testing"

func TestCanSubscribeH2V4CrossClientTopic(t *testing.T) {
	service := &Service{}
	if !service.CanSubscribe(H2V4Topic + "/ssz_snappy") {
		t.Fatal("canonical H2-v4 topic was rejected")
	}
	if service.CanSubscribe("/n42/h2/5/ssz_snappy") {
		t.Fatal("unknown H2 protocol version was accepted")
	}
	if _, err := service.topicScoreParams(H2V4Topic + "/ssz_snappy"); err != nil {
		t.Fatalf("H2-v4 topic has no scoring parameters: %v", err)
	}
}
