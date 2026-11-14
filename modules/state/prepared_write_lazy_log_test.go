// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"testing"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/params"
)

// lazyLogTables digests every table of tx: name -> hash of its rows.
func lazyLogTables(t *testing.T, tx kv.Tx) map[string]string {
	t.Helper()
	names := make([]string, 0, len(modules.N42TableCfg))
	for name := range modules.N42TableCfg {
		names = append(names, name)
	}
	sort.Strings(names)
	out := map[string]string{}
	for _, name := range names {
		c, err := tx.Cursor(name)
		if err != nil {
			continue
		}
		h := sha256.New()
		n := 0
		for k, v, err := c.First(); k != nil; k, v, err = c.Next() {
			if err != nil {
				t.Fatal(err)
			}
			fmt.Fprintf(h, "%d:%x=%d:%x;", len(k), k, len(v), v)
			n++
		}
		c.Close()
		if n > 0 {
			out[name] = fmt.Sprintf("%s/%d", hex.EncodeToString(h.Sum(nil)[:8]), n)
		}
	}
	return out
}

// N42_STATE_WRITE_PREP_LAZY_LOG: a sharded preparation whose calls include
// CreateContract (the block's EIP-161 removals) keeps only the created
// addresses, and the write -- the prepared rows when the store holds no
// Storage row of theirs, the calls replayed when it does -- leaves the same
// tables as the merged log's.
func TestPrepareStateWriteLazyLogSameRows(t *testing.T) {
	defer SetHistoryIndexDeferredForTest(true)()
	defer SetFoldNoJournalForTest(true)()
	defer SetPrepDeferLoadsForTest(true)()
	defer SetPrepShardsForTest(8)()
	rules := params.MainnetChainConfig.Rules(1)
	for _, storedSlot := range []bool{false, true} {
		t.Run(fmt.Sprintf("storedSlotOfCreated=%v", storedSlot), func(t *testing.T) {
			write := func(lazy bool) (map[string]string, *PreparedStateWrite) {
				defer SetPrepLazyLogForTest(lazy)()
				db := memdb.NewTestDB(t)
				if err := db.Update(context.Background(), func(tx kv.RwTx) error {
					w := NewPlainStateWriterNoHistory(tx)
					for i := 0; i < 4000; i += 2 {
						a := account.StateAccount{Nonce: uint64(i % 7), Initialised: true}
						a.Balance.SetUint64(uint64(5 + i))
						a.CodeHash = emptyCodeHashH
						if err := w.UpdateAccountData(benchAddr(2, i), &a, &a); err != nil {
							return err
						}
					}
					if storedSlot {
						// A Storage row of an address the block removes
						// (CreateContract): the fast path is refused.
						addr := benchAddr(4, 7)
						key := append(addr.Bytes(), make([]byte, 32)...)
						if err := tx.Put(modules.Storage, key, []byte{1}); err != nil {
							return err
						}
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				var (
					p      *PreparedStateWrite
					tables map[string]string
				)
				if err := db.Update(context.Background(), func(tx kv.RwTx) error {
					var err error
					ibs := prepShardsBlock(tx, rules, true)
					if p, err = PrepareStateWrite(ibs, rules, 1000); err != nil {
						return err
					}
					if !p.Fast() {
						return fmt.Errorf("not a fast preparation")
					}
					if lazy {
						if p.log != nil || len(p.shardLogs) == 0 || len(p.creates) == 0 {
							return fmt.Errorf("lazy: log %v, %d shard logs, %d creates", p.log != nil, len(p.shardLogs), len(p.creates))
						}
					} else if p.log == nil || len(p.log.creates) == 0 {
						return fmt.Errorf("merged log missing or without creates")
					}
					w := NewPlainStateWriter(tx, tx, 1000)
					if err := p.WriteState(tx, w); err != nil {
						return err
					}
					if p.Fast() == storedSlot {
						return fmt.Errorf("fast after the check %v with a stored slot %v", p.Fast(), storedSlot)
					}
					if err := p.WriteChangeSets(tx, w); err != nil {
						return err
					}
					if err := p.WriteHistory(w); err != nil {
						return err
					}
					tables = lazyLogTables(t, tx)
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				return tables, p
			}
			want, _ := write(false)
			got, p := write(true)
			if len(want) == 0 {
				t.Fatal("no tables written")
			}
			for name, w := range want {
				if got[name] != w {
					t.Fatalf("table %s: %s, lazy %s", name, w, got[name])
				}
			}
			for name, g := range got {
				if want[name] != g {
					t.Fatalf("table %s: %s, lazy %s", name, want[name], g)
				}
			}
			if storedSlot && p.log == nil {
				t.Fatal("the refused fast path did not build the merged log")
			}
		})
	}
}
