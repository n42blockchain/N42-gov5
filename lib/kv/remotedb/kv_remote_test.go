package remotedb

import (
	"testing"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/stretchr/testify/require"
)

func TestReadOnlyMethodsReturnErrors(t *testing.T) {
	db := &DB{}
	tx := &tx{db: db}

	require.Equal(t, kv.DefaultPageSize(), db.PageSize())
	require.Nil(t, db.CHandle())

	_, err := tx.IncrementSequence("bucket", 1)
	require.ErrorIs(t, err, errRemoteDBSequenceUnsupported)

	_, err = tx.ReadSequence("bucket")
	require.ErrorIs(t, err, errRemoteDBSequenceUnsupported)

	require.ErrorIs(t, tx.Append("bucket", []byte("k"), []byte("v")), errRemoteDBReadOnly)
	require.ErrorIs(t, tx.AppendDup("bucket", []byte("k"), []byte("v")), errRemoteDBReadOnly)
	require.ErrorIs(t, tx.Commit(), errRemoteDBReadOnly)

	_, err = tx.DBSize()
	require.ErrorIs(t, err, errRemoteDBSizeUnsupported)

	_, err = tx.BucketSize("bucket")
	require.ErrorIs(t, err, errRemoteBucketSizeUnsupported)

	_, err = tx.RangeDupSort("bucket", nil, nil, nil, false, 0)
	require.ErrorIs(t, err, errRemoteRangeDupSortUnsupported)

	require.Nil(t, tx.CHandle())
}

func TestRemoteCursorDupSortUnsupportedMethodsReturnErrors(t *testing.T) {
	cursor := &remoteCursorDupSort{}

	require.ErrorIs(t, cursor.DeleteExact(nil, nil), errRemoteDupSortWriteUnsupported)
	require.ErrorIs(t, cursor.AppendDup(nil, nil), errRemoteDupSortWriteUnsupported)
	require.ErrorIs(t, cursor.PutNoDupData(nil, nil), errRemoteDupSortWriteUnsupported)
	require.ErrorIs(t, cursor.DeleteCurrentDuplicates(), errRemoteDupSortWriteUnsupported)

	_, err := cursor.CountDuplicates()
	require.ErrorIs(t, err, errRemoteDupSortCountUnsupported)
}
