package direct

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/n42blockchain/N42/lib/gointerfaces/sentry"
	"github.com/n42blockchain/N42/lib/gointerfaces/types"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"
)

// fakeSentryClient implements sentry.SentryClient for exercising
// SentryClientRemote without a real gRPC connection.
type fakeSentryClient struct {
	handshakeReply  *sentry.HandShakeReply
	handshakeErr    error
	messagesIn      *sentry.MessagesRequest
	peerCountCalled bool
}

func (f *fakeSentryClient) SetStatus(ctx context.Context, in *sentry.StatusData, opts ...grpc.CallOption) (*sentry.SetStatusReply, error) {
	return &sentry.SetStatusReply{}, nil
}
func (f *fakeSentryClient) PenalizePeer(ctx context.Context, in *sentry.PenalizePeerRequest, opts ...grpc.CallOption) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}
func (f *fakeSentryClient) PeerMinBlock(ctx context.Context, in *sentry.PeerMinBlockRequest, opts ...grpc.CallOption) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}
func (f *fakeSentryClient) HandShake(ctx context.Context, in *emptypb.Empty, opts ...grpc.CallOption) (*sentry.HandShakeReply, error) {
	return f.handshakeReply, f.handshakeErr
}
func (f *fakeSentryClient) SendMessageByMinBlock(ctx context.Context, in *sentry.SendMessageByMinBlockRequest, opts ...grpc.CallOption) (*sentry.SentPeers, error) {
	return &sentry.SentPeers{}, nil
}
func (f *fakeSentryClient) SendMessageById(ctx context.Context, in *sentry.SendMessageByIdRequest, opts ...grpc.CallOption) (*sentry.SentPeers, error) {
	return &sentry.SentPeers{}, nil
}
func (f *fakeSentryClient) SendMessageToRandomPeers(ctx context.Context, in *sentry.SendMessageToRandomPeersRequest, opts ...grpc.CallOption) (*sentry.SentPeers, error) {
	return &sentry.SentPeers{}, nil
}
func (f *fakeSentryClient) SendMessageToAll(ctx context.Context, in *sentry.OutboundMessageData, opts ...grpc.CallOption) (*sentry.SentPeers, error) {
	return &sentry.SentPeers{}, nil
}
func (f *fakeSentryClient) Messages(ctx context.Context, in *sentry.MessagesRequest, opts ...grpc.CallOption) (sentry.Sentry_MessagesClient, error) {
	f.messagesIn = in
	return nil, nil
}
func (f *fakeSentryClient) Peers(ctx context.Context, in *emptypb.Empty, opts ...grpc.CallOption) (*sentry.PeersReply, error) {
	return &sentry.PeersReply{}, nil
}
func (f *fakeSentryClient) PeerCount(ctx context.Context, in *sentry.PeerCountRequest, opts ...grpc.CallOption) (*sentry.PeerCountReply, error) {
	f.peerCountCalled = true
	return &sentry.PeerCountReply{}, nil
}
func (f *fakeSentryClient) PeerById(ctx context.Context, in *sentry.PeerByIdRequest, opts ...grpc.CallOption) (*sentry.PeerByIdReply, error) {
	return &sentry.PeerByIdReply{}, nil
}
func (f *fakeSentryClient) PeerEvents(ctx context.Context, in *sentry.PeerEventsRequest, opts ...grpc.CallOption) (sentry.Sentry_PeerEventsClient, error) {
	return nil, nil
}
func (f *fakeSentryClient) AddPeer(ctx context.Context, in *sentry.AddPeerRequest, opts ...grpc.CallOption) (*sentry.AddPeerReply, error) {
	return &sentry.AddPeerReply{}, nil
}
func (f *fakeSentryClient) NodeInfo(ctx context.Context, in *emptypb.Empty, opts ...grpc.CallOption) (*types.NodeInfoReply, error) {
	return &types.NodeInfoReply{}, nil
}

func TestSentryClientRemoteHandshakeSetsProtocolAndReady(t *testing.T) {
	ctx := context.Background()
	fake := &fakeSentryClient{handshakeReply: &sentry.HandShakeReply{Protocol: sentry.Protocol_ETH67}}
	c := NewSentryClientRemote(fake)

	require.False(t, c.Ready())
	require.Zero(t, c.Protocol())

	reply, err := c.HandShake(ctx, &emptypb.Empty{})
	require.NoError(t, err)
	require.Equal(t, sentry.Protocol_ETH67, reply.Protocol)
	require.True(t, c.Ready())
	require.EqualValues(t, ETH67, c.Protocol())

	c.MarkDisconnected()
	require.False(t, c.Ready())
}

