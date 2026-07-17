// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package node

import (
	"bytes"
	"testing"

	"github.com/n42blockchain/N42/internal/p2p/encoder"
)

func TestReadRotorStreamBounded(t *testing.T) {
	limit := int(encoder.MaxGossipSize)
	want := bytes.Repeat([]byte{0x42}, limit)
	got, err := readRotorStream(bytes.NewReader(want))
	if err != nil {
		t.Fatalf("at-limit stream rejected: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("at-limit stream changed during read")
	}

	tooLarge := bytes.NewReader(bytes.Repeat([]byte{0x42}, limit+1))
	if _, err := readRotorStream(tooLarge); err == nil {
		t.Fatal("oversized direct stream was accepted")
	}
}
