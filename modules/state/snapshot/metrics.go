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

package snapshot

import (
	prometheus "github.com/n42blockchain/N42/common/metrics"
)

// Prometheus metrics for the snapshot acceleration layer.
var (
	// snapshotDiffLayers tracks the current number of diff layers in the tree.
	snapshotDiffLayers = prometheus.GetOrCreateCounter("snapshot_accel_diff_layers", true)

	// snapshotDiffMemory tracks the total approximate memory usage of diff layers.
	snapshotDiffMemory = prometheus.GetOrCreateCounter("snapshot_accel_diff_memory_bytes", true)

	// snapshotFlattenCount tracks the total number of flatten operations.
	snapshotFlattenCount = prometheus.GetOrCreateCounter("snapshot_accel_flatten_total")

	// snapshotCacheWarmedAccounts tracks accounts loaded during startup warmup.
	snapshotCacheWarmedAccounts = prometheus.GetOrCreateCounter("snapshot_accel_warmed_accounts", true)

	// snapshotLayerHits tracks how often a layer lookup succeeded (found in diff chain).
	snapshotLayerHits = prometheus.GetOrCreateCounter("snapshot_accel_layer_hits")

	// snapshotLayerMisses tracks how often a layer lookup missed (fell through to DB).
	snapshotLayerMisses = prometheus.GetOrCreateCounter("snapshot_accel_layer_misses")
)
