package mev

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/holiman/uint256"
	"go.uber.org/zap"

	"github.com/n42blockchain/N42/common/types"
)

// TestNewRelayClient_NoURLs covers the nil-return guard when no relay URLs
// are configured.
func TestNewRelayClient_NoURLs(t *testing.T) {
	if c := NewRelayClient(nil, zap.NewNop()); c != nil {
		t.Fatalf("NewRelayClient(nil) = %#v, want nil", c)
	}
	if c := NewRelayClient([]string{}, zap.NewNop()); c != nil {
		t.Fatalf("NewRelayClient([]string{}) = %#v, want nil", c)
	}
}

// TestRelayClientRegisterValidator_Success covers the happy path through
// RegisterValidator and postJSON: all relays accept and the call returns
// nil, with ValidatorPubkey reflecting the stored key.
func TestRelayClientRegisterValidator_Success(t *testing.T) {
	var gotBody []RegisterValidatorRequest
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer relay.Close()

	client := NewRelayClient([]string{relay.URL}, zap.NewNop())
	err := client.RegisterValidator(context.Background(), &RegisterValidatorRequest{
		GasLimit:  30000000,
		Timestamp: 123,
		Pubkey:    []byte{0xAB, 0xCD},
	})
	if err != nil {
		t.Fatalf("RegisterValidator() error = %v", err)
	}
	if len(gotBody) != 1 || gotBody[0].GasLimit != 30000000 {
		t.Fatalf("relay received unexpected body: %+v", gotBody)
	}

	pk := client.ValidatorPubkey()
	if len(pk) != 2 || pk[0] != 0xAB || pk[1] != 0xCD {
		t.Fatalf("ValidatorPubkey() = %v, want [0xAB 0xCD]", pk)
	}
}

// TestRelayClientRegisterValidator_NoRelays covers the ErrNoRelays guard.
func TestRelayClientRegisterValidator_NoRelays(t *testing.T) {
	client := &RelayClient{}
	err := client.RegisterValidator(context.Background(), &RegisterValidatorRequest{})
	if !errors.Is(err, ErrNoRelays) {
		t.Fatalf("RegisterValidator() error = %v, want ErrNoRelays", err)
	}
}

// TestRelayClientRegisterValidator_AllFail covers the path where every relay
// rejects registration: the aggregate error should wrap the last relay's
// error since success count stays zero.
func TestRelayClientRegisterValidator_AllFail(t *testing.T) {
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer relay.Close()

	client := NewRelayClient([]string{relay.URL}, zap.NewNop())
	err := client.RegisterValidator(context.Background(), &RegisterValidatorRequest{Pubkey: []byte{0x01}})
	if err == nil {
		t.Fatal("RegisterValidator() error = nil, want failure when all relays reject")
	}
}

// TestRelayClientGetPayload_Success covers GetPayload's happy path: it
// posts the blinded block to the relay that won the last GetHeader call and
// decodes the returned ExecutionPayload.
func TestRelayClientGetPayload_Success(t *testing.T) {
	wantHash := types.HexToHash("0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(&ExecutionPayload{
			BlockNumber: 7,
			BlockHash:   wantHash,
		})
	}))
	defer relay.Close()

	client := NewRelayClient([]string{relay.URL}, zap.NewNop())
	// Seed bestBid directly to avoid depending on GetHeader in this test.
	client.mu.Lock()
	client.bestBid = &BuilderBid{RelayURL: relay.URL, Value: uint256.NewInt(1)}
	client.mu.Unlock()

	payload, err := client.GetPayload(context.Background(), []byte(`{"stub":true}`))
	if err != nil {
		t.Fatalf("GetPayload() error = %v", err)
	}
	if payload.BlockHash != wantHash || payload.BlockNumber != 7 {
		t.Fatalf("payload = %+v, want hash %s number 7", payload, wantHash.Hex())
	}

	if got := client.BestBid(); got == nil || got.RelayURL != relay.URL {
		t.Fatalf("BestBid() = %+v, want relay %s", got, relay.URL)
	}
}

// TestRelayClientGetPayload_NoBid covers the ErrNoValidBid guard when no bid
// has been recorded yet.
func TestRelayClientGetPayload_NoBid(t *testing.T) {
	client := NewRelayClient([]string{"http://relay.invalid"}, zap.NewNop())
	_, err := client.GetPayload(context.Background(), []byte("{}"))
	if !errors.Is(err, ErrNoValidBid) {
		t.Fatalf("GetPayload() error = %v, want ErrNoValidBid", err)
	}
}

// TestRelayClientGetPayload_InvalidPayload covers the zero-BlockHash guard,
// which rejects a relay response that decodes but carries no block hash.
func TestRelayClientGetPayload_InvalidPayload(t *testing.T) {
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(&ExecutionPayload{})
	}))
	defer relay.Close()

	client := NewRelayClient([]string{relay.URL}, zap.NewNop())
	client.mu.Lock()
	client.bestBid = &BuilderBid{RelayURL: relay.URL, Value: uint256.NewInt(1)}
	client.mu.Unlock()

	_, err := client.GetPayload(context.Background(), []byte("{}"))
	if !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("GetPayload() error = %v, want ErrInvalidPayload", err)
	}
}

// TestRelayClientGetPayload_RelayError covers the non-2xx relay-response
// branch of GetPayload.
func TestRelayClientGetPayload_RelayError(t *testing.T) {
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("relay down"))
	}))
	defer relay.Close()

	client := NewRelayClient([]string{relay.URL}, zap.NewNop())
	client.mu.Lock()
	client.bestBid = &BuilderBid{RelayURL: relay.URL, Value: uint256.NewInt(1)}
	client.mu.Unlock()

	_, err := client.GetPayload(context.Background(), []byte("{}"))
	if !errors.Is(err, ErrRelayResponse) {
		t.Fatalf("GetPayload() error = %v, want ErrRelayResponse", err)
	}
}

// TestRelayClientBestBid_Nil covers BestBid's zero-value path before any
// bid has ever been recorded.
func TestRelayClientBestBid_Nil(t *testing.T) {
	client := NewRelayClient([]string{"http://relay.invalid"}, zap.NewNop())
	if bid := client.BestBid(); bid != nil {
		t.Fatalf("BestBid() = %+v, want nil", bid)
	}
}
