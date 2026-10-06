package direct

import (
	"context"
	"errors"
	"io"
	"testing"

	txpool_proto "github.com/n42blockchain/N42/lib/gointerfaces/txpool"
	"github.com/n42blockchain/N42/lib/gointerfaces/types"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/emptypb"
)

type fakeMiningServer struct {
	txpool_proto.UnimplementedMiningServer
	err                error
	pendingBlockEvents []*txpool_proto.OnPendingBlockReply
	pendingBlockErr    error
	minedBlockEvents   []*txpool_proto.OnMinedBlockReply
	minedBlockErr      error
	pendingLogsEvents  []*txpool_proto.OnPendingLogsReply
	pendingLogsErr     error
}

func (f *fakeMiningServer) Version(ctx context.Context, in *emptypb.Empty) (*types.VersionReply, error) {
	return &types.VersionReply{Major: 1}, f.err
}
func (f *fakeMiningServer) GetWork(ctx context.Context, in *txpool_proto.GetWorkRequest) (*txpool_proto.GetWorkReply, error) {
	return &txpool_proto.GetWorkReply{}, f.err
}
func (f *fakeMiningServer) SubmitWork(ctx context.Context, in *txpool_proto.SubmitWorkRequest) (*txpool_proto.SubmitWorkReply, error) {
	return &txpool_proto.SubmitWorkReply{}, f.err
}
func (f *fakeMiningServer) SubmitHashRate(ctx context.Context, in *txpool_proto.SubmitHashRateRequest) (*txpool_proto.SubmitHashRateReply, error) {
	return &txpool_proto.SubmitHashRateReply{}, f.err
}
func (f *fakeMiningServer) HashRate(ctx context.Context, in *txpool_proto.HashRateRequest) (*txpool_proto.HashRateReply, error) {
	return &txpool_proto.HashRateReply{}, f.err
}
func (f *fakeMiningServer) Mining(ctx context.Context, in *txpool_proto.MiningRequest) (*txpool_proto.MiningReply, error) {
	return &txpool_proto.MiningReply{}, f.err
}
func (f *fakeMiningServer) OnPendingBlock(req *txpool_proto.OnPendingBlockRequest, srv txpool_proto.Mining_OnPendingBlockServer) error {
	for _, e := range f.pendingBlockEvents {
		if err := srv.Send(e); err != nil {
			return err
		}
	}
	return f.pendingBlockErr
}
func (f *fakeMiningServer) OnMinedBlock(req *txpool_proto.OnMinedBlockRequest, srv txpool_proto.Mining_OnMinedBlockServer) error {
	for _, e := range f.minedBlockEvents {
		if err := srv.Send(e); err != nil {
			return err
		}
	}
	return f.minedBlockErr
}
func (f *fakeMiningServer) OnPendingLogs(req *txpool_proto.OnPendingLogsRequest, srv txpool_proto.Mining_OnPendingLogsServer) error {
	for _, e := range f.pendingLogsEvents {
		if err := srv.Send(e); err != nil {
			return err
		}
	}
	return f.pendingLogsErr
}

func TestMiningClientForwardsAllUnaryMethods(t *testing.T) {
	ctx := context.Background()
	fake := &fakeMiningServer{}
	c := NewMiningClient(fake)

	_, err := c.Version(ctx, &emptypb.Empty{})
	require.NoError(t, err)
	_, err = c.GetWork(ctx, &txpool_proto.GetWorkRequest{})
	require.NoError(t, err)
	_, err = c.SubmitWork(ctx, &txpool_proto.SubmitWorkRequest{})
	require.NoError(t, err)
	_, err = c.SubmitHashRate(ctx, &txpool_proto.SubmitHashRateRequest{})
	require.NoError(t, err)
	_, err = c.HashRate(ctx, &txpool_proto.HashRateRequest{})
	require.NoError(t, err)
	_, err = c.Mining(ctx, &txpool_proto.MiningRequest{})
	require.NoError(t, err)

	fake.err = errors.New("boom")
	_, err = c.Version(ctx, &emptypb.Empty{})
	require.Error(t, err)
}

func TestMiningClientOnPendingBlockStream(t *testing.T) {
	ctx := context.Background()
	fake := &fakeMiningServer{pendingBlockEvents: []*txpool_proto.OnPendingBlockReply{{RplBlock: []byte{1}}}}
	c := NewMiningClient(fake)

	stream, err := c.OnPendingBlock(ctx, &txpool_proto.OnPendingBlockRequest{})
	require.NoError(t, err)
	require.NotNil(t, stream.(*MiningOnPendingBlockC).Context())
	ev, err := stream.Recv()
	require.NoError(t, err)
	require.Equal(t, []byte{1}, ev.RplBlock)
	_, err = stream.Recv()
	require.ErrorIs(t, err, io.EOF)
}

func TestMiningClientOnPendingBlockStreamError(t *testing.T) {
	ctx := context.Background()
	fake := &fakeMiningServer{pendingBlockErr: errors.New("boom")}
	c := NewMiningClient(fake)
	stream, err := c.OnPendingBlock(ctx, &txpool_proto.OnPendingBlockRequest{})
	require.NoError(t, err)
	_, err = stream.Recv()
	require.Error(t, err)
	require.NotErrorIs(t, err, io.EOF)
}

func TestMiningClientOnMinedBlockStream(t *testing.T) {
	ctx := context.Background()
	fake := &fakeMiningServer{minedBlockEvents: []*txpool_proto.OnMinedBlockReply{{RplBlock: []byte{2}}}}
	c := NewMiningClient(fake)

	stream, err := c.OnMinedBlock(ctx, &txpool_proto.OnMinedBlockRequest{})
	require.NoError(t, err)
	require.NotNil(t, stream.(*MiningOnMinedBlockC).Context())
	ev, err := stream.Recv()
	require.NoError(t, err)
	require.Equal(t, []byte{2}, ev.RplBlock)
	_, err = stream.Recv()
	require.ErrorIs(t, err, io.EOF)
}

func TestMiningClientOnMinedBlockStreamError(t *testing.T) {
	ctx := context.Background()
	fake := &fakeMiningServer{minedBlockErr: errors.New("boom")}
	c := NewMiningClient(fake)
	stream, err := c.OnMinedBlock(ctx, &txpool_proto.OnMinedBlockRequest{})
	require.NoError(t, err)
	_, err = stream.Recv()
	require.Error(t, err)
	require.NotErrorIs(t, err, io.EOF)
}

func TestMiningClientOnPendingLogsStream(t *testing.T) {
	ctx := context.Background()
	fake := &fakeMiningServer{pendingLogsEvents: []*txpool_proto.OnPendingLogsReply{{RplLogs: []byte{3}}}}
	c := NewMiningClient(fake)

	stream, err := c.OnPendingLogs(ctx, &txpool_proto.OnPendingLogsRequest{})
	require.NoError(t, err)
	require.NotNil(t, stream.(*MiningOnPendingLogsC).Context())
	ev, err := stream.Recv()
	require.NoError(t, err)
	require.Equal(t, []byte{3}, ev.RplLogs)
	_, err = stream.Recv()
	require.ErrorIs(t, err, io.EOF)
}

func TestMiningClientOnPendingLogsStreamError(t *testing.T) {
	ctx := context.Background()
	fake := &fakeMiningServer{pendingLogsErr: errors.New("boom")}
	c := NewMiningClient(fake)
	stream, err := c.OnPendingLogs(ctx, &txpool_proto.OnPendingLogsRequest{})
	require.NoError(t, err)
	_, err = stream.Recv()
	require.Error(t, err)
	require.NotErrorIs(t, err, io.EOF)
}
