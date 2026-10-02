package direct

import (
	"context"
	"errors"
	"testing"

	"github.com/n42blockchain/N42/lib/gointerfaces/execution"
	"github.com/n42blockchain/N42/lib/gointerfaces/types"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/emptypb"
)

type fakeExecutionServer struct {
	execution.UnimplementedExecutionServer
	err error
}

func (f *fakeExecutionServer) AssembleBlock(ctx context.Context, in *execution.AssembleBlockRequest) (*execution.AssembleBlockResponse, error) {
	return &execution.AssembleBlockResponse{}, f.err
}
func (f *fakeExecutionServer) GetBodiesByHashes(ctx context.Context, in *execution.GetBodiesByHashesRequest) (*execution.GetBodiesBatchResponse, error) {
	return &execution.GetBodiesBatchResponse{}, f.err
}
func (f *fakeExecutionServer) GetBodiesByRange(ctx context.Context, in *execution.GetBodiesByRangeRequest) (*execution.GetBodiesBatchResponse, error) {
	return &execution.GetBodiesBatchResponse{}, f.err
}
func (f *fakeExecutionServer) HasBlock(ctx context.Context, in *execution.GetSegmentRequest) (*execution.HasBlockResponse, error) {
	return &execution.HasBlockResponse{}, f.err
}
func (f *fakeExecutionServer) GetAssembledBlock(ctx context.Context, in *execution.GetAssembledBlockRequest) (*execution.GetAssembledBlockResponse, error) {
	return &execution.GetAssembledBlockResponse{}, f.err
}
func (f *fakeExecutionServer) InsertBlocks(ctx context.Context, in *execution.InsertBlocksRequest) (*execution.InsertionResult, error) {
	return &execution.InsertionResult{}, f.err
}
func (f *fakeExecutionServer) ValidateChain(ctx context.Context, in *execution.ValidationRequest) (*execution.ValidationReceipt, error) {
	return &execution.ValidationReceipt{}, f.err
}
func (f *fakeExecutionServer) UpdateForkChoice(ctx context.Context, in *execution.ForkChoice) (*execution.ForkChoiceReceipt, error) {
	return &execution.ForkChoiceReceipt{}, f.err
}
func (f *fakeExecutionServer) GetHeader(ctx context.Context, in *execution.GetSegmentRequest) (*execution.GetHeaderResponse, error) {
	return &execution.GetHeaderResponse{}, f.err
}
func (f *fakeExecutionServer) CurrentHeader(ctx context.Context, in *emptypb.Empty) (*execution.GetHeaderResponse, error) {
	return &execution.GetHeaderResponse{}, f.err
}
func (f *fakeExecutionServer) GetTD(ctx context.Context, in *execution.GetSegmentRequest) (*execution.GetTDResponse, error) {
	return &execution.GetTDResponse{}, f.err
}
func (f *fakeExecutionServer) GetBody(ctx context.Context, in *execution.GetSegmentRequest) (*execution.GetBodyResponse, error) {
	return &execution.GetBodyResponse{}, f.err
}
func (f *fakeExecutionServer) IsCanonicalHash(ctx context.Context, in *types.H256) (*execution.IsCanonicalResponse, error) {
	return &execution.IsCanonicalResponse{}, f.err
}
func (f *fakeExecutionServer) GetHeaderHashNumber(ctx context.Context, in *types.H256) (*execution.GetHeaderHashNumberResponse, error) {
	return &execution.GetHeaderHashNumberResponse{}, f.err
}
func (f *fakeExecutionServer) GetForkChoice(ctx context.Context, in *emptypb.Empty) (*execution.ForkChoice, error) {
	return &execution.ForkChoice{}, f.err
}
func (f *fakeExecutionServer) Ready(ctx context.Context, in *emptypb.Empty) (*execution.ReadyResponse, error) {
	return &execution.ReadyResponse{}, f.err
}
func (f *fakeExecutionServer) FrozenBlocks(ctx context.Context, in *emptypb.Empty) (*execution.FrozenBlocksResponse, error) {
	return &execution.FrozenBlocksResponse{}, f.err
}

func TestExecutionClientDirectForwardsAllMethods(t *testing.T) {
	ctx := context.Background()
	fake := &fakeExecutionServer{}
	c := NewExecutionClientDirect(fake)

	_, err := c.AssembleBlock(ctx, &execution.AssembleBlockRequest{})
	require.NoError(t, err)
	_, err = c.GetBodiesByHashes(ctx, &execution.GetBodiesByHashesRequest{})
	require.NoError(t, err)
	_, err = c.GetBodiesByRange(ctx, &execution.GetBodiesByRangeRequest{})
	require.NoError(t, err)
	_, err = c.HasBlock(ctx, &execution.GetSegmentRequest{})
	require.NoError(t, err)
	_, err = c.GetAssembledBlock(ctx, &execution.GetAssembledBlockRequest{})
	require.NoError(t, err)
	_, err = c.InsertBlocks(ctx, &execution.InsertBlocksRequest{})
	require.NoError(t, err)
	_, err = c.ValidateChain(ctx, &execution.ValidationRequest{})
	require.NoError(t, err)
	_, err = c.UpdateForkChoice(ctx, &execution.ForkChoice{})
	require.NoError(t, err)
	_, err = c.GetHeader(ctx, &execution.GetSegmentRequest{})
	require.NoError(t, err)
	_, err = c.CurrentHeader(ctx, &emptypb.Empty{})
	require.NoError(t, err)
	_, err = c.GetTD(ctx, &execution.GetSegmentRequest{})
	require.NoError(t, err)
	_, err = c.GetBody(ctx, &execution.GetSegmentRequest{})
	require.NoError(t, err)
	_, err = c.IsCanonicalHash(ctx, &types.H256{})
	require.NoError(t, err)
	_, err = c.GetHeaderHashNumber(ctx, &types.H256{})
	require.NoError(t, err)
	_, err = c.GetForkChoice(ctx, &emptypb.Empty{})
	require.NoError(t, err)
	_, err = c.Ready(ctx, &emptypb.Empty{})
	require.NoError(t, err)
	_, err = c.FrozenBlocks(ctx, &emptypb.Empty{})
	require.NoError(t, err)

	fake.err = errors.New("boom")
	_, err = c.AssembleBlock(ctx, &execution.AssembleBlockRequest{})
	require.Error(t, err)
}
