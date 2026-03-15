package membatch

import (
	"context"
	"os"
	"testing"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/lib/log/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMapmutation_Flush_Close(t *testing.T) {
	db := memdb.NewTestDB(t)

	tx, err := db.BeginRw(context.Background())
	require.NoError(t, err)
	defer tx.Rollback()

	batch := NewHashBatch(tx, nil, os.TempDir(), log.New())
	defer func() {
		batch.Close()
	}()
	assert.Equal(t, batch.size, 0)
	err = batch.Put(kv.ChaindataTables[0], []byte{1}, []byte{1})
	require.NoError(t, err)
	assert.Equal(t, batch.size, 2)
	err = batch.Put(kv.ChaindataTables[0], []byte{2}, []byte{2})
	require.NoError(t, err)
	assert.Equal(t, batch.size, 4)
	err = batch.Put(kv.ChaindataTables[0], []byte{1}, []byte{3, 2, 1, 0})
	require.NoError(t, err)
	assert.Equal(t, batch.size, 7)
	err = batch.Flush(context.Background(), tx)
	require.NoError(t, err)
	batch.Close()
	batch.Close()
}

func TestMapmutationReturnsErrorsWithoutAttachedDB(t *testing.T) {
	batch := NewHashBatch(nil, nil, os.TempDir(), log.New())

	_, _, err := batch.Last(kv.ChaindataTables[0])
	require.ErrorIs(t, err, errMapmutationDBUnavailable)

	err = batch.ForEach(kv.ChaindataTables[0], nil, func(k, v []byte) error { return nil })
	require.ErrorIs(t, err, errMapmutationDBUnavailable)

	err = batch.ForPrefix(kv.ChaindataTables[0], nil, func(k, v []byte) error { return nil })
	require.ErrorIs(t, err, errMapmutationDBUnavailable)

	err = batch.ForAmount(kv.ChaindataTables[0], nil, 1, func(k, v []byte) error { return nil })
	require.ErrorIs(t, err, errMapmutationDBUnavailable)

	err = batch.Commit()
	require.ErrorIs(t, err, errMapmutationCommitUnsupported)

	batch.Rollback()
	batch.Rollback()
}
