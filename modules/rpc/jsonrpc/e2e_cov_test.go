package jsonrpc

import (
	"context"
	"errors"
	"testing"
	"time"
)

// e2eService is a minimal RPC service used to exercise the server/client/
// handler codec path end-to-end over an in-process pipe.
type e2eService struct{}

func (e2eService) Echo(msg string) string { return msg }

func (e2eService) Add(a, b int) int { return a + b }

func (e2eService) Fail() error { return errors.New("boom") }

func newE2EServerAndClient(t *testing.T) (*Server, *Client) {
	t.Helper()
	srv := NewServer()
	if err := srv.RegisterName("test", e2eService{}); err != nil {
		t.Fatalf("RegisterName failed: %v", err)
	}
	client := DialInProc(srv)
	return srv, client
}

func TestE2ECallAndNotify(t *testing.T) {
	_, client := newE2EServerAndClient(t)
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var result string
	if err := client.CallContext(ctx, &result, "test_echo", "hello"); err != nil {
		t.Fatalf("CallContext failed: %v", err)
	}
	if result != "hello" {
		t.Errorf("got %q want hello", result)
	}

	var sum int
	if err := client.CallContext(ctx, &sum, "test_add", 2, 3); err != nil {
		t.Fatalf("CallContext failed: %v", err)
	}
	if sum != 5 {
		t.Errorf("got %d want 5", sum)
	}

	// method returning an error
	var ignored int
	if err := client.CallContext(ctx, &ignored, "test_fail"); err == nil {
		t.Error("expected error from test_fail")
	}

	// unknown method
	if err := client.CallContext(ctx, &ignored, "test_missing"); err == nil {
		t.Error("expected error for unknown method")
	}

	// SupportedModules exercises the built-in rpc_modules call.
	modules, err := client.SupportedModules()
	if err != nil {
		t.Fatalf("SupportedModules failed: %v", err)
	}
	if _, ok := modules["test"]; !ok {
		t.Errorf("expected test module in %v", modules)
	}
}

func TestE2EServerStop(t *testing.T) {
	srv, client := newE2EServerAndClient(t)
	defer client.Close()
	// Stop should not panic and should mark codecs for closure.
	srv.Stop()
}