func TestSentryClientRemoteHandshakeAllProtocols(t *testing.T) {
	cases := []struct {
		proto sentry.Protocol
		want  uint
	}{
		{sentry.Protocol_ETH65, ETH65},
		{sentry.Protocol_ETH66, ETH66},
		{sentry.Protocol_ETH67, ETH67},
		{sentry.Protocol_ETH68, ETH68},
		{sentry.Protocol_ETH69, ETH69},
	}
	for _, tc := range cases {
		fake := &fakeSentryClient{handshakeReply: &sentry.HandShakeReply{Protocol: tc.proto}}
		c := NewSentryClientRemote(fake)
		_, err := c.HandShake(context.Background(), &emptypb.Empty{})
		require.NoError(t, err)
		require.EqualValues(t, tc.want, c.Protocol())
	}
}

func TestSentryClientRemoteHandshakeUnknownProtocolErrors(t *testing.T) {
	fake := &fakeSentryClient{handshakeReply: &sentry.HandShakeReply{Protocol: sentry.Protocol(999)}}
	c := NewSentryClientRemote(fake)
	_, err := c.HandShake(context.Background(), &emptypb.Empty{})
	require.Error(t, err)
	require.False(t, c.Ready())
}

func TestSentryClientRemoteHandshakeErrorPassthrough(t *testing.T) {
	fake := &fakeSentryClient{handshakeErr: errors.New("boom")}
	c := NewSentryClientRemote(fake)
	_, err := c.HandShake(context.Background(), &emptypb.Empty{})
	require.Error(t, err)
}

func TestSentryClientRemoteSetStatusAndPeerCount(t *testing.T) {
	fake := &fakeSentryClient{}
	c := NewSentryClientRemote(fake)
	_, err := c.SetStatus(context.Background(), &sentry.StatusData{})
	require.NoError(t, err)
	_, err = c.PeerCount(context.Background(), &sentry.PeerCountRequest{})
	require.NoError(t, err)
	require.True(t, fake.peerCountCalled)
}

func TestSentryClientRemoteMessagesFiltersIds(t *testing.T) {
	fake := &fakeSentryClient{handshakeReply: &sentry.HandShakeReply{Protocol: sentry.Protocol_ETH66}}
	c := NewSentryClientRemote(fake)
	_, err := c.HandShake(context.Background(), &emptypb.Empty{})
	require.NoError(t, err)

	_, err = c.Messages(context.Background(), &sentry.MessagesRequest{
		Ids: []sentry.MessageId{sentry.MessageId_BLOCK_HEADERS_66, sentry.MessageId_BLOCK_HEADERS_65},
	})
	require.NoError(t, err)
	// ETH66 protocol set doesn't contain the ETH65-only id, so it's filtered out.
	require.Equal(t, []sentry.MessageId{sentry.MessageId_BLOCK_HEADERS_66}, fake.messagesIn.Ids)
}

// --- SentryClientDirect ---

type fakeSentryServer struct {
	sentry.UnimplementedSentryServer
	err              error
	messagesEvents   []*sentry.InboundMessage
	messagesErr      error
	peerEventsEvents []*sentry.PeerEvent
	peerEventsErr    error
}

