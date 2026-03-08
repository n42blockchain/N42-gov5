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

package snapsync

import (
	prometheus "github.com/n42blockchain/N42/common/metrics"
)

var (
	snapAccountsDownloaded = prometheus.GetOrCreateCounter("snap_sync_accounts_downloaded", true)
	snapStorageDownloaded  = prometheus.GetOrCreateCounter("snap_sync_storage_downloaded", true)
	snapCodesDownloaded    = prometheus.GetOrCreateCounter("snap_sync_codes_downloaded", true)
	snapBytesReceived      = prometheus.GetOrCreateCounter("snap_sync_bytes_received", true)
	snapTaskErrors         = prometheus.GetOrCreateCounter("snap_sync_task_errors_total", true)
	snapTaskTimeouts       = prometheus.GetOrCreateCounter("snap_sync_task_timeouts_total", true)
	snapCodesMissing       = prometheus.GetOrCreateCounter("snap_sync_codes_missing_total", true)
	snapInvalidAccounts    = prometheus.GetOrCreateCounter("snap_sync_invalid_accounts_total", true)
)
