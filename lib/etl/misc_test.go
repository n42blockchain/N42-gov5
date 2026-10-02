package etl

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
)

func TestProgressFromKey(t *testing.T) {
	r := require.New(t)
	r.Equal(0, ProgressFromKey(nil))
	r.Equal(0, ProgressFromKey([]byte{}))
	r.Equal(0, ProgressFromKey([]byte{0x00}))
	var nibble float64 = 0x0f
	r.Equal(int(nibble*3.3), ProgressFromKey([]byte{0xff}))
}

func TestCurrentTableReaderGet(t *testing.T) {
	r := require.New(t)
	_, tx := memdb.NewTestTx(t)
	table := kv.ChaindataTables[0]
	r.NoError(tx.Put(table, []byte("key"), []byte("val")))

	reader := &currentTableReader{getter: tx, bucket: table}
	v, err := reader.Get([]byte("key"))
	r.NoError(err)
	r.Equal([]byte("val"), v)

	v, err = reader.Get([]byte("missing"))
	r.NoError(err)
	r.Nil(v)
}

func TestMemoryDataProviderString(t *testing.T) {
	r := require.New(t)
	buf := NewSortableBuffer(1024)
	buf.Put([]byte("a"), []byte("1"))
	provider := KeepInRAM(buf)
	r.Contains(provider.(interface{ String() string }).String(), "buffer.Len: 1")
	r.NoError(provider.Wait())
	provider.Dispose() // no-op, must not panic
}