func (f *fakeSentryServer) PenalizePeer(ctx context.Context, in *sentry.PenalizePeerRequest) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, f.err
}
func (f *fakeSentryServer) PeerMinBlock(ctx context.Context, in *sentry.PeerMinBlockRequest) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, f.err
}
func (f *fakeSentryServer) SendMessageByMinBlock(ctx context.Context, in *sentry.SendMessageByMinBlockRequest) (*sentry.SentPeers, error) {
	return &sentry.SentPeers{}, f.err
}
func (f *fakeSentryServer) SendMessageById(ctx context.Context, in *sentry.SendMessageByIdRequest) (*sentry.SentPeers, error) {
	return &sentry.SentPeers{}, f.err
}
func (f *fakeSentryServer) SendMessageToRandomPeers(ctx context.Context, in *sentry.SendMessageToRandomPeersRequest) (*sentry.SentPeers, error) {
	return &sentry.SentPeers{}, f.err
}
func (f *fakeSentryServer) SendMessageToAll(ctx context.Context, in *sentry.OutboundMessageData) (*sentry.SentPeers, error) {
	return &sentry.SentPeers{}, f.err
}
func (f *fakeSentryServer) HandShake(ctx context.Context, in *emptypb.Empty) (*sentry.HandShakeReply, error) {
	return &sentry.HandShakeReply{}, f.err
}
func (f *fakeSentryServer) SetStatus(ctx context.Context, in *sentry.StatusData) (*sentry.SetStatusReply, error) {
	return &sentry.SetStatusReply{}, f.err
}
func (f *fakeSentryServer) Peers(ctx context.Context, in *emptypb.Empty) (*sentry.PeersReply, error) {
	return &sentry.PeersReply{}, f.err
}
func (f *fakeSentryServer) PeerCount(ctx context.Context, in *sentry.PeerCountRequest) (*sentry.PeerCountReply, error) {
	return &sentry.PeerCountReply{}, f.err
}
func (f *fakeSentryServer) PeerById(ctx context.Context, in *sentry.PeerByIdRequest) (*sentry.PeerByIdReply, error) {
	return &sentry.PeerByIdReply{}, f.err
}
func (f *fakeSentryServer) AddPeer(ctx context.Context, in *sentry.AddPeerRequest) (*sentry.AddPeerReply, error) {
	return &sentry.AddPeerReply{}, f.err
}
func (f *fakeSentryServer) NodeInfo(ctx context.Context, in *emptypb.Empty) (*types.NodeInfoReply, error) {
	return &types.NodeInfoReply{}, f.err
}
func (f *fakeSentryServer) Messages(in *sentry.MessagesRequest, srv sentry.Sentry_MessagesServer) error {
	for _, e := range f.messagesEvents {
		if err := srv.Send(e); err != nil {
			return err
		}
	}
	return f.messagesErr
}
func (f *fakeSentryServer) PeerEvents(in *sentry.PeerEventsRequest, srv sentry.Sentry_PeerEventsServer) error {
	for _, e := range f.peerEventsEvents {
		if err := srv.Send(e); err != nil {
			return err
		}
	}
	return f.peerEventsErr
}

func TestSentryClientDirectForwardsAllUnaryMethods(t *testing.T) {
	ctx := context.Background()
	fake := &fakeSentryServer{}
	c := NewSentryClientDirect(ETH67, fake)

	require.EqualValues(t, ETH67, c.Protocol())
	require.True(t, c.Ready())
	c.MarkDisconnected() // no-op, must not panic

	_, err := c.PenalizePeer(ctx, &sentry.PenalizePeerRequest{})
	require.NoError(t, err)
	_, err = c.PeerMinBlock(ctx, &sentry.PeerMinBlockRequest{})
	require.NoError(t, err)
	_, err = c.SendMessageByMinBlock(ctx, &sentry.SendMessageByMinBlockRequest{})
	require.NoError(t, err)
	_, err = c.SendMessageById(ctx, &sentry.SendMessageByIdRequest{})
	require.NoError(t, err)
	_, err = c.SendMessageToRandomPeers(ctx, &sentry.SendMessageToRandomPeersRequest{})
	require.NoError(t, err)
	_, err = c.SendMessageToAll(ctx, &sentry.OutboundMessageData{})
	require.NoError(t, err)
	_, err = c.HandShake(ctx, &emptypb.Empty{})
	require.NoError(t, err)
	_, err = c.SetStatus(ctx, &sentry.StatusData{})
	require.NoError(t, err)
	_, err = c.Peers(ctx, &emptypb.Empty{})
	require.NoError(t, err)
	_, err = c.PeerCount(ctx, &sentry.PeerCountRequest{})
	require.NoError(t, err)
	_, err = c.PeerById(ctx, &sentry.PeerByIdRequest{})
	require.NoError(t, err)
	_, err = c.AddPeer(ctx, &sentry.AddPeerRequest{})
	require.NoError(t, err)
	_, err = c.NodeInfo(ctx, &emptypb.Empty{})
	require.NoError(t, err)

	fake.err = errors.New("boom")
	_, err = c.PenalizePeer(ctx, &sentry.PenalizePeerRequest{})
	require.Error(t, err)
}

