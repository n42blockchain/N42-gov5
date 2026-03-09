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

// NOTE: DB read/write metrics are defined in lib/kv/kv_interface.go (DbReadCount etc.)
// and updated in lib/kv/mdbx/kv_mdbx_tx.go CollectMetrics().
// Freezer metrics are defined in modules/rawdb/freezer/freezer.go.
