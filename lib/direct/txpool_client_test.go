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

type fakeTxpoolServer struct {
	txpool_proto.UnimplementedTxpoolServer
	err         error
	onAddErr    error
	onAddEvents []*txpool_proto.OnAddReply
}

func (f *fakeTxpoolServer) Version(ctx context.Context, in *emptypb.Empty) (*types.VersionReply, error) {
	return &types.VersionReply{Major: 1}, f.err
}
func (f *fakeTxpoolServer) FindUnknown(ctx context.Context, in *txpool_proto.TxHashes) (*txpool_proto.TxHashes, error) {
	return &txpool_proto.TxHashes{}, f.err
}
func (f *fakeTxpoolServer) Add(ctx context.Context, in *txpool_proto.AddRequest) (*txpool_proto.AddReply, error) {
	return &txpool_proto.AddReply{}, f.err
}
func (f *fakeTxpoolServer) Transactions(ctx context.Context, in *txpool_proto.TransactionsRequest) (*txpool_proto.TransactionsReply, error) {
	return &txpool_proto.TransactionsReply{}, f.err
}
func (f *fakeTxpoolServer) All(ctx context.Context, in *txpool_proto.AllRequest) (*txpool_proto.AllReply, error) {
	return &txpool_proto.AllReply{}, f.err
}
func (f *fakeTxpoolServer) Pending(ctx context.Context, in *emptypb.Empty) (*txpool_proto.PendingReply, error) {
	return &txpool_proto.PendingReply{}, f.err
}
func (f *fakeTxpoolServer) Status(ctx context.Context, in *txpool_proto.StatusRequest) (*txpool_proto.StatusReply, error) {
	return &txpool_proto.StatusReply{}, f.err
}
func (f *fakeTxpoolServer) Nonce(ctx context.Context, in *txpool_proto.NonceRequest) (*txpool_proto.NonceReply, error) {
	return &txpool_proto.NonceReply{}, f.err
}
func (f *fakeTxpoolServer) OnAdd(req *txpool_proto.OnAddRequest, srv txpool_proto.Txpool_OnAddServer) error {
	for _, e := range f.onAddEvents {
		if err := srv.Send(e); err != nil {
			return err
		}
	}
	return f.onAddErr
}

func TestTxPoolClientForwardsAllMethods(t *testing.T) {
	ctx := context.Background()
	fake := &fakeTxpoolServer{}
	c := NewTxPoolClient(fake)

	_, err := c.Version(ctx, &emptypb.Empty{})
	require.NoError(t, err)
	_, err = c.FindUnknown(ctx, &txpool_proto.TxHashes{})
	require.NoError(t, err)
	_, err = c.Add(ctx, &txpool_proto.AddRequest{})
	require.NoError(t, err)
	_, err = c.Transactions(ctx, &txpool_proto.TransactionsRequest{})
	require.NoError(t, err)
	_, err = c.All(ctx, &txpool_proto.AllRequest{})
	require.NoError(t, err)
	_, err = c.Pending(ctx, &emptypb.Empty{})
	require.NoError(t, err)
	_, err = c.Status(ctx, &txpool_proto.StatusRequest{})
	require.NoError(t, err)
	_, err = c.Nonce(ctx, &txpool_proto.NonceRequest{})
	require.NoError(t, err)

	fake.err = errors.New("boom")
	_, err = c.Version(ctx, &emptypb.Empty{})
	require.Error(t, err)
}

func TestTxPoolClientOnAddStream(t *testing.T) {
	ctx := context.Background()
	fake := &fakeTxpoolServer{onAddEvents: []*txpool_proto.OnAddReply{{RplTxs: [][]byte{{1}}}}}
	c := NewTxPoolClient(fake)

	stream, err := c.OnAdd(ctx, &txpool_proto.OnAddRequest{})
	require.NoError(t, err)
	require.NotNil(t, stream.(*TxPoolOnAddC).Context())

	ev, err := stream.Recv()
	require.NoError(t, err)
	require.Equal(t, [][]byte{{1}}, ev.RplTxs)

	_, err = stream.Recv()
	require.ErrorIs(t, err, io.EOF)
}

func TestTxPoolClientOnAddStreamError(t *testing.T) {
	ctx := context.Background()
	fake := &fakeTxpoolServer{onAddErr: errors.New("stream boom")}
	c := NewTxPoolClient(fake)

	stream, err := c.OnAdd(ctx, &txpool_proto.OnAddRequest{})
	require.NoError(t, err)

	_, err = stream.Recv()
	require.Error(t, err)
	require.NotErrorIs(t, err, io.EOF)
}
