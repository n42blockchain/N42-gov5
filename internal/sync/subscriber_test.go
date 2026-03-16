package sync

import (
	"context"
	"errors"
	"testing"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
)

func TestShouldLogSubscriptionErrorSuppressesExpectedShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	tests := []struct {
		name string
		err  error
	}{
		{name: "nil", err: nil},
		{name: "context canceled", err: context.Canceled},
		{name: "subscription canceled", err: pubsub.ErrSubscriptionCancelled},
		{name: "other error after context canceled", err: errors.New("shutdown")},
	}

	for _, tt := range tests {
		if shouldLogSubscriptionError(ctx, tt.err) {
			t.Fatalf("%s: shouldLogSubscriptionError() = true, want false", tt.name)
		}
	}
}

func TestShouldLogSubscriptionErrorReportsUnexpectedFailure(t *testing.T) {
	if !shouldLogSubscriptionError(context.Background(), errors.New("boom")) {
		t.Fatal("shouldLogSubscriptionError() = false, want true")
	}
}
