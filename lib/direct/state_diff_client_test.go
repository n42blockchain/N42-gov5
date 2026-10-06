package direct

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/n42blockchain/N42/lib/gointerfaces/remote"
	"github.com/stretchr/testify/require"
)

type fakeKVServerForStateDiff struct {
	remote.UnimplementedKVServer
	snapshotsErr error
	sendBatches  []*remote.StateChangeBatch
	streamErr    error
}

func (f *fakeKVServerForStateDiff) Snapshots(ctx context.Context, in *remote.SnapshotsRequest) (*remote.SnapshotsReply, error) {
	if f.snapshotsErr != nil {
		return nil, f.snapshotsErr
	}
	return &remote.SnapshotsReply{BlocksFiles: []string{"a.seg"}}, nil
}

func (f *fakeKVServerForStateDiff) StateChanges(req *remote.StateChangeRequest, srv remote.KV_StateChangesServer) error {
	for _, b := range f.sendBatches {
		if err := srv.Send(b); err != nil {
			return err
		}
	}
	return f.streamErr
}

func TestStateDiffClientDirectSnapshots(t *testing.T) {
	ctx := context.Background()
	fake := &fakeKVServerForStateDiff{}
	c := NewStateDiffClientDirect(fake)

	reply, err := c.Snapshots(ctx, &remote.SnapshotsRequest{})
	require.NoError(t, err)
	require.Equal(t, []string{"a.seg"}, reply.BlocksFiles)

	fake.snapshotsErr = errors.New("boom")
	_, err = c.Snapshots(ctx, &remote.SnapshotsRequest{})
	require.Error(t, err)
}

func TestStateDiffClientDirectStateChangesStream(t *testing.T) {
	ctx := context.Background()
	fake := &fakeKVServerForStateDiff{
		sendBatches: []*remote.StateChangeBatch{
			{StateVersionId: 1},
			{StateVersionId: 2},
		},
	}
	c := NewStateDiffClientDirect(fake)

	stream, err := c.StateChanges(ctx, &remote.StateChangeRequest{})
	require.NoError(t, err)

	b1, err := stream.Recv()
	require.NoError(t, err)
	require.Equal(t, uint64(1), b1.StateVersionId)

	b2, err := stream.Recv()
	require.NoError(t, err)
	require.Equal(t, uint64(2), b2.StateVersionId)

	_, err = stream.Recv()
	require.ErrorIs(t, err, io.EOF)

	require.NotNil(t, stream.(*StateDiffStreamC).Context())
}

func TestStateDiffClientDirectStateChangesStreamError(t *testing.T) {
	ctx := context.Background()
	fake := &fakeKVServerForStateDiff{streamErr: errors.New("stream boom")}
	c := NewStateDiffClientDirect(fake)

	stream, err := c.StateChanges(ctx, &remote.StateChangeRequest{})
	require.NoError(t, err)

	_, err = stream.Recv()
	require.Error(t, err)
	require.NotErrorIs(t, err, io.EOF)
}
