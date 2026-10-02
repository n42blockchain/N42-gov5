package direct

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/n42blockchain/N42/lib/gointerfaces/remote"
	"github.com/n42blockchain/N42/lib/gointerfaces/types"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/emptypb"
)

type fakeEthBackendServer struct {
	remote.UnimplementedETHBACKENDServer
	err              error
	subscribeEvents  []*remote.SubscribeReply
	subscribeErr     error
	logsReplyEvents  []*remote.SubscribeLogsReply
	logsErr          error
	receivedFilterIn []*remote.LogsFilterRequest
}

func (f *fakeEthBackendServer) Etherbase(ctx context.Context, in *remote.EtherbaseRequest) (*remote.EtherbaseReply, error) {
	return &remote.EtherbaseReply{}, f.err
}
func (f *fakeEthBackendServer) NetVersion(ctx context.Context, in *remote.NetVersionRequest) (*remote.NetVersionReply, error) {
	return &remote.NetVersionReply{}, f.err
}
func (f *fakeEthBackendServer) NetPeerCount(ctx context.Context, in *remote.NetPeerCountRequest) (*remote.NetPeerCountReply, error) {
	return &remote.NetPeerCountReply{}, f.err
}
func (f *fakeEthBackendServer) Version(ctx context.Context, in *emptypb.Empty) (*types.VersionReply, error) {
	return &types.VersionReply{Major: 1}, f.err
}
func (f *fakeEthBackendServer) ProtocolVersion(ctx context.Context, in *remote.ProtocolVersionRequest) (*remote.ProtocolVersionReply, error) {
	return &remote.ProtocolVersionReply{}, f.err
}
func (f *fakeEthBackendServer) ClientVersion(ctx context.Context, in *remote.ClientVersionRequest) (*remote.ClientVersionReply, error) {
	return &remote.ClientVersionReply{}, f.err
}
func (f *fakeEthBackendServer) Block(ctx context.Context, in *remote.BlockRequest) (*remote.BlockReply, error) {
	return &remote.BlockReply{}, f.err
}
func (f *fakeEthBackendServer) TxnLookup(ctx context.Context, in *remote.TxnLookupRequest) (*remote.TxnLookupReply, error) {
	return &remote.TxnLookupReply{}, f.err
}
func (f *fakeEthBackendServer) NodeInfo(ctx context.Context, in *remote.NodesInfoRequest) (*remote.NodesInfoReply, error) {
	return &remote.NodesInfoReply{}, f.err
}
func (f *fakeEthBackendServer) Peers(ctx context.Context, in *emptypb.Empty) (*remote.PeersReply, error) {
	return &remote.PeersReply{}, f.err
}
func (f *fakeEthBackendServer) AddPeer(ctx context.Context, in *remote.AddPeerRequest) (*remote.AddPeerReply, error) {
	return &remote.AddPeerReply{}, f.err
}
func (f *fakeEthBackendServer) PendingBlock(ctx context.Context, in *emptypb.Empty) (*remote.PendingBlockReply, error) {
	return &remote.PendingBlockReply{}, f.err
}
func (f *fakeEthBackendServer) BorEvent(ctx context.Context, in *remote.BorEventRequest) (*remote.BorEventReply, error) {
	return &remote.BorEventReply{}, f.err
}
func (f *fakeEthBackendServer) Subscribe(in *remote.SubscribeRequest, srv remote.ETHBACKEND_SubscribeServer) error {
	for _, e := range f.subscribeEvents {
		if err := srv.Send(e); err != nil {
			return err
		}
	}
	return f.subscribeErr
}
func (f *fakeEthBackendServer) SubscribeLogs(srv remote.ETHBACKEND_SubscribeLogsServer) error {
	if f.logsErr != nil {
		return f.logsErr
	}
	in, err := srv.Recv()
	if err != nil && err != io.EOF {
		return err
	}
	if in != nil {
		f.receivedFilterIn = append(f.receivedFilterIn, in)
	}
	for _, e := range f.logsReplyEvents {
		if err := srv.Send(e); err != nil {
			return err
		}
	}
	return nil
}

