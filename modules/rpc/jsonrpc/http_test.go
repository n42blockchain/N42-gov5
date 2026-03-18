package jsonrpc

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

func TestHTTPConnWriteJSONReturnsError(t *testing.T) {
	hc := &httpConn{}

	err := hc.writeJSON(context.Background(), map[string]any{"jsonrpc": "2.0"})
	if !errors.Is(err, errHTTPWriteUnsupported) {
		t.Fatalf("writeJSON() error = %v, want %v", err, errHTTPWriteUnsupported)
	}
}

func TestValidateRequestAllowsLargeEnginePayload(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "http://example.com", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	req.Header.Set("content-type", contentType)
	req.ContentLength = 16 * 1024 * 1024

	code, err := validateRequest(req)
	if code != 0 || err != nil {
		t.Fatalf("validateRequest() = (%d, %v), want (0, nil)", code, err)
	}
}

func TestValidateRequestRejectsOversizedPayload(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "http://example.com", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	req.Header.Set("content-type", contentType)
	req.ContentLength = maxRequestContentLength + 1

	code, err := validateRequest(req)
	if code != http.StatusRequestEntityTooLarge {
		t.Fatalf("validateRequest() code = %d, want %d", code, http.StatusRequestEntityTooLarge)
	}
	if err == nil {
		t.Fatal("validateRequest() error = nil, want oversized payload error")
	}
}
