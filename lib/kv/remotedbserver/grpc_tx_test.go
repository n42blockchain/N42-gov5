package remotedbserver

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/n42blockchain/N42/lib/gointerfaces/remote"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/lib/log/v3"
)

func TestGRPCVersion(t *testing.T) {
	s, _ := newServerWithData(t)
	client := newBufconnFixture(t, s)
	ctx := context.Background()

	v, err := client.Version(ctx, &emptypb.Empty{})
	require.NoError(t, err)
	require.Equal(t, kv.DBSchemaVersion.Major, v.Major)
}

func TestGRPCTxCursorLifecycle(t *testing.T) {
	s, _ := newServerWithData(t)
	client := newBufconnFixture(t, s)
	ctx := context.Background()

	stream, err := client.Tx(ctx)
	require.NoError(t, err)
	defer stream.CloseSend()

	// open message with txID/viewID
	pair, err := stream.Recv()
	require.NoError(t, err)
	require.NotZero(t, pair.TxId)

	// open a plain cursor
	require.NoError(t, stream.Send(&remote.Cursor{Op: remote.Op_OPEN, BucketName: kv.PlainState}))
	pair, err = stream.Recv()
	require.NoError(t, err)
	cursorID := pair.CursorId
	require.NotZero(t, cursorID)

	// FIRST
	require.NoError(t, stream.Send(&remote.Cursor{Op: remote.Op_FIRST, Cursor: cursorID}))
	pair, err = stream.Recv()
	require.NoError(t, err)
	require.Equal(t, []byte{1}, pair.K)
	require.Equal(t, []byte{1}, pair.V)

	// NEXT
	require.NoError(t, stream.Send(&remote.Cursor{Op: remote.Op_NEXT, Cursor: cursorID}))
	pair, err = stream.Recv()
	require.NoError(t, err)
	require.Equal(t, []byte{1}, pair.K)
	require.Equal(t, []byte{2}, pair.V)

	// SEEK
	require.NoError(t, stream.Send(&remote.Cursor{Op: remote.Op_SEEK, Cursor: cursorID, K: []byte{2}}))
	pair, err = stream.Recv()
	require.NoError(t, err)
	require.Equal(t, []byte{2}, pair.K)

	// SEEK_EXACT miss
	require.NoError(t, stream.Send(&remote.Cursor{Op: remote.Op_SEEK_EXACT, Cursor: cursorID, K: []byte{9}}))
	pair, err = stream.Recv()
	require.NoError(t, err)
	require.Nil(t, pair.K)

	// LAST / PREV
	require.NoError(t, stream.Send(&remote.Cursor{Op: remote.Op_LAST, Cursor: cursorID}))
	pair, err = stream.Recv()
	require.NoError(t, err)
	require.Equal(t, []byte{3}, pair.K)

	require.NoError(t, stream.Send(&remote.Cursor{Op: remote.Op_PREV, Cursor: cursorID}))
	pair, err = stream.Recv()
	require.NoError(t, err)
	require.Equal(t, []byte{2}, pair.K)

	// COUNT
	require.NoError(t, stream.Send(&remote.Cursor{Op: remote.Op_COUNT, Cursor: cursorID}))
	pair, err = stream.Recv()
	require.NoError(t, err)
	require.NotNil(t, pair.V)

	// CLOSE
	require.NoError(t, stream.Send(&remote.Cursor{Op: remote.Op_CLOSE, Cursor: cursorID}))
	_, err = stream.Recv()
	require.NoError(t, err)

	// using closed cursor afterwards -> unknown cursor error
	require.NoError(t, stream.Send(&remote.Cursor{Op: remote.Op_FIRST, Cursor: cursorID}))
	_, err = stream.Recv()
	require.Error(t, err)
}

func TestGRPCTxDupSortCursor(t *testing.T) {
	s, _ := newServerWithData(t)
	client := newBufconnFixture(t, s)
	ctx := context.Background()

	stream, err := client.Tx(ctx)
	require.NoError(t, err)
	defer stream.CloseSend()
	_, err = stream.Recv()
	require.NoError(t, err)

	require.NoError(t, stream.Send(&remote.Cursor{Op: remote.Op_OPEN_DUP_SORT, BucketName: kv.PlainState}))
	pair, err := stream.Recv()
	require.NoError(t, err)
	cursorID := pair.CursorId

	require.NoError(t, stream.Send(&remote.Cursor{Op: remote.Op_SEEK_BOTH, Cursor: cursorID, K: []byte{1}, V: []byte{1}}))
	pair, err = stream.Recv()
	require.NoError(t, err)
	require.Equal(t, []byte{1}, pair.V)

	require.NoError(t, stream.Send(&remote.Cursor{Op: remote.Op_FIRST_DUP, Cursor: cursorID}))
	pair, err = stream.Recv()
	require.NoError(t, err)
	require.Equal(t, []byte{1}, pair.V)

	require.NoError(t, stream.Send(&remote.Cursor{Op: remote.Op_NEXT_DUP, Cursor: cursorID}))
	pair, err = stream.Recv()
	require.NoError(t, err)
	require.Equal(t, []byte{2}, pair.V)

	require.NoError(t, stream.Send(&remote.Cursor{Op: remote.Op_LAST_DUP, Cursor: cursorID}))
	pair, err = stream.Recv()
	require.NoError(t, err)
	require.Equal(t, []byte{2}, pair.V)

	require.NoError(t, stream.Send(&remote.Cursor{Op: remote.Op_NEXT_NO_DUP, Cursor: cursorID}))
	pair, err = stream.Recv()
	require.NoError(t, err)
	require.Equal(t, []byte{2}, pair.K)

	require.NoError(t, stream.Send(&remote.Cursor{Op: remote.Op_SEEK_BOTH_EXACT, Cursor: cursorID, K: []byte{1}, V: []byte{2}}))
	pair, err = stream.Recv()
	require.NoError(t, err)
	require.Equal(t, []byte{1}, pair.K)
	require.Equal(t, []byte{2}, pair.V)
}

