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
// Prometheus metrics for the ZK prover service: proofs requested,
// proofs generated, proof latency histogram and prover queue depth.
// Registered through the shared common/metrics facade so they
// surface on the node's metrics endpoint.

package zkprover

import (
	prometheus "github.com/n42blockchain/N42/common/metrics"
)

var (
	zkProofSubmitted = prometheus.GetOrCreateCounter("zkprover_proofs_submitted_total", true)
	zkProofGenerated = prometheus.GetOrCreateCounter("zkprover_proofs_generated_total", true)
	zkProofFailed    = prometheus.GetOrCreateCounter("zkprover_proofs_failed_total", true)

	// zkProvingDuration tracks end-to-end proving time (used when gRPC is connected).
	zkProvingDuration = prometheus.GetOrCreateSummary("zkprover_proving_duration_seconds")

	// zkWitnessSize and zkProofSize track the last seen sizes.
	// These use Counter with Set() to emulate gauge behavior (N42's prometheus
	// package supports Set on counters). Named with _bytes suffix per convention.
	zkWitnessSize = prometheus.GetOrCreateCounter("zkprover_witness_size_bytes", true)
	zkProofSize   = prometheus.GetOrCreateCounter("zkprover_proof_size_bytes", true)
)
