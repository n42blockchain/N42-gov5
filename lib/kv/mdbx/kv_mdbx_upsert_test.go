package mdbx

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/log/v3"
)

func dumpAll(t *testing.T, tx kv.Tx, table string) map[string][]string {
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

func TestUpsertLeavesExactlyOneValue(t *testing.T) {
	const (
		plain = "Plain"
		dup   = "Dup"
		auto  = "AutoDup"
	)
	db := NewMDBX(log.New()).InMem(t.TempDir()).WithTableCfg(func(kv.TableCfg) kv.TableCfg {
		return kv.TableCfg{
			plain: {},
			dup:   {Flags: kv.DupSort},
			auto:  {Flags: kv.DupSort, AutoDupSortKeysConversion: true, DupFromLen: 6, DupToLen: 4},
		}
	}).MustOpen()
	t.Cleanup(db.Close)

	tx, err := db.BeginRw(context.Background())
	require.NoError(t, err)
	t.Cleanup(tx.Rollback)
	up, ok := tx.(kv.Upserter)
	require.True(t, ok, "MdbxTx must implement kv.Upserter")

	// Plain table: an ordinary overwrite.
	require.NoError(t, up.Upsert(plain, []byte("p1"), []byte("one")))
	require.NoError(t, up.Upsert(plain, []byte("p1"), []byte("three")))
	require.Equal(t, map[string][]string{"p1": {"three"}}, dumpAll(t, tx, plain))

	// Plain DupSort table.
	for _, v := range []string{"aa", "bb", "cc"} {
		require.NoError(t, tx.Put(dup, []byte("many"), []byte(v)))
	}
	require.NoError(t, up.Upsert(dup, []byte("many"), []byte("zz"))) // several duplicates -> one
	require.NoError(t, tx.Put(dup, []byte("one"), []byte("x1")))
	require.NoError(t, up.Upsert(dup, []byte("one"), []byte("x2")))     // single, same length: in place
	require.NoError(t, up.Upsert(dup, []byte("one"), []byte("longer"))) // single, other length
	require.NoError(t, up.Upsert(dup, []byte("one"), []byte("longer"))) // equal: no-op
	require.NoError(t, up.Upsert(dup, []byte("new"), []byte("fresh")))  // absent key
	require.NoError(t, tx.Put(dup, []byte("low"), []byte("zz")))
	require.NoError(t, up.Upsert(dup, []byte("low"), []byte("aa"))) // same length, sorts lower
	require.Equal(t, map[string][]string{
		"many": {"zz"},
		"one":  {"longer"},
		"new":  {"fresh"},
		"low":  {"aa"},
	}, dumpAll(t, tx, dup))

	// AutoDupSort table: only the duplicate with the same sub-key is replaced.
	k1 := []byte{0, 0, 0, 1, 0, 1}
	k2 := []byte{0, 0, 0, 1, 0, 2}
	k3 := []byte{0, 0, 0, 1, 0, 3}
	require.NoError(t, tx.Put(auto, k1, []byte("v1")))
	require.NoError(t, tx.Put(auto, k2, []byte("w")))
	require.NoError(t, up.Upsert(auto, k1, []byte("v22")))
	require.NoError(t, up.Upsert(auto, k1, []byte("v3")))
	require.NoError(t, up.Upsert(auto, k3, []byte("u")))
	require.Equal(t, map[string][]string{
		string(k1): {"v3"},
		string(k2): {"w"},
		string(k3): {"u"},
	}, dumpAll(t, tx, auto))
}
