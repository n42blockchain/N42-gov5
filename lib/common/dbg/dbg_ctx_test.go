package dbg

import (
	"context"
	"testing"
)

func TestEnabledNoValue(t *testing.T) {
	if Enabled(context.Background()) {
		t.Fatal("expected Enabled to be false for a plain context")
	}
}

func TestContextWithDebug(t *testing.T) {
	ctx := ContextWithDebug(context.Background(), true)
	if !Enabled(ctx) {
		t.Fatal("expected Enabled to be true after ContextWithDebug(true)")
	}

	ctx = ContextWithDebug(context.Background(), false)
	if Enabled(ctx) {
		t.Fatal("expected Enabled to be false after ContextWithDebug(false)")
	}
}

func TestHTTPHeaderConst(t *testing.T) {
	if HTTPHeader != "dbg" {
		t.Fatalf("HTTPHeader = %q, want %q", HTTPHeader, "dbg")
	}
}
