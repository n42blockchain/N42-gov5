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
// config includes the poaSnapshot table (shared by apos and apoa). See
// apoa's identical helper for the full explanation: memdb.NewTestDB opens
// against kv.ChaindataTablesCfg, which only gains poaSnapshot once
// modules.N42Init() has populated modules.N42TableCfg.

package apos

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/params"
)

func aposTNewSnapshotDB(t *testing.T) kv.RwDB {
	t.Helper()
	modules.N42Init()
	prev := kv.ChaindataTablesCfg
	kv.ChaindataTablesCfg = modules.N42TableCfg
	t.Cleanup(func() { kv.ChaindataTablesCfg = prev })
	return memdb.NewTestDB(t)
}

func TestAposSnapshotStoreAndLoadRoundTrip(t *testing.T) {
	signer := types.HexToAddress("0x01")
	cfg := &params.APosConfig{Period: 1, Epoch: 30000}
	snap := newSnapshot(cfg, newAposSigCache(t), 3, types.HexToHash("0xabc"), []types.Address{signer})

	db := aposTNewSnapshotDB(t)
	tx := memdb.BeginRw(t, db)
	if err := snap.store(tx); err != nil {
		t.Fatalf("store: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	roTx := memdb.BeginRw(t, db)
	loaded, err := loadSnapshot(cfg, newAposSigCache(t), roTx, types.HexToHash("0xabc"))
	if err != nil {
		t.Fatalf("loadSnapshot: %v", err)
	}
	if loaded.Number != 3 || len(loaded.Signers) != 1 {
		t.Fatalf("unexpected loaded snapshot: %+v", loaded)
	}
}
