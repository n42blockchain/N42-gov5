package evmsdk

import (
	"context"
	"testing"
	"time"
)

func TestEngineStartAlreadyRunningKeepsExistingContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	engine := &EvmEngine{
		ctx:         ctx,
		cancelFunc:  cancel,
		EngineState: EngineStateRunning,
	}

	err := engine.Start()
	if err == nil || err.Error() != "evme is running" {
		t.Fatalf("Start() error = %v", err)
	}
	if engine.ctx != ctx {
		t.Fatal("Start() replaced existing context on already-running engine")
	}
}

func TestEngineStartFailureClearsContext(t *testing.T) {
	engine := &EvmEngine{
		PrivKey:   "not-hex",
		ServerUri: "ws://127.0.0.1:1",
	}

	if err := engine.Start(); err == nil {
		t.Fatal("Start() expected error for invalid private key")
	}
	if engine.EngineState != EngineStateStopped {
		t.Fatalf("EngineState = %q, want %q", engine.EngineState, EngineStateStopped)
	}
	if engine.ctx != nil {
		t.Fatal("Start() left context set after startup failure")
	}
	if engine.cancelFunc != nil {
		t.Fatal("Start() left cancelFunc set after startup failure")
	}
}

func TestSendVerificationResultCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan []byte)
	done := make(chan bool, 1)

	go func() {
		done <- sendVerificationResult(ctx, out, []byte("result"))
	}()

	cancel()

	select {
	case ok := <-done:
		if ok {
			t.Fatal("sendVerificationResult() = true, want false after cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("sendVerificationResult() blocked after context cancellation")
	}
}

func TestSendVerificationResultDelivered(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	out := make(chan []byte, 1)
	if ok := sendVerificationResult(ctx, out, []byte("result")); !ok {
		t.Fatal("sendVerificationResult() = false, want true")
	}

	select {
	case got := <-out:
		if string(got) != "result" {
			t.Fatalf("got %q, want %q", got, "result")
		}
	case <-time.After(time.Second):
		t.Fatal("sendVerificationResult() did not deliver message")
	}
}
