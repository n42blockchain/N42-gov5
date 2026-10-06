package diagnostics

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"
)

// waitFor polls cond until it returns true or the deadline elapses, sending
// `send` repeatedly in between (since the listener goroutines in this
// package subscribe asynchronously via StartProviders).
func waitFor(t *testing.T, send func(), cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(200 * time.Millisecond)
	for {
		send()
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestHeadersListenersAndJSON(t *testing.T) {
	c := newTestClient(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c.setupHeadersDiagnostics(ctx)

	waitFor(t, func() { Send(HeadersWaitingUpdate{From: 11}) }, func() bool {
		c.headerMutex.Lock()
		defer c.headerMutex.Unlock()
		return c.headers.WaitingForHeaders == 11
	})

	wantWrite := BlockHeadersUpdate{CurrentBlockNumber: 22}
	waitFor(t, func() { Send(wantWrite) }, func() bool {
		c.headerMutex.Lock()
		defer c.headerMutex.Unlock()
		return c.headers.WriteHeaders == wantWrite
	})

	wantMarker := HeaderCanonicalMarkerUpdate{AncestorHeight: 33, AncestorHash: "0xabc"}
	waitFor(t, func() { Send(wantMarker) }, func() bool {
		c.headerMutex.Lock()
		defer c.headerMutex.Unlock()
		return c.headers.CanonicalMarker == wantMarker
	})

	wantProcessed := HeadersProcessedUpdate{Highest: 44}
	waitFor(t, func() { Send(wantProcessed) }, func() bool {
		c.headerMutex.Lock()
		defer c.headerMutex.Unlock()
		return c.headers.Processed == wantProcessed
	})

	var buf bytes.Buffer
	c.HeadersJson(&buf)
	var got Headers
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.WaitingForHeaders != 11 || got.WriteHeaders != wantWrite ||
		got.CanonicalMarker != wantMarker || got.Processed != wantProcessed {
		t.Fatalf("HeadersJson() = %+v, missing expected updates", got)
	}
}

func TestBodiesListenersAndJSON(t *testing.T) {
	c := newTestClient(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c.setupBodiesDiagnostics(ctx)

	wantDownload := BodiesDownloadBlockUpdate{}
	waitFor(t, func() { Send(wantDownload) }, func() bool {
		c.bodiesMutex.Lock()
		defer c.bodiesMutex.Unlock()
		return c.bodies.BlockDownload == wantDownload
	})

	wantWrite := BodiesWriteBlockUpdate{}
	waitFor(t, func() { Send(wantWrite) }, func() bool {
		c.bodiesMutex.Lock()
		defer c.bodiesMutex.Unlock()
		return c.bodies.BlockWrite == wantWrite
	})

	wantProcessing := BodiesProcessingUpdate{}
	waitFor(t, func() { Send(wantProcessing) }, func() bool {
		c.bodiesMutex.Lock()
		defer c.bodiesMutex.Unlock()
		return c.bodies.Processing == wantProcessing
	})

	wantProcessed := BodiesProcessedUpdate{}
	waitFor(t, func() { Send(wantProcessed) }, func() bool {
		c.bodiesMutex.Lock()
		defer c.bodiesMutex.Unlock()
		return c.bodies.Processed == wantProcessed
	})

	var buf bytes.Buffer
	c.BodiesInfoJson(&buf)
	if buf.Len() == 0 {
		t.Fatal("expected non-empty BodiesInfoJson output")
	}
}
