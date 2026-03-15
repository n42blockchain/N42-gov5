package state

import (
	"encoding/binary"
	"errors"
	"testing"
	"unsafe"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/iter"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/lib/kv/order"
)

func TestStateAsOfIterDBDefersAdvanceError(t *testing.T) {
	roTx := failingTx{err: errors.New("cursor failure")}

	it := &StateAsOfIterDB{
		roTx:      roTx,
		valsTable: "missing_table",
		nextKey:   []byte("key"),
		nextVal:   []byte("value"),
		limit:     1,
	}

	k, v, err := it.Next()
	require.NoError(t, err)
	require.Equal(t, []byte("key"), k)
	require.Equal(t, []byte("value"), v)
	require.True(t, it.HasNext())

	_, _, err = it.Next()
	require.Error(t, err)
}

func TestHistoryChangesIterDBDefersAdvanceError(t *testing.T) {
	roTx := failingTx{err: errors.New("cursor failure")}

	it := &HistoryChangesIterDB{
		roTx:      roTx,
		valsTable: "missing_table",
		nextKey:   []byte("key"),
		nextVal:   []byte("value"),
		limit:     1,
	}

	k, v, err := it.Next()
	require.NoError(t, err)
	require.Equal(t, []byte("key"), k)
	require.Equal(t, []byte("value"), v)
	require.True(t, it.HasNext())

	_, _, err = it.Next()
	require.Error(t, err)
}

func TestStateAsOfIterFReturnsStoredError(t *testing.T) {
	wantErr := errors.New("boom")
	it := &StateAsOfIterF{
		err:     wantErr,
		limit:   1,
		nextKey: []byte("key"),
	}

	require.True(t, it.HasNext())
	_, _, err := it.Next()
	require.ErrorIs(t, err, wantErr)
}

func TestStateAsOfIterDBSkipsKeysWithoutAsOfValue(t *testing.T) {
	_, rwTx := memdb.NewTestTx(t)

	encode := func(txNum uint64, value string) []byte {
		buf := make([]byte, 8+len(value))
		binary.BigEndian.PutUint64(buf[:8], txNum)
		copy(buf[8:], value)
		return buf
	}

	require.NoError(t, rwTx.Put(kv.AccountChangeSet, []byte("key1"), encode(1, "old")))
	require.NoError(t, rwTx.Put(kv.AccountChangeSet, []byte("key2"), encode(12, "new")))

	it := &StateAsOfIterDB{
		roTx:      rwTx,
		valsTable: kv.AccountChangeSet,
		from:      []byte("key1"),
		limit:     10,
	}
	binary.BigEndian.PutUint64(it.startTxKey[:], 10)

	require.NoError(t, it.advance())
	require.True(t, it.HasNext())

	k, v, err := it.Next()
	require.NoError(t, err)
	require.Equal(t, []byte("key2"), k)
	require.Equal(t, []byte("new"), v)
}

func TestStateAsOfIterDBRejectsShortValue(t *testing.T) {
	_, rwTx := memdb.NewTestTx(t)
	require.NoError(t, rwTx.Put(kv.AccountChangeSet, []byte("key1"), []byte{0x01}))

	it := &StateAsOfIterDB{
		roTx:      rwTx,
		valsTable: kv.AccountChangeSet,
		from:      []byte("key1"),
		limit:     1,
	}

	err := it.advance()
	require.Error(t, err)
	require.Contains(t, err.Error(), "txnum prefix")
}

func TestStateAsOfIterDBRejectsShortLargeValueKey(t *testing.T) {
	_, rwTx := memdb.NewTestTx(t)
	require.NoError(t, rwTx.Put(kv.PlainState, []byte("short"), []byte("value")))

	it := &StateAsOfIterDB{
		largeValues: true,
		roTx:        rwTx,
		valsTable:   kv.PlainState,
		limit:       1,
	}

	err := it.advance()
	require.Error(t, err)
	require.Contains(t, err.Error(), "txnum suffix")
}

func TestHistoryChangesIterFilesReturnsStoredError(t *testing.T) {
	wantErr := errors.New("boom")
	it := &HistoryChangesIterFiles{
		err:     wantErr,
		limit:   1,
		nextKey: []byte("key"),
	}

	require.True(t, it.HasNext())
	_, _, err := it.Next()
	require.ErrorIs(t, err, wantErr)
}

func TestHistoryChangesIterDBRejectsShortValue(t *testing.T) {
	_, rwTx := memdb.NewTestTx(t)
	require.NoError(t, rwTx.Put(kv.AccountChangeSet, []byte("key1"), []byte{0x01}))

	it := &HistoryChangesIterDB{
		roTx:      rwTx,
		valsTable: kv.AccountChangeSet,
		limit:     1,
	}

	err := it.advance()
	require.Error(t, err)
	require.Contains(t, err.Error(), "txnum prefix")
}

func TestHistoryChangesIterDBRejectsShortLargeValueKey(t *testing.T) {
	_, rwTx := memdb.NewTestTx(t)
	require.NoError(t, rwTx.Put(kv.PlainState, []byte("short"), []byte("value")))

	it := &HistoryChangesIterDB{
		largeValues: true,
		roTx:        rwTx,
		valsTable:   kv.PlainState,
		limit:       1,
	}

	err := it.advance()
	require.Error(t, err)
	require.Contains(t, err.Error(), "txnum suffix")
}

type failingTx struct {
	err error
}

func (f failingTx) Has(table string, key []byte) (bool, error) { return false, nil }
func (f failingTx) GetOne(table string, key []byte) ([]byte, error) {
	return nil, nil
}
func (f failingTx) ForEach(table string, fromPrefix []byte, walker func(k, v []byte) error) error {
	return nil
}
func (f failingTx) ForPrefix(table string, prefix []byte, walker func(k, v []byte) error) error {
	return nil
}
func (f failingTx) ForAmount(table string, prefix []byte, amount uint32, walker func(k, v []byte) error) error {
	return nil
}
func (f failingTx) Commit() error                             { return nil }
func (f failingTx) Rollback()                                 {}
func (f failingTx) ReadSequence(table string) (uint64, error) { return 0, nil }
func (f failingTx) ListBuckets() ([]string, error)            { return nil, nil }
func (f failingTx) ViewID() uint64                            { return 0 }
func (f failingTx) Cursor(table string) (kv.Cursor, error)    { return nil, f.err }
func (f failingTx) CursorDupSort(table string) (kv.CursorDupSort, error) {
	return nil, f.err
}
func (f failingTx) DBSize() (uint64, error) { return 0, nil }
func (f failingTx) Range(table string, fromPrefix, toPrefix []byte) (iter.KV, error) {
	return nil, nil
}
func (f failingTx) RangeAscend(table string, fromPrefix, toPrefix []byte, limit int) (iter.KV, error) {
	return nil, nil
}
func (f failingTx) RangeDescend(table string, fromPrefix, toPrefix []byte, limit int) (iter.KV, error) {
	return nil, nil
}
func (f failingTx) Prefix(table string, prefix []byte) (iter.KV, error) { return nil, nil }
func (f failingTx) RangeDupSort(table string, key []byte, fromPrefix, toPrefix []byte, asc order.By, limit int) (iter.KV, error) {
	return nil, nil
}
func (f failingTx) CHandle() unsafe.Pointer                 { return nil }
func (f failingTx) BucketSize(table string) (uint64, error) { return 0, nil }
