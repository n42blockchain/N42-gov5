//go:build linux && amd64 && cgo

package mdbx

import (
	"context"
	"testing"

	"github.com/c2h5oh/datasize"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/log/v3"
	"github.com/stretchr/testify/require"
)

const (
	putManyPlainTable = "PutManyPlain"
	putManyDupTable   = "PutManyDup"
	putManyAutoTable  = "PutManyAuto"
)

func putManyTestDB(t *testing.T, mapSize datasize.ByteSize) *MdbxKV {
	t.Helper()
	db := putManyOpenTestDB(t.TempDir(), mapSize)
	t.Cleanup(db.Close)
	return db
}

func putManyOpenTestDB(path string, mapSize datasize.ByteSize) *MdbxKV {
	if mapSize == 0 {
		mapSize = 128 * datasize.MB
	}
	db := NewMDBX(log.New()).Path(path).MapSize(mapSize).GrowthStep(1 * datasize.MB).WithTableCfg(func(kv.TableCfg) kv.TableCfg {
		return kv.TableCfg{
			putManyPlainTable: {},
			putManyDupTable:   {Flags: kv.DupSort},
			putManyAutoTable:  {Flags: kv.DupSort, AutoDupSortKeysConversion: true, DupFromLen: 8, DupToLen: 4},
		}
	}).MustOpen()
	return db.(*MdbxKV)
}

func putManyKeys(count int) []byte {
	keys := make([]byte, count*32)
	for i := 0; i < count; i++ {
		// Distinct deterministic 32-byte keys, with no shared mutable backing.
		keys[i*32+24] = byte(i >> 24)
		keys[i*32+25] = byte(i >> 16)
		keys[i*32+26] = byte(i >> 8)
		keys[i*32+27] = byte(i)
		keys[i*32+31] = 1
	}
	return keys
}

func putManyKey(keys []byte, i int) []byte { return keys[i*32 : (i+1)*32] }

func putManyStats() [3]uint64 {
	batches, attempted, written := SameValueBatchStats()
	return [3]uint64{batches, attempted, written}
}

func TestTryPutSameValueManyWritesAndOverwritesInOneTransaction(t *testing.T) {
	path := t.TempDir()
	db := putManyOpenTestDB(path, 0)
	rw, err := db.BeginRw(context.Background())
	require.NoError(t, err)
	tx := rw.(*MdbxTx)
	t.Cleanup(tx.Rollback)

	oldProbe := writeProbeEnabled
	writeProbeEnabled = true
	t.Cleanup(func() { writeProbeEnabled = oldProbe })
	beforeBatches, beforeAttempts, beforeWritten := SameValueBatchStats()

	keys := putManyKeys(5)
	first := []byte("first-value")
	handled, err := tx.TryPutSameValueMany(tx, putManyPlainTable, keys, 32, first)
	require.True(t, handled)
	require.NoError(t, err)
	require.EqualValues(t, 5, tx.writeCount.Load())
	require.EqualValues(t, 5*(32+len(first)), tx.writeBytes.Load())

	second := []byte("overwrite")
	handled, err = tx.TryPutSameValueMany(tx, putManyPlainTable, keys, 32, second)
	require.True(t, handled)
	require.NoError(t, err)
	require.EqualValues(t, 10, tx.writeCount.Load())
	require.EqualValues(t, 5*(32+len(first))+5*(32+len(second)), tx.writeBytes.Load())
	require.EqualValues(t, 10, tx.tableWrites.rows[putManyPlainTable].puts)
	require.EqualValues(t, 5*(32+len(first))+5*(32+len(second)), tx.tableWrites.rows[putManyPlainTable].bytes)

	afterBatches, afterAttempts, afterWritten := SameValueBatchStats()
	require.EqualValues(t, beforeBatches+2, afterBatches)
	require.EqualValues(t, beforeAttempts+10, afterAttempts)
	require.EqualValues(t, beforeWritten+10, afterWritten)
	require.NoError(t, tx.Commit())
	db.Close()

	reopened := putManyOpenTestDB(path, 0)
	t.Cleanup(reopened.Close)

	for i := 0; i < 5; i++ {
		var got []byte
		err := reopened.View(context.Background(), func(ro kv.Tx) error {
			var err error
			got, err = ro.GetOne(putManyPlainTable, putManyKey(keys, i))
			return err
		})
		require.NoError(t, err)
		require.Equal(t, second, got, "key %d must contain the overwritten value", i)
	}
}

