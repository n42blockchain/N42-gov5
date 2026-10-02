package direct

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/n42blockchain/N42/lib/gointerfaces/sentinel"
	"github.com/stretchr/testify/require"
)

type fakeSentinelServer struct {
	sentinel.UnimplementedSentinelServer
	err          error
	gossipEvents []*sentinel.GossipData
	gossipErr    error
}

func (f *fakeSentinelServer) SendRequest(ctx context.Context, in *sentinel.RequestData) (*sentinel.ResponseData, error) {
	return &sentinel.ResponseData{}, f.err
}
func (f *fakeSentinelServer) SetStatus(ctx context.Context, in *sentinel.Status) (*sentinel.EmptyMessage, error) {
	return &sentinel.EmptyMessage{}, f.err
}
func (f *fakeSentinelServer) GetPeers(ctx context.Context, in *sentinel.EmptyMessage) (*sentinel.PeerCount, error) {
	return &sentinel.PeerCount{}, f.err
}
func (f *fakeSentinelServer) BanPeer(ctx context.Context, in *sentinel.Peer) (*sentinel.EmptyMessage, error) {
	return &sentinel.EmptyMessage{}, f.err
}
func (f *fakeSentinelServer) UnbanPeer(ctx context.Context, in *sentinel.Peer) (*sentinel.EmptyMessage, error) {
	return &sentinel.EmptyMessage{}, f.err
}
func (f *fakeSentinelServer) RewardPeer(ctx context.Context, in *sentinel.Peer) (*sentinel.EmptyMessage, error) {
	return &sentinel.EmptyMessage{}, f.err
}
func (f *fakeSentinelServer) PenalizePeer(ctx context.Context, in *sentinel.Peer) (*sentinel.EmptyMessage, error) {
	return &sentinel.EmptyMessage{}, f.err
}
func (f *fakeSentinelServer) PublishGossip(ctx context.Context, in *sentinel.GossipData) (*sentinel.EmptyMessage, error) {
	return &sentinel.EmptyMessage{}, f.err
}
func (f *fakeSentinelServer) Identity(ctx context.Context, in *sentinel.EmptyMessage) (*sentinel.IdentityResponse, error) {
	return &sentinel.IdentityResponse{}, f.err
}
func (f *fakeSentinelServer) PeersInfo(ctx context.Context, in *sentinel.PeersInfoRequest) (*sentinel.PeersInfoResponse, error) {
	return &sentinel.PeersInfoResponse{}, f.err
}
func (f *fakeSentinelServer) SetSubscribeExpiry(ctx context.Context, in *sentinel.RequestSubscribeExpiry) (*sentinel.EmptyMessage, error) {
	return &sentinel.EmptyMessage{}, f.err
}
func (f *fakeSentinelServer) SubscribeGossip(in *sentinel.SubscriptionData, srv sentinel.Sentinel_SubscribeGossipServer) error {
	for _, e := range f.gossipEvents {
		if err := srv.Send(e); err != nil {
			return err
		}
	}
	return f.gossipErr
}

func TestSentinelClientDirectForwardsAllUnaryMethods(t *testing.T) {
	ctx := context.Background()
	fake := &fakeSentinelServer{}
	c := NewSentinelClientDirect(fake)

	_, err := c.SendRequest(ctx, &sentinel.RequestData{})
	require.NoError(t, err)
	_, err = c.SetStatus(ctx, &sentinel.Status{})
	require.NoError(t, err)
	_, err = c.GetPeers(ctx, &sentinel.EmptyMessage{})
	require.NoError(t, err)
	_, err = c.BanPeer(ctx, &sentinel.Peer{})
	require.NoError(t, err)
	_, err = c.UnbanPeer(ctx, &sentinel.Peer{})
	require.NoError(t, err)
	_, err = c.RewardPeer(ctx, &sentinel.Peer{})
	require.NoError(t, err)
	_, err = c.PenalizePeer(ctx, &sentinel.Peer{})
	require.NoError(t, err)
	_, err = c.PublishGossip(ctx, &sentinel.GossipData{})
	require.NoError(t, err)
	_, err = c.Identity(ctx, &sentinel.EmptyMessage{})
	require.NoError(t, err)
	_, err = c.PeersInfo(ctx, &sentinel.PeersInfoRequest{})
	require.NoError(t, err)
	_, err = c.SetSubscribeExpiry(ctx, &sentinel.RequestSubscribeExpiry{})
	require.NoError(t, err)

	fake.err = errors.New("boom")
	_, err = c.SendRequest(ctx, &sentinel.RequestData{})
	require.Error(t, err)
}

func TestSentinelClientDirectSubscribeGossipStream(t *testing.T) {
	ctx := context.Background()
	fake := &fakeSentinelServer{gossipEvents: []*sentinel.GossipData{{Data: []byte{1}}}}
	c := NewSentinelClientDirect(fake)

	stream, err := c.SubscribeGossip(ctx, &sentinel.SubscriptionData{})
	require.NoError(t, err)
	require.NotNil(t, stream.(*SentinelSubscribeGossipC).Context())

	ev, err := stream.Recv()
	require.NoError(t, err)
	require.Equal(t, []byte{1}, ev.Data)

	_, err = stream.Recv()
	require.ErrorIs(t, err, io.EOF)
}

func TestSentinelClientDirectSubscribeGossipStreamError(t *testing.T) {
	ctx := context.Background()
	fake := &fakeSentinelServer{gossipErr: errors.New("boom")}
	c := NewSentinelClientDirect(fake)

	stream, err := c.SubscribeGossip(ctx, &sentinel.SubscriptionData{})
	require.NoError(t, err)
	_, err = stream.Recv()
	require.Error(t, err)
	require.NotErrorIs(t, err, io.EOF)
}
