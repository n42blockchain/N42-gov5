// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package api

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
)

func TestHotStuffReconfigAPI_NoEngine(t *testing.T) {
	r := NewHotStuffReconfigAPI(func() *hotstuff.HotStuff { return nil })

	if err := r.ProposeAddValidator(context.Background(), types.Address{}, "00"); err == nil {
		t.Fatal("ProposeAddValidator(no engine) error = nil, want error")
	}
	if err := r.ProposeRemoveValidator(context.Background(), types.Address{}); err == nil {
		t.Fatal("ProposeRemoveValidator(no engine) error = nil, want error")
	}
	res := r.PendingReconfigChanges(context.Background())
	if res["error"] == nil {
		t.Fatalf("PendingReconfigChanges(no engine) = %v, want error field", res)
	}
}
