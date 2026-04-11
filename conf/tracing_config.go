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
//
// OpenTelemetry distributed tracing configuration.
// TracingConfig toggles OTLP span export to an OTel collector:
// Enable flag, Endpoint (OTLP/HTTP, e.g. localhost:4318) and
// SampleRate in [0.0, 1.0]. All instrumentation falls back to
// no-ops when tracing is disabled.

package conf

// TracingConfig holds OpenTelemetry distributed tracing configuration.
// Tracing is opt-in; when Enable is false all operations are no-ops.
type TracingConfig struct {
	// Enable activates OpenTelemetry tracing.
	Enable bool `json:"otel_tracing" yaml:"otel_tracing"`

	// Endpoint is the OTLP/HTTP collector endpoint (e.g. "localhost:4318").
	Endpoint string `json:"otel_endpoint" yaml:"otel_endpoint"`

	// SampleRate controls the fraction of traces sampled (0.0–1.0).
	SampleRate float64 `json:"otel_sample_rate" yaml:"otel_sample_rate"`
}
