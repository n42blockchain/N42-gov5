package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/lib/bmt"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

func newTestDB(t *testing.T) kv.RwDB {
	t.Helper()
	tableCfg := kv.TableCfg{
		BMTNodeTable: {},
		BMTRootTable: {},
	}
	db := mdbx.NewMDBX(log.New()).InMem(t.TempDir()).
		WithTableCfg(func(_ kv.TableCfg) kv.TableCfg { return tableCfg }).
		MustOpen()
	t.Cleanup(db.Close)
	return db
}

func TestMDBXStoreGetPut(t *testing.T) {
	r := require.New(t)
	db := newTestDB(t)
	tx, err := db.BeginRw(context.Background())
	r.NoError(err)
	defer tx.Rollback()

	s := NewMDBXStore(tx, BMTNodeTable)
	var h bmt.Hash
	h[0] = 1

	// Missing key -> bmt.ErrNotFound.
	_, err = s.Get(h)
	r.ErrorIs(err, bmt.ErrNotFound)

	val := bmt.NodeValue("hello-node")
	r.NoError(s.Put(h, val))

	got, err := s.Get(h)
	r.NoError(err)
	r.Equal(val, got)

	// Get returns a defensive copy: mutating the returned slice must not
	// corrupt what is stored.
	got[0] = 'X'
	got2, err := s.Get(h)
	r.NoError(err)
	r.Equal(val, got2)
}

func TestMDBXStoreImplementsNodeStore(t *testing.T) {
	var _ bmt.NodeStore = (*MDBXStore)(nil)
}

func TestWriteReadBMTRoot(t *testing.T) {
	r := require.New(t)
	db := newTestDB(t)
	tx, err := db.BeginRw(context.Background())
	r.NoError(err)
	defer tx.Rollback()

	// No root written yet -> EmptyHash, no error.
	got, err := ReadBMTRoot(tx)
	r.NoError(err)
	r.Equal(bmt.EmptyHash, got)

	var root bmt.Hash
	for i := range root {
		root[i] = byte(i)
	}
	r.NoError(WriteBMTRoot(tx, root))

	got, err = ReadBMTRoot(tx)
	r.NoError(err)
	r.Equal(root, got)
}

func TestWriteReadBMTVersion(t *testing.T) {
	r := require.New(t)
	db := newTestDB(t)
	tx, err := db.BeginRw(context.Background())
	r.NoError(err)
	defer tx.Rollback()

	// No version written yet -> 0, no error.
	v, err := ReadBMTVersion(tx)
	r.NoError(err)
	r.Equal(uint64(0), v)

	r.NoError(WriteBMTVersion(tx, 42))

	v, err = ReadBMTVersion(tx)
	r.NoError(err)
	r.Equal(uint64(42), v)

	r.NoError(WriteBMTVersion(tx, 1<<40))
	v, err = ReadBMTVersion(tx)
	r.NoError(err)
	r.Equal(uint64(1<<40), v)
}
