package olddb

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/ethdb"
)

func TestTxDbCloseAndNestedBegin(t *testing.T) {
	_, rwTx := memdb.NewTestTx(t)

	txDB := WrapIntoTxDB(rwTx)

	_, err := txDB.Begin(context.Background(), ethdb.RO)
	require.ErrorIs(t, err, errNestedTransactionsUnsupported)

	txDB.Close()
	require.Nil(t, txDB.Tx())
}

func TestMapMutationReturnsErrorsWithoutAttachedDB(t *testing.T) {
	batch := NewHashBatch(nil, nil, t.TempDir())

	err := batch.ForEach("bucket", nil, func(k, v []byte) error { return nil })
	require.ErrorIs(t, err, errMutationDBUnavailable)

	err = batch.ForPrefix("bucket", nil, func(k, v []byte) error { return nil })
	require.ErrorIs(t, err, errMutationDBUnavailable)

	err = batch.ForAmount("bucket", nil, 1, func(k, v []byte) error { return nil })
	require.ErrorIs(t, err, errMutationDBUnavailable)

	_, err = batch.Begin(context.Background(), ethdb.RW)
	require.ErrorIs(t, err, errMutationBeginUnsupported)
}

func TestBTreeMutationReturnsErrorsWithoutAttachedDB(t *testing.T) {
	var tx kv.RwTx
	batch := NewBatch(tx, nil)

	err := batch.ForEach("bucket", nil, func(k, v []byte) error { return nil })
	require.ErrorIs(t, err, errMutationDBUnavailable)

	err = batch.ForPrefix("bucket", nil, func(k, v []byte) error { return nil })
	require.ErrorIs(t, err, errMutationDBUnavailable)

	err = batch.ForAmount("bucket", nil, 1, func(k, v []byte) error { return nil })
	require.ErrorIs(t, err, errMutationDBUnavailable)

	_, err = batch.Begin(context.Background(), ethdb.RW)
	require.ErrorIs(t, err, errMutationBeginUnsupported)
}

func TestTxDbRwKVReturnsNil(t *testing.T) {
	_, rwTx := memdb.NewTestTx(t)

	txDB := WrapIntoTxDB(rwTx)
	require.Nil(t, txDB.RwKV())
}
