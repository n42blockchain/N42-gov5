// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package api

import (
	"context"
	"testing"
	"time"
)

// TestMachineVerify_NoVerifiersReturnsOnContextDone covers the fast path:
// with no validVerifiers registered (the default; nothing populates this map
// at runtime today), MachineVerify does not subscribe to anything and simply
// waits for ctx.Done() before returning nil.
func TestMachineVerify_NoVerifiersReturnsOnContextDone(t *testing.T) {
	if len(validVerifiers) != 0 {
		t.Skip("validVerifiers is non-empty in this test binary; skip to avoid the subscription path")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- MachineVerify(ctx) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("MachineVerify() error = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("MachineVerify() did not return after context cancellation")
	}
}
