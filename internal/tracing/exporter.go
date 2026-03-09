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
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// otlpExporter is a minimal OTLP/HTTP span exporter that sends trace data to
// an OpenTelemetry collector using plain net/http. This avoids pulling in the
// full go.opentelemetry.io/otel/exporters/otlp dependency tree.
//
// It implements sdktrace.SpanExporter.
type otlpExporter struct {
	endpoint string
	client   *http.Client
}

// NewOTLPExporter creates a new OTLP/HTTP exporter that sends spans to
// http://<endpoint>/v1/traces. The endpoint should be host:port without a
// scheme (e.g. "localhost:4318").
func NewOTLPExporter(endpoint string) (sdktrace.SpanExporter, error) {
	if endpoint == "" {
		return nil, fmt.Errorf("otlp exporter: endpoint is required")
	}
	return &otlpExporter{
		endpoint: endpoint,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}, nil
}

// ExportSpans serializes the read-only span data to OTLP JSON and POSTs it
// to the collector. Failures are returned as errors but never panic; the
// batch processor will retry or drop as configured.
func (e *otlpExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	if len(spans) == 0 {
		return nil
	}

	payload := e.buildPayload(spans)
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("otlp exporter: marshal: %w", err)
	}

	url := fmt.Sprintf("http://%s/v1/traces", e.endpoint)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("otlp exporter: new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := e.client.Do(req)
	if err != nil {
		return fmt.Errorf("otlp exporter: send: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("otlp exporter: collector returned status %d", resp.StatusCode)
	}
	return nil
}

// Shutdown releases any resources held by the exporter.
func (e *otlpExporter) Shutdown(_ context.Context) error {
	e.client.CloseIdleConnections()
	return nil
}

// --- OTLP JSON payload construction ---
//
// These types mirror the subset of the OTLP protobuf schema needed to export
// spans as JSON. Field names use the OTLP JSON mapping conventions.

type otlpPayload struct {
	ResourceSpans []otlpResourceSpans `json:"resourceSpans"`
}

type otlpResourceSpans struct {
	Resource  otlpResource    `json:"resource"`
	ScopeSpans []otlpScopeSpans `json:"scopeSpans"`
}

type otlpResource struct {
	Attributes []otlpKeyValue `json:"attributes"`
}

type otlpScopeSpans struct {
	Scope otlpScope  `json:"scope"`
	Spans []otlpSpan `json:"spans"`
}

type otlpScope struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

type otlpSpan struct {
	TraceID            string           `json:"traceId"`
	SpanID             string           `json:"spanId"`
	ParentSpanID       string           `json:"parentSpanId,omitempty"`
	Name               string           `json:"name"`
	Kind               int              `json:"kind"`
	StartTimeUnixNano  string           `json:"startTimeUnixNano"`
	EndTimeUnixNano    string           `json:"endTimeUnixNano"`
	Attributes         []otlpKeyValue   `json:"attributes,omitempty"`
	Status             *otlpStatus      `json:"status,omitempty"`
	Events             []otlpEvent      `json:"events,omitempty"`
}

type otlpKeyValue struct {
	Key   string    `json:"key"`
	Value otlpValue `json:"value"`
}

type otlpValue struct {
	StringValue *string `json:"stringValue,omitempty"`
	IntValue    *string `json:"intValue,omitempty"`
}

type otlpStatus struct {
	Code    int    `json:"code"`
	Message string `json:"message,omitempty"`
}

type otlpEvent struct {
	Name               string         `json:"name"`
	TimeUnixNano       string         `json:"timeUnixNano"`
	Attributes         []otlpKeyValue `json:"attributes,omitempty"`
}

func (e *otlpExporter) buildPayload(spans []sdktrace.ReadOnlySpan) otlpPayload {
	// Group spans by their instrumentation scope.
	type scopeKey struct{ name, version string }
	grouped := make(map[scopeKey][]otlpSpan)

	var resAttrs []otlpKeyValue

	for _, s := range spans {
		// Collect resource attributes from the first span (they are the same
		// for all spans in a batch from the same provider).
		if resAttrs == nil && s.Resource().Len() > 0 {
			for _, attr := range s.Resource().Attributes() {
				resAttrs = append(resAttrs, otlpKeyValue{
					Key:   string(attr.Key),
					Value: otlpValue{StringValue: strPtr(attr.Value.Emit())},
				})
			}
		}

		sk := scopeKey{
			name:    s.InstrumentationScope().Name,
			version: s.InstrumentationScope().Version,
		}

		traceID := s.SpanContext().TraceID()
		spanID := s.SpanContext().SpanID()
		span := otlpSpan{
			TraceID:           hex.EncodeToString(traceID[:]),
			SpanID:            hex.EncodeToString(spanID[:]),
			Name:              s.Name(),
			Kind:              int(s.SpanKind()),
			StartTimeUnixNano: fmt.Sprintf("%d", s.StartTime().UnixNano()),
			EndTimeUnixNano:   fmt.Sprintf("%d", s.EndTime().UnixNano()),
		}

		if s.Parent().IsValid() {
			parentSpanID := s.Parent().SpanID()
			span.ParentSpanID = hex.EncodeToString(parentSpanID[:])
		}

		for _, attr := range s.Attributes() {
			kv := otlpKeyValue{Key: string(attr.Key)}
			switch attr.Value.Type().String() {
			case "INT64":
				v := fmt.Sprintf("%d", attr.Value.AsInt64())
				kv.Value = otlpValue{IntValue: &v}
			default:
				kv.Value = otlpValue{StringValue: strPtr(attr.Value.Emit())}
			}
			span.Attributes = append(span.Attributes, kv)
		}

		st := s.Status()
		if st.Code != 0 {
			span.Status = &otlpStatus{
				Code:    int(st.Code),
				Message: st.Description,
			}
		}

		for _, ev := range s.Events() {
			oev := otlpEvent{
				Name:         ev.Name,
				TimeUnixNano: fmt.Sprintf("%d", ev.Time.UnixNano()),
			}
			for _, attr := range ev.Attributes {
				oev.Attributes = append(oev.Attributes, otlpKeyValue{
					Key:   string(attr.Key),
					Value: otlpValue{StringValue: strPtr(attr.Value.Emit())},
				})
			}
			span.Events = append(span.Events, oev)
		}

		grouped[sk] = append(grouped[sk], span)
	}

	var scopeSpans []otlpScopeSpans
	for sk, ss := range grouped {
		scopeSpans = append(scopeSpans, otlpScopeSpans{
			Scope: otlpScope{Name: sk.name, Version: sk.version},
			Spans: ss,
		})
	}

	return otlpPayload{
		ResourceSpans: []otlpResourceSpans{{
			Resource:   otlpResource{Attributes: resAttrs},
			ScopeSpans: scopeSpans,
		}},
	}
}

func strPtr(s string) *string { return &s }