func TestGRPCTxUnknownCursorOpErrors(t *testing.T) {
	s, _ := newServerWithData(t)
	client := newBufconnFixture(t, s)
	ctx := context.Background()

	stream, err := client.Tx(ctx)
	require.NoError(t, err)
	defer stream.CloseSend()
	_, err = stream.Recv()
	require.NoError(t, err)

	require.NoError(t, stream.Send(&remote.Cursor{Op: remote.Op_FIRST, Cursor: 999}))
	_, err = stream.Recv()
	require.Error(t, err)
}

func TestGRPCTxCancelledContext(t *testing.T) {
	s, _ := newServerWithData(t)
	client := newBufconnFixture(t, s)
	ctx, cancel := context.WithCancel(context.Background())

	stream, err := client.Tx(ctx)
	require.NoError(t, err)
	_, err = stream.Recv()
	require.NoError(t, err)

	cancel()
	// give the server goroutine a moment to observe cancellation
	time.Sleep(20 * time.Millisecond)
	_ = stream.Send(&remote.Cursor{Op: remote.Op_FIRST, Cursor: 1})
	_, err = stream.Recv()
	require.Error(t, err)
}

func TestGRPCTxManyKeysStreaming(t *testing.T) {
	ctx := context.Background()
	db := memdb.NewTestDB(t)
	require.NoError(t, db.Update(ctx, func(tx kv.RwTx) error {
		wc, err := tx.RwCursor(kv.PlainState)
		if err != nil {
			return err
		}
		for i := 0; i < 5000; i++ {
			k := make([]byte, 4)
			k[0] = byte(i >> 24)
			k[1] = byte(i >> 16)
			k[2] = byte(i >> 8)
			k[3] = byte(i)
			if err := wc.Append(k, []byte("v")); err != nil {
				return err
			}
		}
		return nil
	}))

	s := NewKvServer(ctx, db, nil, nil, nil, log.New())
	client := newBufconnFixture(t, s)

	stream, err := client.Tx(ctx)
	require.NoError(t, err)
	defer stream.CloseSend()
	_, err = stream.Recv()
	require.NoError(t, err)

	require.NoError(t, stream.Send(&remote.Cursor{Op: remote.Op_OPEN, BucketName: kv.PlainState}))
	pair, err := stream.Recv()
	require.NoError(t, err)
	cursorID := pair.CursorId

	require.NoError(t, stream.Send(&remote.Cursor{Op: remote.Op_FIRST, Cursor: cursorID}))
	_, err = stream.Recv()
	require.NoError(t, err)

	count := 1
	for i := 0; i < 4999; i++ {
		require.NoError(t, stream.Send(&remote.Cursor{Op: remote.Op_NEXT, Cursor: cursorID}))
		pair, err := stream.Recv()
		require.NoError(t, err)
		if pair.K == nil {
			break
		}
		count++
	}
	require.Equal(t, 5000, count)
}

func TestGRPCDomainHistoryIndexRangeRequireTemporalTx(t *testing.T) {
	s, _ := newServerWithData(t)
	client := newBufconnFixture(t, s)
	ctx := context.Background()

	stream, err := client.Tx(ctx)
	require.NoError(t, err)
	defer stream.CloseSend()
	pair, err := stream.Recv()
	require.NoError(t, err)
	txID := pair.TxId

	_, err = client.DomainGet(ctx, &remote.DomainGetReq{TxId: txID, Table: "PlainState", Latest: true})
	require.Error(t, err)

	_, err = client.HistoryGet(ctx, &remote.HistoryGetReq{TxId: txID, Table: "PlainState"})
	require.Error(t, err)

	_, err = client.IndexRange(ctx, &remote.IndexRangeReq{TxId: txID, Table: "PlainState"})
	require.Error(t, err)
}

func TestGRPCRangeAscendAndDescend(t *testing.T) {
	s, _ := newServerWithData(t)
	client := newBufconnFixture(t, s)
	ctx := context.Background()

	stream, err := client.Tx(ctx)
	require.NoError(t, err)
	defer stream.CloseSend()
	pair, err := stream.Recv()
	require.NoError(t, err)
	txID := pair.TxId

	reply, err := client.Range(ctx, &remote.RangeReq{TxId: txID, Table: kv.PlainState, OrderAscend: true, Limit: -1})
	require.NoError(t, err)
	require.NotEmpty(t, reply.Keys)

	replyDesc, err := client.Range(ctx, &remote.RangeReq{TxId: txID, Table: kv.PlainState, OrderAscend: false, Limit: -1})
	require.NoError(t, err)
	require.NotEmpty(t, replyDesc.Keys)
}

func TestGRPCRangeUnknownTxErrors(t *testing.T) {
	s, _ := newServerWithData(t)
	client := newBufconnFixture(t, s)
	ctx := context.Background()

	_, err := client.Range(ctx, &remote.RangeReq{TxId: 123456, Table: kv.PlainState, OrderAscend: true, Limit: -1})
	require.Error(t, err)
}
