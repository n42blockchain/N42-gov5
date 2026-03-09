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

// Package tracing provides OpenTelemetry distributed tracing integration for
// the N42 blockchain node. It wraps the OpenTelemetry API and SDK to provide
// a simple, opt-in tracing facility that exports spans via OTLP/HTTP.
//
// When tracing is disabled (the default), all operations are no-ops with
// negligible overhead — the global OTel tracer provider returns noop spans.
package tracing

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdkresource "go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.37.0"
	"go.opentelemetry.io/otel/trace"
)

// Config holds tracing configuration. All fields are opt-in; the zero value
// means tracing is disabled.
type Config struct {
	// Enable activates OpenTelemetry tracing. When false (default), all
	// tracing operations are no-ops.
	Enable bool `json:"otel_tracing" yaml:"otel_tracing"`

	// Endpoint is the OTLP/HTTP collector endpoint (e.g. "localhost:4318").
	// The exporter sends spans to http://<Endpoint>/v1/traces.
	Endpoint string `json:"otel_endpoint" yaml:"otel_endpoint"`

	// SampleRate controls the fraction of traces that are sampled (0.0–1.0).
	// A value of 0.1 means 10% of traces are recorded.
	SampleRate float64 `json:"otel_sample_rate" yaml:"otel_sample_rate"`
}

// DefaultConfig returns a Config with sensible defaults: tracing disabled,
// collector at localhost:4318, 10% sampling.
var DefaultConfig = Config{
	Enable:     false,
	Endpoint:   "localhost:4318",
	SampleRate: 0.1,
}

// Init initializes the global OpenTelemetry tracer provider. If cfg.Enable is
// false it returns a no-op shutdown function immediately. On success the
// returned function should be called during node shutdown to flush pending
// spans.
func Init(cfg Config) (shutdown func(context.Context) error, err error) {
	noop := func(context.Context) error { return nil }

	if !cfg.Enable {
		return noop, nil
	}

	if cfg.Endpoint == "" {
		cfg.Endpoint = DefaultConfig.Endpoint
	}
	if cfg.SampleRate <= 0 || cfg.SampleRate > 1 {
		cfg.SampleRate = DefaultConfig.SampleRate
	}

	// Build resource describing this service.
	res, err := sdkresource.Merge(
		sdkresource.Default(),
		sdkresource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName("n42-node"),
			semconv.ServiceVersion("1.0.0"),
		),
	)
	if err != nil {
		return noop, fmt.Errorf("tracing: build resource: %w", err)
	}

	// Create the OTLP/HTTP span exporter (lightweight, uses net/http).
	exporter, err := NewOTLPExporter(cfg.Endpoint)
	if err != nil {
		return noop, fmt.Errorf("tracing: create exporter: %w", err)
	}

	// Build the tracer provider with a batch span processor for production use.
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(
			sdktrace.TraceIDRatioBased(cfg.SampleRate),
		)),
	)

	// Install as the global provider so Tracer() calls work everywhere.
	otel.SetTracerProvider(tp)

	return tp.Shutdown, nil
}

// Tracer returns a named tracer for a component. The name typically matches
// the package or subsystem (e.g. "rpc", "blockchain", "txpool", "p2p").
// When tracing is not initialised, the global noop tracer is returned.
func Tracer(name string) trace.Tracer {
	return otel.Tracer("github.com/n42blockchain/N42/" + name)
}

// StartSpan is a convenience wrapper that starts a new span on the given
// tracer, returning the child context and span. Callers must defer span.End().
func StartSpan(ctx context.Context, tracer trace.Tracer, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	return tracer.Start(ctx, name, opts...)
}

// SetSpanError marks a span as errored and records the error message.
func SetSpanError(span trace.Span, err error) {
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		span.RecordError(err)
	}
}

// SetSpanOK marks a span as successful.
func SetSpanOK(span trace.Span) {
	span.SetStatus(codes.Ok, "")
}

// StringAttr is a shorthand for creating a string attribute key-value pair.
func StringAttr(key, value string) attribute.KeyValue {
	return attribute.String(key, value)
}

// Int64Attr is a shorthand for creating an int64 attribute key-value pair.
func Int64Attr(key string, value int64) attribute.KeyValue {
	return attribute.Int64(key, value)
}
