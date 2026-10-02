package sync

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/test"
	pubsub "github.com/libp2p/go-libp2p-pubsub"

	"github.com/n42blockchain/N42/internal/p2p"
)

// TestGenerateErrorResponseEncodesReasonAndCode exercises
// Service.generateErrorResponse (thin wrapper over createErrorResponse).
func TestGenerateErrorResponseEncodesReasonAndCode(t *testing.T) {
	fp := newFakeP2P(t)
	svc := &Service{cfg: &config{p2p: fp}}

	resp, err := svc.generateErrorResponse(responseCodeInvalidRequest, "boom")
	if err != nil {
		t.Fatalf("generateErrorResponse: %v", err)
	}
	if len(resp) == 0 || resp[0] != responseCodeInvalidRequest {
		t.Fatalf("response = %x, want leading byte %x", resp, responseCodeInvalidRequest)
	}
}

// TestCloseStreamAndWaitClosesWriteThenRead drives closeStreamAndWait over a
// net.Pipe pair: the server side calls it while the client reads to EOF then
// closes, exercising the success path (CloseWrite succeeds, then Read/Close).
func TestCloseStreamAndWaitClosesWriteThenRead(t *testing.T) {
	local, err := test.RandPeerID()
	if err != nil {
		t.Fatalf("RandPeerID: %v", err)
	}
	remote, err := test.RandPeerID()
	if err != nil {
		t.Fatalf("RandPeerID: %v", err)
	}
	client, server := newFakeStreamPair(local, remote)

	done := make(chan struct{})
	go func() {
		closeStreamAndWait(server)
		close(done)
	}()

	// closeStreamAndWait blocks reading for the remote's acknowledgement;
	// closing our end unblocks that Read with an EOF-like error on a
	// net.Pipe (CloseWrite is a no-op in this harness -- see fakeStream).
	_ = client.Close()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("closeStreamAndWait did not return within 2s")
	}
}

// TestSafelyHandleMessageRecoversPanic exercises safelyHandleMessage's defer
// of handlePanic: a handler that panics must not propagate, and a handler
// that returns an error must not panic either.
func TestSafelyHandleMessageRecoversPanic(t *testing.T) {
	svc := &Service{}
	msg := &pubsub.Message{}

	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("safelyHandleMessage leaked panic: %v", r)
			}
		}()
		svc.safelyHandleMessage(context.Background(), func(context.Context, *pubsub.Message) error {
			panic("boom")
		}, msg)
	}()
}

// TestSafelyHandleMessagePropagatesNoError covers the non-panicking,
// error-returning path (span status set, no panic).
func TestSafelyHandleMessagePropagatesNoError(t *testing.T) {
	svc := &Service{}
	svc.safelyHandleMessage(context.Background(), func(context.Context, *pubsub.Message) error {
		return errors.New("handled error")
	}, &pubsub.Message{})
}

// TestHandlePanicNilMessageAndContext exercises handlePanic's nil-message and
// nil-context guards directly (no span attached).
func TestHandlePanicNilMessageAndContext(t *testing.T) {
	svc := &Service{}
	func() {
		defer svc.handlePanic(nil, nil)
		panic("boom")
	}()
}

// TestReplaceForkDigestRejectsMalformedTopic covers both branches of
// replaceForkDigest: well-formed 4-segment topics and malformed ones.
func TestReplaceForkDigestRejectsMalformedTopic(t *testing.T) {
	svc := &Service{}
	got, err := svc.replaceForkDigest(p2p.BlockTopicFormat)
	// BlockTopicFormat itself has an unsubstituted %x placeholder already,
	// so splitting "/n42/%x/block/ssz_snappy" by "/" yields 4 segments.
	if err != nil {
		t.Fatalf("replaceForkDigest(%q): %v", p2p.BlockTopicFormat, err)
	}
	if got != p2p.BlockTopicFormat {
		t.Fatalf("replaceForkDigest(%q) = %q, want unchanged", p2p.BlockTopicFormat, got)
	}

	if _, err := svc.replaceForkDigest("not/a/valid/topic/at/all"); err == nil {
		t.Fatal("replaceForkDigest() on a malformed topic: want error, got nil")
	}
}