func TestSentryClientDirectMessagesStreamFiltersAndDelivers(t *testing.T) {
	ctx := context.Background()
	fake := &fakeSentryServer{messagesEvents: []*sentry.InboundMessage{{Data: []byte{1}}}}
	c := NewSentryClientDirect(ETH66, fake)

	stream, err := c.Messages(ctx, &sentry.MessagesRequest{
		Ids: []sentry.MessageId{sentry.MessageId_BLOCK_HEADERS_66, sentry.MessageId_BLOCK_HEADERS_65},
	})
	require.NoError(t, err)
	require.NotNil(t, stream.(*SentryMessagesStreamC).Context())

	msg, err := stream.Recv()
	require.NoError(t, err)
	require.Equal(t, []byte{1}, msg.Data)

	_, err = stream.Recv()
	require.ErrorIs(t, err, io.EOF)

	var out sentry.InboundMessage
	// RecvMsg on a drained+closed channel returns io.EOF
	require.ErrorIs(t, stream.(*SentryMessagesStreamC).RecvMsg(&out), io.EOF)
}

func TestSentryClientDirectMessagesStreamRecvMsg(t *testing.T) {
	ctx := context.Background()
	fake := &fakeSentryServer{messagesEvents: []*sentry.InboundMessage{{Data: []byte{7}}}}
	c := NewSentryClientDirect(ETH66, fake)

	stream, err := c.Messages(ctx, &sentry.MessagesRequest{})
	require.NoError(t, err)

	var out sentry.InboundMessage
	require.NoError(t, stream.(*SentryMessagesStreamC).RecvMsg(&out))
	require.Equal(t, []byte{7}, out.Data)
}

func TestSentryClientDirectMessagesStreamError(t *testing.T) {
	ctx := context.Background()
	fake := &fakeSentryServer{messagesErr: errors.New("boom")}
	c := NewSentryClientDirect(ETH66, fake)

	stream, err := c.Messages(ctx, &sentry.MessagesRequest{})
	require.NoError(t, err)
	_, err = stream.Recv()
	require.Error(t, err)
	require.NotErrorIs(t, err, io.EOF)
}

func TestSentryClientDirectPeerEventsStream(t *testing.T) {
	ctx := context.Background()
	fake := &fakeSentryServer{peerEventsEvents: []*sentry.PeerEvent{{PeerId: &types.H512{}}}}
	c := NewSentryClientDirect(ETH66, fake)

	stream, err := c.PeerEvents(ctx, &sentry.PeerEventsRequest{})
	require.NoError(t, err)
	require.NotNil(t, stream.(*SentryPeersStreamC).Context())

	ev, err := stream.Recv()
	require.NoError(t, err)
	require.NotNil(t, ev.PeerId)

	_, err = stream.Recv()
	require.ErrorIs(t, err, io.EOF)
}

func TestSentryClientDirectPeerEventsStreamRecvMsg(t *testing.T) {
	ctx := context.Background()
	fake := &fakeSentryServer{peerEventsEvents: []*sentry.PeerEvent{{PeerId: &types.H512{}}}}
	c := NewSentryClientDirect(ETH66, fake)

	stream, err := c.PeerEvents(ctx, &sentry.PeerEventsRequest{})
	require.NoError(t, err)
	var out sentry.PeerEvent
	require.NoError(t, stream.(*SentryPeersStreamC).RecvMsg(&out))
}

func TestSentryClientDirectPeerEventsStreamError(t *testing.T) {
	ctx := context.Background()
	fake := &fakeSentryServer{peerEventsErr: errors.New("boom")}
	c := NewSentryClientDirect(ETH66, fake)

	stream, err := c.PeerEvents(ctx, &sentry.PeerEventsRequest{})
	require.NoError(t, err)
	_, err = stream.Recv()
	require.Error(t, err)
	require.NotErrorIs(t, err, io.EOF)
}

func TestFilterIdsUnknownProtocolReturnsNil(t *testing.T) {
	filtered := filterIds([]sentry.MessageId{sentry.MessageId_BLOCK_HEADERS_66}, 12345)
	require.Nil(t, filtered)
}
