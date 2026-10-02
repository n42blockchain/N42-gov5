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
// Test-only helper that opens a memdb-backed MDBX database whose table
// config includes the poaSnapshot table. memdb.NewTestDB uses the default
// kv.ChaindataTablesCfg, which (before this helper runs) does not register
// poaSnapshot, so any attempt to open that table fails with
// "mdbx_cursor_open: permission denied". Pointing kv.ChaindataTablesCfg at
// modules.N42TableCfg (which does declare the table) before opening the DB
// fixes that, matching the pattern already used by modules/rawdb tests
// (see modules/rawdb/accessors_test.go, accessors_bad_header_test.go, etc).

package apoa

import (
	"testing"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
)

// apoaTNewSnapshotDB returns a memdb-backed kv.RwDB whose table config
// includes the poaSnapshot table, so Snapshot.store/loadSnapshot work.
func apoaTNewSnapshotDB(t *testing.T) kv.RwDB {
	t.Helper()
	// N42Init populates N42TableCfg with default entries for every table in
	// n42Tables (including poaSnapshot); it is not run via a package init(),
	// so it must be called explicitly before the table config is usable.
	modules.N42Init()
	prev := kv.ChaindataTablesCfg
	kv.ChaindataTablesCfg = modules.N42TableCfg
	t.Cleanup(func() { kv.ChaindataTablesCfg = prev })
	return memdb.NewTestDB(t)
}
