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

package metrics

import (
	prometheus "github.com/n42blockchain/N42/common/metrics"
)

// Chain synchronization metrics.
var (
	SyncCurrentBlock = prometheus.GetOrCreateCounter("sync_current_block", true)
	SyncHighestBlock = prometheus.GetOrCreateCounter("sync_highest_block", true)
	SyncIsSyncing    = prometheus.GetOrCreateCounter("sync_is_syncing", true)
)

// Database metrics.
var (
	DBReadCount  = prometheus.GetOrCreateCounter("db_read_total")
	DBWriteCount = prometheus.GetOrCreateCounter("db_write_total")
	DBReadBytes  = prometheus.GetOrCreateCounter("db_read_bytes_total")
	DBWriteBytes = prometheus.GetOrCreateCounter("db_write_bytes_total")
)

// Freezer metrics.
var (
	FreezerFrozenBlocks = prometheus.GetOrCreateCounter("freezer_frozen_blocks", true)
)
