package commitment

import (
	"context"
	"math/rand"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	"github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
)

// deletePutTx hides kv.Upserter, so FlushTo takes the delete-before-put path.
type deletePutTx struct{ kv.RwTx }

// openStateTables opens an in-memory DB with the four state tables configured
// as in the N42 chaindata schema (HashedStorage AutoDupSort, TrieOfStorage
// plain DupSort).
func openStateTables(t *testing.T) kv.RwDB {
	t.Helper()
	db := mdbx.NewMDBX(log.New()).InMem(t.TempDir()).WithTableCfg(func(kv.TableCfg) kv.TableCfg {
		return kv.TableCfg{
			modules.HashedAccounts: {},
			modules.TrieOfAccounts: {},
			modules.HashedStorage:  modules.N42TableCfg[modules.HashedStorage],
			modules.TrieOfStorage:  modules.N42TableCfg[modules.TrieOfStorage],
		}
	}).MustOpen()
	t.Cleanup(db.Close)
	return db
}

func dumpStateTable(t *testing.T, tx kv.Tx, table string) map[string][]string {
	t.Helper()
	c, err := tx.Cursor(table)
	require.NoError(t, err)
	defer c.Close()
	m := map[string][]string{}
	for k, v, err := c.First(); ; k, v, err = c.Next() {
		require.NoError(t, err)
		if k == nil {
			break
		}
		m[string(k)] = append(m[string(k)], string(v))
	}
	return m
}

// TestFlushToUpsertMatchesDeleteBeforePut applies the same random overlay
// writes through the Upsert path and through delete-before-put, and requires
// both DBs to hold exactly the model's single current value per key.
func TestFlushToUpsertMatchesDeleteBeforePut(t *testing.T) {
	ctx := context.Background()
	dbUp, dbDel := openStateTables(t), openStateTables(t)
	tables := []string{modules.HashedAccounts, modules.TrieOfAccounts, modules.HashedStorage, modules.TrieOfStorage}
	rng := rand.New(rand.NewSource(42))

	addr := func() []byte {
		b := make([]byte, 32)
		b[0], b[31] = byte(rng.Intn(6)), 0xaa
		return b
	}
	path := func() []byte { return []byte{byte(rng.Intn(4)), byte(rng.Intn(4))}[:1+rng.Intn(2)] }
	keyFor := func(table string) []byte {
		switch table {
		case modules.HashedAccounts:
			return addr()
		case modules.HashedStorage:
			slot := make([]byte, 32)
			slot[0], slot[31] = byte(rng.Intn(24)), 0x55
			return append(addr(), slot...)
		case modules.TrieOfAccounts:
			return path()
		default:
			return append(addr(), path()...)
		}
	}
	value := func() []byte {
		n := 8 // same-length replacements exercise the in-place write
		if rng.Intn(2) == 0 {
			n = 1 + rng.Intn(40)
		}
		b := make([]byte, n)
		rng.Read(b)
		return b
	}

	// Stale duplicates under TrieOfStorage keys: the first flush must leave
	// exactly one value behind on both paths.
	var seeds [][]byte
	for i := 0; i < 16; i++ {
		seeds = append(seeds, keyFor(modules.TrieOfStorage))
	}
	for _, db := range []kv.RwDB{dbUp, dbDel} {
		tx, err := db.BeginRw(ctx)
		require.NoError(t, err)
		for _, k := range seeds {
			require.NoError(t, tx.Put(modules.TrieOfStorage, k, []byte("stale-a")))
			require.NoError(t, tx.Put(modules.TrieOfStorage, k, []byte("stale-b")))
		}
		require.NoError(t, tx.Commit())
	}

	model := map[string]map[string]string{}
	for _, tb := range tables {
		model[tb] = map[string]string{}
	}
	for round := 0; round < 6; round++ {
		ovUp, ovDel := NewStateOverlay(), NewStateOverlay()
		put := func(tb string, k, v []byte) {
			ovUp.Put(tb, k, v)
			ovDel.Put(tb, k, v)
			model[tb][string(k)] = string(v)
		}
		if round == 0 {
			for _, k := range seeds {
				put(modules.TrieOfStorage, k, value())
			}
		}
		for i := 0; i < 3000; i++ {
			tb := tables[rng.Intn(len(tables))]
			k := keyFor(tb)
			if rng.Intn(4) == 0 {
				ovUp.Delete(tb, k)
				ovDel.Delete(tb, k)
				delete(model[tb], string(k))
				continue
			}
			put(tb, k, value())
		}

		for _, side := range []struct {
			db     kv.RwDB
			ov     *StateOverlay
			hidden bool
		}{{dbUp, ovUp, false}, {dbDel, ovDel, true}} {
			tx, err := side.db.BeginRw(ctx)
			require.NoError(t, err)
			var w kv.RwTx = tx
			if side.hidden {
				w = deletePutTx{tx}
				_, ok := w.(kv.Upserter)
				require.False(t, ok)
			} else {
				_, ok := w.(kv.Upserter)
				require.True(t, ok)
			}
			require.NoError(t, side.ov.FlushTo(w))
			require.NoError(t, tx.Commit())
		}

		txU, err := dbUp.BeginRo(ctx)
		require.NoError(t, err)
		txD, err := dbDel.BeginRo(ctx)
		require.NoError(t, err)
		for _, tb := range tables {
			want := map[string][]string{}
			for k, v := range model[tb] {
				want[k] = []string{v}
			}
			require.Equal(t, want, dumpStateTable(t, txU, tb), "upsert path, round %d, table %s", round, tb)
			require.Equal(t, want, dumpStateTable(t, txD, tb), "delete-before-put path, round %d, table %s", round, tb)
		}
		txU.Rollback()
		txD.Rollback()
	}
}