func TestTryPutSameValueManyRollbackLeavesNoRows(t *testing.T) {
	db := putManyTestDB(t, 0)
	rw, err := db.BeginRw(context.Background())
	require.NoError(t, err)
	tx := rw.(*MdbxTx)
	keys := putManyKeys(4)
	handled, err := tx.TryPutSameValueMany(tx, putManyPlainTable, keys, 32, []byte("rolled-back"))
	require.True(t, handled)
	require.NoError(t, err)
	tx.Rollback()

	for i := 0; i < 4; i++ {
		var present bool
		err := db.View(context.Background(), func(ro kv.Tx) error {
			var err error
			present, err = ro.Has(putManyPlainTable, putManyKey(keys, i))
			if err != nil {
				return err
			}
			return nil
		})
		require.NoError(t, err)
		require.False(t, present, "rolled-back key %d must not exist", i)
	}
}

func TestTryPutSameValueManyDeclinesWrappersAndUnsupportedTables(t *testing.T) {
	db := putManyTestDB(t, 0)
	rw, err := db.BeginRw(context.Background())
	require.NoError(t, err)
	tx := rw.(*MdbxTx)
	t.Cleanup(tx.Rollback)
	keys := putManyKeys(1)
	before := putManyStats()

	otherDB := putManyTestDB(t, 0)
	otherRW, err := otherDB.BeginRw(context.Background())
	require.NoError(t, err)
	other := otherRW.(*MdbxTx)
	t.Cleanup(other.Rollback)
	handled, err := tx.TryPutSameValueMany(other, putManyPlainTable, keys, 32, []byte("value"))
	require.False(t, handled)
	require.NoError(t, err)

	// This value type has a slice and is therefore deliberately uncomparable.
	// Its promoted Put method must not make it eligible for the native bypass.
	wrapped := embeddedUncomparablePutter{MdbxTx: tx, marker: []byte("wrapper")}
	var owner kv.Putter = wrapped
	require.NotPanics(t, func() {
		handled, err = tx.TryPutSameValueMany(owner, putManyPlainTable, keys, 32, []byte("value"))
	})
	require.False(t, handled)
	require.NoError(t, err)

	for _, table := range []string{putManyDupTable, putManyAutoTable} {
		fallbackKey := putManyKey(keys, 0)
		batchKeys := keys
		if table == putManyAutoTable {
			fallbackKey = []byte("12345678")
			batchKeys = fallbackKey
		}
		handled, err = tx.TryPutSameValueMany(tx, table, batchKeys, 32, []byte("value"))
		require.False(t, handled, "unsupported table %s must return to the original Put path", table)
		require.NoError(t, err)
		require.NoError(t, tx.Put(table, fallbackKey, []byte("value")))
	}
	after := putManyStats()
	require.Equal(t, before, after, "declined calls must not touch native-batch accounting")
}

type embeddedUncomparablePutter struct {
	*MdbxTx
	marker []byte
}

