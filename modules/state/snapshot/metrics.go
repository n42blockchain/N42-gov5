// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package snapshot

import (
	prometheus "github.com/n42blockchain/N42/common/metrics"
)

// Prometheus metrics for the snapshot acceleration layer.
var (
	snapshotDiffLayers          = prometheus.GetOrCreateCounter("snapshot_accel_diff_layers", true)
	snapshotDiffMemory          = prometheus.GetOrCreateCounter("snapshot_accel_diff_memory_bytes", true)
	snapshotFlattenCount        = prometheus.GetOrCreateCounter("snapshot_accel_flatten_total")
	snapshotCacheWarmedAccounts = prometheus.GetOrCreateCounter("snapshot_accel_warmed_accounts", true)
	snapshotLayerHits           = prometheus.GetOrCreateCounter("snapshot_accel_layer_hits")
	snapshotLayerMisses         = prometheus.GetOrCreateCounter("snapshot_accel_layer_misses")
	snapshotDiskHits            = prometheus.GetOrCreateCounter("snapshot_accel_disk_hits")
	snapshotPersistFlushCount   = prometheus.GetOrCreateCounter("snapshot_accel_persist_flush_total")
	snapshotPersistErrors       = prometheus.GetOrCreateCounter("snapshot_accel_persist_errors_total")
	snapshotGenDuration         = prometheus.GetOrCreateCounter("snapshot_accel_gen_duration_ms", true)
)
