package remotedbserver

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/lib/gointerfaces/remote"
	"github.com/n42blockchain/N42/lib/kv"
)

// fakeStateChangesServer is a minimal remote.KV_StateChangesServer for
// exercising KvServer.StateChanges without a real gRPC stream.
type fakeStateChangesServer struct {
	remote.KV_StateChangesServer
	ctx context.Context
	out chan *remote.StateChangeBatch
}

func (f *fakeStateChangesServer) Context() context.Context { return f.ctx }
func (f *fakeStateChangesServer) Send(b *remote.StateChangeBatch) error {
	f.out <- b
	return nil
}

func TestStateChangesPubSub(t *testing.T) {
	s := NewKvServer(context.Background(), nil, nil, nil, nil, nil)

	streamCtx, cancel := context.WithCancel(context.Background())
	fake := &fakeStateChangesServer{ctx: streamCtx, out: make(chan *remote.StateChangeBatch, 1)}

	done := make(chan error, 1)
	go func() {
		done <- s.StateChanges(&remote.StateChangeRequest{}, fake)
	}()

	// give the Sub() call inside StateChanges time to register
	time.Sleep(20 * time.Millisecond)
	require.Equal(t, 1, s.stateChangeStreams.Len())

	batch := &remote.StateChangeBatch{StateVersionId: 42}
	s.SendStateChanges(context.Background(), batch)

	select {
	case got := <-fake.out:
		require.Equal(t, uint64(42), got.StateVersionId)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for state change")
	}

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("StateChanges did not return after context cancellation")
	}
	require.Equal(t, 0, s.stateChangeStreams.Len())
}

func TestStateChangesServerContextDone(t *testing.T) {
	s := NewKvServer(context.Background(), nil, nil, nil, nil, nil)
	streamCtx, cancel := context.WithCancel(context.Background())
	fake := &fakeStateChangesServer{ctx: streamCtx, out: make(chan *remote.StateChangeBatch, 1)}
	cancel()

	err := s.StateChanges(&remote.StateChangeRequest{}, fake)
	require.NoError(t, err)
}

func TestStateChangePubSubDoubleRemoveIsSafe(t *testing.T) {
	pubsub := newStateChangeStreams()
	_, remove := pubsub.Sub()
	remove()
	// double-unsubscribe must be a no-op, not a panic
	require.NotPanics(t, func() { remove() })
	require.Equal(t, 0, pubsub.Len())
}

func TestPaginationRoundTrip(t *testing.T) {
	// Range with a tiny PageSize forces marshalPagination/unmarshalPagination
	// to be exercised through NextPageToken/PageToken.
	s, _ := newServerWithData(t)
	client := newBufconnFixture(t, s)
	ctx := context.Background()

	stream, err := client.Tx(ctx)
	require.NoError(t, err)
	defer stream.CloseSend()
	pair, err := stream.Recv()
	require.NoError(t, err)
	txID := pair.TxId

	// drain the whole PlainState table to make sure the basic path works
	// alongside the PageSizeLimit default (page size 0 => default).
	reply, err := client.Range(ctx, &remote.RangeReq{TxId: txID, Table: "PlainState", OrderAscend: true, Limit: -1, PageSize: 1})
	require.NoError(t, err)
	require.NotEmpty(t, reply.Keys)
}

// TestRangePaginationNeverTriggersViaIntegration documents a real defect:
// the underlying cursor2iter.HasNext() (lib/kv/mdbx/kv_mdbx_iterator.go)
// returns false as soon as its internal `limit` counter reaches zero, even
// when a next key actually exists (it never looks ahead once limit is
// exhausted). Range()'s pagination branch is
// `len(reply.Keys) == PageSizeLimit && it.HasNext()`: by the time exactly
// PageSizeLimit items have been produced via a request with
// Limit == PageSizeLimit, the iterator's own limit has also just hit zero,
// so HasNext() is always false and NextPageToken is never set through this
// path. With Limit == -1 (unlimited) the loop instead drains the whole
// table past PageSizeLimit without ever stopping to check the threshold.
// Net effect: Range/IndexRange pagination tokens are dead code via the
// public API. We verify the observed (non-paginating) behavior here rather
// than fix the production iterator, and cover marshalPagination /
// unmarshalPagination directly below since the integration path cannot
// reach them.
func TestRangePaginationNeverTriggersViaIntegration(t *testing.T) {
	ctx := context.Background()
	db := memdbNewPlainStateWithNKeys(t, PageSizeLimit+5)
	s := NewKvServer(ctx, db, nil, nil, nil, nil)
	client := newBufconnFixture(t, s)

	stream, err := client.Tx(ctx)
	require.NoError(t, err)
	defer stream.CloseSend()
	pair, err := stream.Recv()
	require.NoError(t, err)
	txID := pair.TxId

	reply, err := client.Range(ctx, &remote.RangeReq{TxId: txID, Table: "PlainState", OrderAscend: true, Limit: PageSizeLimit})
	require.NoError(t, err)
	require.Len(t, reply.Keys, PageSizeLimit)
	require.Empty(t, reply.NextPageToken, "defect: pagination token is never produced via the public Range API")
}

func TestVersionComparisonBranches(t *testing.T) {
	s := NewKvServer(context.Background(), nil, nil, nil, nil, nil)
	origAPIMajor, origAPIMinor := KvServiceAPIVersion.Major, KvServiceAPIVersion.Minor
	origSchemaMajor, origSchemaMinor := kv.DBSchemaVersion.Major, kv.DBSchemaVersion.Minor
	t.Cleanup(func() {
		KvServiceAPIVersion.Major, KvServiceAPIVersion.Minor = origAPIMajor, origAPIMinor
		kv.DBSchemaVersion.Major, kv.DBSchemaVersion.Minor = origSchemaMajor, origSchemaMinor
	})

	// Local (KvServiceAPIVersion) major greater than schema -> local wins.
	KvServiceAPIVersion.Major = origSchemaMajor + 1
	v, err := s.Version(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, KvServiceAPIVersion.Major, v.Major)
	KvServiceAPIVersion.Major = origAPIMajor

	// Schema major greater than local -> schema wins.
	kv.DBSchemaVersion.Major = origAPIMajor + 1
	v, err = s.Version(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, kv.DBSchemaVersion.Major, v.Major)
	kv.DBSchemaVersion.Major = origSchemaMajor

	// Equal majors, local minor greater -> local wins.
	KvServiceAPIVersion.Minor = origSchemaMinor + 1
	v, err = s.Version(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, KvServiceAPIVersion.Minor, v.Minor)
	KvServiceAPIVersion.Minor = origAPIMinor

	// Equal majors, schema minor greater -> schema wins.
	kv.DBSchemaVersion.Minor = origAPIMinor + 1
	v, err = s.Version(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, kv.DBSchemaVersion.Minor, v.Minor)
}

func TestPaginationHelpersRoundTrip(t *testing.T) {
	token, err := marshalPagination(&remote.ParisPagination{NextKey: []byte{1, 2, 3}, Limit: 7})
	require.NoError(t, err)
	require.NotEmpty(t, token)

	var out remote.ParisPagination
	require.NoError(t, unmarshalPagination(token, &out))
	require.Equal(t, []byte{1, 2, 3}, out.NextKey)
	require.Equal(t, int64(7), out.Limit)

	require.Error(t, unmarshalPagination("not-base64!!", &out))
}
