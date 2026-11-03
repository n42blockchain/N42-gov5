// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"bytes"
	"context"
	"encoding/binary"
	"testing"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
)

// chgSetTestRows is a block's changeset rows shaped as the encoder emits
// them: accounts under the block key, storage under block+address, each
// in ascending (key, value) order.
func chgSetTestRows(block uint64, n int) (acct, stor []ChangeSetRow) {
	for i := 0; i < n; i++ {
		k := binary.BigEndian.AppendUint64(nil, block)
		v := binary.BigEndian.AppendUint32(make([]byte, 0, 48), uint32(i)*3)
		v = append(v, bytes.Repeat([]byte{byte(i)}, 1+i%40)...)
		acct = append(acct, ChangeSetRow{K: k, V: v})
	}
	for a := 0; a < n/8; a++ {
		k := binary.BigEndian.AppendUint64(nil, block)
		k = binary.BigEndian.AppendUint32(append(k, make([]byte, 16)...), uint32(a)*5)
		for s := 0; s < 1+a%4; s++ {
			v := binary.BigEndian.AppendUint32(make([]byte, 28), uint32(s))
			v = append(v, byte(a), byte(s))
			stor = append(stor, ChangeSetRow{K: k, V: v})
		}
	}
	return acct, stor
}

func dumpChgSetTables(t *testing.T, db kv.RwDB) [][2][]byte {
	var out [][2][]byte
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		for _, table := range []string{modules.AccountChangeSet, modules.StorageChangeSet} {
			out = append(out, [2][]byte{[]byte(table)})
			if err := tx.ForEach(table, nil, func(k, v []byte) error {
				out = append(out, [2][]byte{bytes.Clone(k), bytes.Clone(v)})
				return nil
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

// Appending with N42_CHGSET_APPEND leaves exactly the tables AppendDup
// does, including the fallbacks: a block below the table's last key (an
// out-of-order rewrite) and unsorted rows.
func TestAppendChangeSetRowsAppendFlag(t *testing.T) {
	run := func(on bool) [][2][]byte {
		defer SetChgSetAppendForTest(on)()
		db := memdb.NewTestDB(t)
		for _, blk := range []uint64{5, 6, 9, 7} {
			acct, stor := chgSetTestRows(blk, 3000)
			if err := db.Update(context.Background(), func(tx kv.RwTx) error {
				return AppendChangeSetRows(tx, acct, stor)
			}); err != nil {
				t.Fatalf("block %d: %v", blk, err)
			}
		}
		// Unsorted rows (a key below its predecessor): the flag falls back
		// to AppendDup, which inserts them.
		k := binary.BigEndian.AppendUint64(nil, 20)
		rows := []ChangeSetRow{{K: k, V: []byte{1}}, {K: k, V: []byte{3}}, {K: append(bytes.Clone(k), 0), V: []byte{2}}, {K: k, V: []byte{4}}}
		if err := db.Update(context.Background(), func(tx kv.RwTx) error {
			return AppendChangeSetRows(tx, rows, nil)
		}); err != nil {
			t.Fatal(err)
		}
		return dumpChgSetTables(t, db)
	}
	off, on := run(false), run(true)
	if len(off) < 4*3000 {
		t.Fatalf("only %d rows written", len(off))
	}
	if len(off) != len(on) {
		t.Fatalf("row counts differ: %d vs %d", len(off), len(on))
	}
	for i := range off {
		if !bytes.Equal(off[i][0], on[i][0]) || !bytes.Equal(off[i][1], on[i][1]) {
			t.Fatalf("row %d differs: %x=%x vs %x=%x", i, off[i][0], off[i][1], on[i][0], on[i][1])
		}
	}
}

func TestChgSetAppendable(t *testing.T) {
	db := memdb.NewTestDB(t)
	acct, _ := chgSetTestRows(10, 10)
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		c, err := tx.RwCursorDupSort(modules.AccountChangeSet)
		if err != nil {
			return err
		}
		defer c.Close()
		if ok, err := chgSetAppendable(c, acct); err != nil || !ok {
			t.Fatalf("empty table: ok %v err %v", ok, err)
		}
		if err := AppendChangeSetRows(tx, acct, nil); err != nil {
			return err
		}
		for _, tc := range []struct {
			name string
			blk  uint64
			swap bool
			want bool
		}{{"above", 11, false, true}, {"same", 10, false, false}, {"below", 9, false, false}, {"unsorted", 11, true, false}} {
			rows, _ := chgSetTestRows(tc.blk, 10)
			if tc.swap {
				rows[3], rows[4] = rows[4], rows[3]
			}
			if ok, err := chgSetAppendable(c, rows); err != nil || ok != tc.want {
				t.Fatalf("%s: ok %v err %v, want %v", tc.name, ok, err, tc.want)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