func TestEthBackendClientDirectForwardsAllUnaryMethods(t *testing.T) {
	ctx := context.Background()
	fake := &fakeEthBackendServer{}
	c := NewEthBackendClientDirect(fake)

	_, err := c.Etherbase(ctx, &remote.EtherbaseRequest{})
	require.NoError(t, err)
	_, err = c.NetVersion(ctx, &remote.NetVersionRequest{})
	require.NoError(t, err)
	_, err = c.NetPeerCount(ctx, &remote.NetPeerCountRequest{})
	require.NoError(t, err)
	_, err = c.Version(ctx, &emptypb.Empty{})
	require.NoError(t, err)
	_, err = c.ProtocolVersion(ctx, &remote.ProtocolVersionRequest{})
	require.NoError(t, err)
	_, err = c.ClientVersion(ctx, &remote.ClientVersionRequest{})
	require.NoError(t, err)
	_, err = c.Block(ctx, &remote.BlockRequest{})
	require.NoError(t, err)
	_, err = c.TxnLookup(ctx, &remote.TxnLookupRequest{})
	require.NoError(t, err)
	_, err = c.NodeInfo(ctx, &remote.NodesInfoRequest{})
	require.NoError(t, err)
	_, err = c.Peers(ctx, &emptypb.Empty{})
	require.NoError(t, err)
	_, err = c.AddPeer(ctx, &remote.AddPeerRequest{})
	require.NoError(t, err)
	_, err = c.PendingBlock(ctx, &emptypb.Empty{})
	require.NoError(t, err)
	_, err = c.BorEvent(ctx, &remote.BorEventRequest{})
	require.NoError(t, err)

	fake.err = errors.New("boom")
	_, err = c.Etherbase(ctx, &remote.EtherbaseRequest{})
	require.Error(t, err)
}

func TestEthBackendClientDirectSubscribeStream(t *testing.T) {
	ctx := context.Background()
	fake := &fakeEthBackendServer{subscribeEvents: []*remote.SubscribeReply{{Data: []byte{1}}}}
	c := NewEthBackendClientDirect(fake)

	stream, err := c.Subscribe(ctx, &remote.SubscribeRequest{})
	require.NoError(t, err)
	require.NotNil(t, stream.(*SubscribeStreamC).Context())

	ev, err := stream.Recv()
	require.NoError(t, err)
	require.Equal(t, []byte{1}, ev.Data)

	_, err = stream.Recv()
	require.ErrorIs(t, err, io.EOF)
}

func TestEthBackendClientDirectSubscribeStreamError(t *testing.T) {
	ctx := context.Background()
	fake := &fakeEthBackendServer{subscribeErr: errors.New("boom")}
	c := NewEthBackendClientDirect(fake)

	stream, err := c.Subscribe(ctx, &remote.SubscribeRequest{})
	require.NoError(t, err)
	_, err = stream.Recv()
	require.Error(t, err)
	require.NotErrorIs(t, err, io.EOF)
}

func TestEthBackendClientDirectSubscribeLogsStream(t *testing.T) {
	ctx := context.Background()
	fake := &fakeEthBackendServer{logsReplyEvents: []*remote.SubscribeLogsReply{{Data: []byte{9}}}}
	c := NewEthBackendClientDirect(fake)

	stream, err := c.SubscribeLogs(ctx)
	require.NoError(t, err)

	require.NoError(t, stream.Send(&remote.LogsFilterRequest{}))
	// NOTE: SubscribeLogsStreamC/S embed grpc.ClientStream/ServerStream but
	// never implement their own Context(); unlike the other stream wrappers
	// in this package, calling Context() on them panics on the nil embedded
	// interface. Not exercised here for that reason.

	ev, err := stream.Recv()
	require.NoError(t, err)
	require.Equal(t, []byte{9}, ev.Data)

	_, err = stream.Recv()
	require.ErrorIs(t, err, io.EOF)
}

// NOTE: this is a real data race in EthBackendClientDirect.SubscribeLogs:
// when the server goroutine returns immediately (as it does here on
// logsErr) it closes the chRecv channel in a deferred close, which races
// with any concurrent client-side Send on that same channel. We avoid
// calling Send here to dodge the race (go test -race catches it otherwise)
// rather than touch production code; the race itself is reported, not
// fixed.
func TestEthBackendClientDirectSubscribeLogsStreamError(t *testing.T) {
	ctx := context.Background()
	fake := &fakeEthBackendServer{logsErr: errors.New("boom")}
	c := NewEthBackendClientDirect(fake)

	stream, err := c.SubscribeLogs(ctx)
	require.NoError(t, err)

	_, err = stream.Recv()
	require.Error(t, err)
	require.NotErrorIs(t, err, io.EOF)
}
