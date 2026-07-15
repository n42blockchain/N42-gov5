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

package rawdb

import (
	"bytes"
	"context"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
)

func TestBlockAccessListRoundTrip(t *testing.T) {
	modules.N42Init()
	prev := kv.ChaindataTablesCfg
	kv.ChaindataTablesCfg = modules.N42TableCfg
	t.Cleanup(func() { kv.ChaindataTablesCfg = prev })

	db := memdb.NewTestDB(t)
	hash := types.HexToHash("0xabc123")
	raw := []byte{0xc2, 0x01, 0x02} // arbitrary RLP-ish bytes

	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		return WriteBlockAccessList(tx, hash, raw)
	}); err != nil {
		t.Fatal(err)
	}

	if err := db.View(context.Background(), func(tx kv.Tx) error {
		got := ReadBlockAccessList(tx, hash)
		if !bytes.Equal(got, raw) {
			t.Fatalf("read %x, want %x", got, raw)
		}
		// Absent hash -> nil.
		if ReadBlockAccessList(tx, types.HexToHash("0xdead")) != nil {
			t.Fatal("absent BAL should read nil")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Empty raw is a no-op write (nothing stored).
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		return WriteBlockAccessList(tx, types.HexToHash("0xfeed"), nil)
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		if ReadBlockAccessList(tx, types.HexToHash("0xfeed")) != nil {
			t.Fatal("empty-raw write should store nothing")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
