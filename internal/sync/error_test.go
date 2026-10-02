package sync

import (
	"errors"
	"testing"

	"github.com/libp2p/go-libp2p/core/network"

	"github.com/n42blockchain/N42/internal/p2p/encoder"
)

type testEncodingProvider struct{}

func (testEncodingProvider) Encoding() encoder.NetworkEncoding {
	return encoder.SszNetworkEncoder{}
}

func TestCreateErrorResponse(t *testing.T) {
	resp, err := createErrorResponse(responseCodeInvalidRequest, "bad request", testEncodingProvider{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp) == 0 {
		t.Fatalf("expected non-empty response")
	}
	if resp[0] != responseCodeInvalidRequest {
		t.Fatalf("expected first byte to be the response code, got %x", resp[0])
	}
}

func TestIsValidStreamError(t *testing.T) {
	if isValidStreamError(nil) {
		t.Fatalf("expected false for nil error")
	}
	if isValidStreamError(network.ErrReset) {
		t.Fatalf("expected false for network.ErrReset")
	}
	if !isValidStreamError(errors.New("some other error")) {
		t.Fatalf("expected true for a non-reset error")
	}
}
