package jsonrpc

import (
	"context"
	"errors"
	"testing"
)

func TestHTTPConnWriteJSONReturnsError(t *testing.T) {
	hc := &httpConn{}

	err := hc.writeJSON(context.Background(), map[string]any{"jsonrpc": "2.0"})
	if !errors.Is(err, errHTTPWriteUnsupported) {
		t.Fatalf("writeJSON() error = %v, want %v", err, errHTTPWriteUnsupported)
	}
}