func TestTryPutSameValueManyHandlesInvalidEmptyReadOnlyAndClosedTransactions(t *testing.T) {
	db := putManyTestDB(t, 0)
	keys := putManyKeys(1)
	before := putManyStats()

	rw, err := db.BeginRw(context.Background())
	require.NoError(t, err)
	tx := rw.(*MdbxTx)
	for _, tc := range []struct {
		name    string
		keys    []byte
		keySize int
	}{
		{name: "wrong key size", keys: keys, keySize: 31},
		{name: "partial key", keys: keys[:len(keys)-1], keySize: 32},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handled, err := tx.TryPutSameValueMany(tx, putManyPlainTable, tc.keys, tc.keySize, []byte("value"))
			require.True(t, handled)
			require.Error(t, err)
		})
	}
	handled, err := tx.TryPutSameValueMany(tx, putManyPlainTable, nil, 32, []byte("value"))
	require.True(t, handled)
	require.NoError(t, err, "empty key set is a selected no-op")
	require.NoError(t, tx.Commit())

	closed := tx
	handled, err = closed.TryPutSameValueMany(closed, putManyPlainTable, keys, 32, []byte("value"))
	require.True(t, handled)
	require.Error(t, err, "closed transaction is selected and must not be retried")

	roAny, err := db.BeginRo(context.Background())
	require.NoError(t, err)
	ro := roAny.(*MdbxTx)
	t.Cleanup(ro.Rollback)
	handled, err = ro.TryPutSameValueMany(ro, putManyPlainTable, keys, 32, []byte("value"))
	require.True(t, handled)
	require.Error(t, err, "read-only transaction is selected and must not be retried")

	var nilTx *MdbxTx
	var nilOwner kv.Putter = nilTx
	handled, err = nilTx.TryPutSameValueMany(nilOwner, putManyPlainTable, keys, 32, []byte("value"))
	require.True(t, handled)
	require.Error(t, err, "nil transaction is selected and must not panic or fall through")

	after := putManyStats()
	require.Equal(t, before, after, "pre-native validation must not change batch or row counters")
}

func TestTryPutSameValueManyNativeMapFullReportsAttemptedVersusWrittenAndRollsBack(t *testing.T) {
	// A hard maximum with small growth increments makes this a bounded MDBX
	// failure fixture. The whole transaction is rolled back below, so no
	// partially written prefix escapes.
	db := putManyTestDB(t, 4*datasize.MB)
	rw, err := db.BeginRw(context.Background())
	require.NoError(t, err)
	tx := rw.(*MdbxTx)
	keys := putManyKeys(2048)
	value := make([]byte, 8*1024)
	for i := range value {
		value[i] = byte(i)
	}
	beforeBatches, beforeAttempts, beforeWritten := SameValueBatchStats()
	oldProbe := writeProbeEnabled
	writeProbeEnabled = true
	t.Cleanup(func() { writeProbeEnabled = oldProbe })

	handled, err := tx.TryPutSameValueMany(tx, putManyPlainTable, keys, 32, value)
	require.True(t, handled, "native MapFull must not ask the caller to replay through Put")
	require.Error(t, err)
	var opErr interface{ Error() string }
	require.ErrorAs(t, err, &opErr)
	require.NotEmpty(t, opErr.Error())

	afterBatches, afterAttempts, afterWritten := SameValueBatchStats()
	require.EqualValues(t, beforeBatches+1, afterBatches)
	require.Greater(t, afterAttempts-beforeAttempts, uint64(0))
	require.Less(t, afterAttempts-beforeAttempts, uint64(len(keys)/32), "first native error stops the cursor loop")
	require.Equal(t, afterAttempts-beforeAttempts-1, afterWritten-beforeWritten, "attempt count includes the failing row; written counts only successful rows")
	require.EqualValues(t, afterWritten-beforeWritten, tx.writeCount.Load())
	require.EqualValues(t, (afterWritten-beforeWritten)*uint64(32+len(value)), tx.writeBytes.Load())
	require.NotNil(t, tx.tableWrites)
	require.EqualValues(t, afterAttempts-beforeAttempts, tx.tableWrites.rows[putManyPlainTable].puts)
	tx.Rollback()

	var stored int
	err = db.View(context.Background(), func(ro kv.Tx) error {
		for i := 0; i < len(keys)/32; i++ {
			present, err := ro.Has(putManyPlainTable, putManyKey(keys, i))
			if err != nil {
				return err
			}
			if present {
				stored++
			}
		}
		return nil
	})
	require.NoError(t, err)
	require.Zero(t, stored, "rollback must undo the prefix written before MDBX_MAP_FULL")
}
