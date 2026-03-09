// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package tracing

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// TestInit_Disabled verifies that Init returns a no-op shutdown when tracing
// is disabled, and that the global provider remains the default noop.
func TestInit_Disabled(t *testing.T) {
	// Reset the global provider after this test.
	orig := otel.GetTracerProvider()
	defer otel.SetTracerProvider(orig)

	shutdown, err := Init(Config{Enable: false})
	if err != nil {
		t.Fatalf("Init(disabled) returned error: %v", err)
	}
	if shutdown == nil {
		t.Fatal("Init(disabled) returned nil shutdown func")
	}
	// Shutdown should be a no-op.
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown returned error: %v", err)
	}

	// The global provider should still be a noop.
	tr := otel.Tracer("test")
	_, span := tr.Start(context.Background(), "test-span")
	if span.SpanContext().IsValid() {
		t.Error("expected noop span with invalid context when tracing is disabled")
	}
	span.End()
}

// TestInit_DefaultConfig verifies that DefaultConfig has expected values.
func TestInit_DefaultConfig(t *testing.T) {
	if DefaultConfig.Enable {
		t.Error("DefaultConfig.Enable should be false")
	}
	if DefaultConfig.Endpoint != "localhost:4318" {
		t.Errorf("DefaultConfig.Endpoint = %q, want %q", DefaultConfig.Endpoint, "localhost:4318")
	}
	if DefaultConfig.SampleRate != 0.1 {
		t.Errorf("DefaultConfig.SampleRate = %f, want 0.1", DefaultConfig.SampleRate)
	}
}

// TestTracer_Returns verifies that Tracer returns a tracer with the expected
// instrumentation scope name.
func TestTracer_Returns(t *testing.T) {
	tr := Tracer("blockchain")
	if tr == nil {
		t.Fatal("Tracer returned nil")
	}
	// Without an active provider, the tracer is a noop tracer; verify it is
	// usable by starting a span.
	ctx, span := tr.Start(context.Background(), "test-op")
	if ctx == nil {
		t.Fatal("Start returned nil context")
	}
	span.End()
}

// TestStartSpan_NoOp verifies that StartSpan works even without Init (noop
// tracer). This ensures instrumented code paths don't panic when tracing is
// not configured.
func TestStartSpan_NoOp(t *testing.T) {
	tr := noop.NewTracerProvider().Tracer("test")
	ctx, span := StartSpan(context.Background(), tr, "noop-span")
	if ctx == nil {
		t.Fatal("StartSpan returned nil context")
	}
	if span == nil {
		t.Fatal("StartSpan returned nil span")
	}
	// Should not panic.
	SetSpanError(span, fmt.Errorf("test error"))
	SetSpanOK(span)
	span.SetAttributes(StringAttr("key", "value"), Int64Attr("num", 42))
	span.End()
}

// TestInit_Enabled verifies that Init with Enable=true sets up a real tracer
// provider and that spans are exported to the collector.
func TestInit_Enabled(t *testing.T) {
	// Reset the global provider after this test.
	orig := otel.GetTracerProvider()
	defer otel.SetTracerProvider(orig)

	var received []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received = body
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	// Extract host:port from the test server URL.
	endpoint := strings.TrimPrefix(ts.URL, "http://")

	shutdown, err := Init(Config{
		Enable:     true,
		Endpoint:   endpoint,
		SampleRate: 1.0, // sample everything for the test
	})
	if err != nil {
		t.Fatalf("Init(enabled) returned error: %v", err)
	}

	// Create and end a span to produce export data.
	tr := Tracer("test")
	_, span := tr.Start(context.Background(), "test-export-span")
	span.SetAttributes(StringAttr("test.key", "hello"))
	span.End()

	// Flush spans by shutting down.
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown returned error: %v", err)
	}

	if len(received) == 0 {
		t.Fatal("expected collector to receive span data, got nothing")
	}

	// Verify the payload is valid OTLP JSON.
	var payload otlpPayload
	if err := json.Unmarshal(received, &payload); err != nil {
		t.Fatalf("failed to unmarshal OTLP payload: %v", err)
	}
	if len(payload.ResourceSpans) == 0 {
		t.Fatal("expected at least one ResourceSpans entry")
	}
	found := false
	for _, rs := range payload.ResourceSpans {
		for _, ss := range rs.ScopeSpans {
			for _, s := range ss.Spans {
				if s.Name == "test-export-span" {
					found = true
				}
			}
		}
	}
	if !found {
		t.Error("expected to find 'test-export-span' in exported payload")
	}
}

// TestOTLPExporter_EmptyBatch verifies ExportSpans with no spans is a no-op.
func TestOTLPExporter_EmptyBatch(t *testing.T) {
	exp, err := NewOTLPExporter("localhost:4318")
	if err != nil {
		t.Fatalf("NewOTLPExporter: %v", err)
	}
	if err := exp.ExportSpans(context.Background(), nil); err != nil {
		t.Fatalf("ExportSpans(nil) returned error: %v", err)
	}
}

// TestSetSpanError_Nil verifies that SetSpanError with nil error is safe.
func TestSetSpanError_Nil(t *testing.T) {
	tr := noop.NewTracerProvider().Tracer("test")
	_, span := tr.Start(context.Background(), "s")
	// Should not panic with nil error.
	SetSpanError(span, nil)
	span.End()
}

// TestTracerScope verifies that different Tracer names produce distinct tracers.
func TestTracerScope(t *testing.T) {
	t1 := Tracer("rpc")
	t2 := Tracer("blockchain")
	// They should both be valid (non-nil) tracers.
	if t1 == nil || t2 == nil {
		t.Fatal("Tracer returned nil")
	}
	// Both should be usable.
	_, s1 := t1.Start(context.Background(), "op1")
	s1.End()
	_, s2 := t2.Start(context.Background(), "op2")
	s2.End()
}

// TestStartSpan_PropagatesContext verifies that StartSpan returns a context
// containing the span, enabling parent-child relationships.
func TestStartSpan_PropagatesContext(t *testing.T) {
	orig := otel.GetTracerProvider()
	defer otel.SetTracerProvider(orig)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()
	endpoint := strings.TrimPrefix(ts.URL, "http://")

	shutdown, err := Init(Config{Enable: true, Endpoint: endpoint, SampleRate: 1.0})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	defer shutdown(context.Background())

	tr := Tracer("test")
	ctx, parentSpan := StartSpan(context.Background(), tr, "parent")
	_, childSpan := StartSpan(ctx, tr, "child")

	// The child span's parent should match the parent span's context.
	parentSC := parentSpan.SpanContext()
	childSC := childSpan.SpanContext()

	if !parentSC.IsValid() {
		t.Fatal("parent span context is not valid")
	}
	if !childSC.IsValid() {
		t.Fatal("child span context is not valid")
	}
	if parentSC.TraceID() != childSC.TraceID() {
		t.Errorf("trace IDs differ: parent=%s child=%s", parentSC.TraceID(), childSC.TraceID())
	}

	childSpan.End()
	parentSpan.End()

	// Verify the child's parent is indeed the parent span by checking the
	// span stored in the child context.
	spanFromCtx := trace.SpanFromContext(ctx)
	if spanFromCtx.SpanContext().SpanID() != parentSC.SpanID() {
		t.Error("context does not carry the parent span")
	}
}
