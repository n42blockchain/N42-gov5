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
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv/memdb"
)

func TestTdStorage(t *testing.T) {
	_, tx := memdb.NewTestTx(t)

	hash, td := types.Hash{}, uint256.NewInt(314)

	// Read non-existent TD
	entry, err := ReadTd(tx, hash, 0)
	if err != nil {
		t.Fatalf("ReadTd failed: %v", err)
	}
	if entry != nil {
		t.Fatalf("Non existent TD returned: %v", entry)
	}

	// Write and verify
	if err := WriteTd(tx, hash, 0, td); err != nil {
		t.Fatalf("WriteTd failed: %v", err)
	}
	entry, err = ReadTd(tx, hash, 0)
	if err != nil {
		t.Fatalf("ReadTd failed: %v", err)
	}
	if entry == nil {
		t.Fatal("Stored TD not found")
	}
	if entry.Cmp(td) != 0 {
		t.Fatalf("Retrieved TD mismatch: have %v, want %v", entry, td)
	}

	// Delete and verify
	if err := TruncateTd(tx, 0); err != nil {
		t.Fatalf("TruncateTd failed: %v", err)
	}
	entry, err = ReadTd(tx, hash, 0)
	if err != nil {
		t.Fatalf("ReadTd failed: %v", err)
	}
	if entry != nil {
		t.Fatalf("Deleted TD returned: %v", entry)
	}

	// Write TD at block 100, verify block 101 returns nil
	zeroTd := uint256.NewInt(0)
	if err := WriteTd(tx, hash, 100, zeroTd); err != nil {
		t.Fatalf("WriteTd failed: %v", err)
	}

	entry, err = ReadTd(tx, hash, 101)
	if err != nil {
		t.Fatalf("ReadTd at block 101 failed: %v", err)
	}
	if entry != nil {
		t.Fatalf("ReadTd at non-existent block 101 returned: %v", entry)
	}

	entry, err = ReadTd(tx, hash, 100)
	if err != nil {
		t.Fatalf("ReadTd at block 100 failed: %v", err)
	}
	if entry.Cmp(zeroTd) != 0 {
		t.Fatalf("TD at block 100 mismatch: have %v, want %v", entry, zeroTd)
	}
}
