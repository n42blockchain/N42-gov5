package mdbx

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/lib/log/v3"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/lib/kv"
)

// TestWriteProbeCountsEachRowOnce pins the probe's attribution: a tx-level
// write delegates to a cursor, and the row must be recorded once. Before the
// fix the tx and the cursor both recorded it, so a 163k-transaction block
// showed 326k BlockTransaction rows.
func TestWriteProbeCountsEachRowOnce(t *testing.T) {
	prev := writeProbeEnabled
	writeProbeEnabled = true
	t.Cleanup(func() { writeProbeEnabled = prev })

	const plain, dup = "ProbePlain", "ProbeDup"
	db := NewMDBX(log.New()).InMem(t.TempDir()).WithTableCfg(func(kv.TableCfg) kv.TableCfg {
		return kv.TableCfg{
			plain: {},
			dup:   {Flags: kv.DupSort},
		}
	}).MustOpen()
	t.Cleanup(db.Close)

	rw, err := db.BeginRw(context.Background())
	require.NoError(t, err)
	t.Cleanup(rw.Rollback)
	tx, ok := rw.(*MdbxTx)
	require.True(t, ok, "BeginRw returned %T", rw)

	require.NoError(t, tx.Put(plain, []byte("k1"), []byte("v1")))
	require.NoError(t, tx.Upsert(plain, []byte("k2"), []byte("v22")))
	require.NoError(t, tx.Append(plain, []byte("k3"), []byte("v333")))
	require.NoError(t, tx.Delete(plain, []byte("k1")))
	require.NoError(t, tx.Append(dup, []byte("a"), []byte("x")))
	require.NoError(t, tx.AppendDup(dup, []byte("a"), []byte("y")))

	require.NotNil(t, tx.tableWrites)
	p := tx.tableWrites.rows[plain]
	require.NotNil(t, p)
	require.EqualValues(t, 3, p.puts, "plain puts")
	require.EqualValues(t, 1, p.dels, "plain deletes")
	require.EqualValues(t, 4+5+6, p.bytes, "plain payload bytes")
	d := tx.tableWrites.rows[dup]
	require.NotNil(t, d)
	require.EqualValues(t, 2, d.puts, "dupsort puts")
	require.EqualValues(t, 0, d.dels, "dupsort deletes")
}
