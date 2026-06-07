// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// Ported from erigon node/direct/sentinel_client.go. SentinelClientDirect
// adapts an in-process sentinelproto.SentinelServer (the cl/sentinel service)
// into a sentinelproto.SentinelClient, so Caplin's embedded EL-driving path
// (#34) wires the sentinel and cl/rpc.BeaconRpcP2P together with no gRPC wire
// hop. SubscribeGossip's stream is bridged over a buffered channel.

//go:build n42el

package direct

import (
	"context"
	"io"

	"github.com/n42blockchain/N42/internal/cl/depshim/sentinelproto"
	"google.golang.org/grpc"
)

type SentinelClientDirect struct {
	server sentinelproto.SentinelServer
}

func NewSentinelClientDirect(sentinel sentinelproto.SentinelServer) sentinelproto.SentinelClient {
	return &SentinelClientDirect{server: sentinel}
}

func (s *SentinelClientDirect) SendRequest(ctx context.Context, in *sentinelproto.RequestData, opts ...grpc.CallOption) (*sentinelproto.ResponseData, error) {
	return s.server.SendRequest(ctx, in)
}

func (s *SentinelClientDirect) SendPeerRequest(ctx context.Context, in *sentinelproto.RequestDataWithPeer, opts ...grpc.CallOption) (*sentinelproto.ResponseData, error) {
	return s.server.SendPeerRequest(ctx, in)
}

func (s *SentinelClientDirect) SetStatus(ctx context.Context, in *sentinelproto.Status, opts ...grpc.CallOption) (*sentinelproto.EmptyMessage, error) {
	return s.server.SetStatus(ctx, in)
}

func (s *SentinelClientDirect) GetPeers(ctx context.Context, in *sentinelproto.EmptyMessage, opts ...grpc.CallOption) (*sentinelproto.PeerCount, error) {
	return s.server.GetPeers(ctx, in)
}

func (s *SentinelClientDirect) BanPeer(ctx context.Context, p *sentinelproto.Peer, opts ...grpc.CallOption) (*sentinelproto.EmptyMessage, error) {
	return s.server.BanPeer(ctx, p)
}
func (s *SentinelClientDirect) UnbanPeer(ctx context.Context, p *sentinelproto.Peer, opts ...grpc.CallOption) (*sentinelproto.EmptyMessage, error) {
	return s.server.UnbanPeer(ctx, p)
}
func (s *SentinelClientDirect) RewardPeer(ctx context.Context, p *sentinelproto.Peer, opts ...grpc.CallOption) (*sentinelproto.EmptyMessage, error) {
	return s.server.RewardPeer(ctx, p)
}
func (s *SentinelClientDirect) PenalizePeer(ctx context.Context, p *sentinelproto.Peer, opts ...grpc.CallOption) (*sentinelproto.EmptyMessage, error) {
	return s.server.PenalizePeer(ctx, p)
}

func (s *SentinelClientDirect) PublishGossip(ctx context.Context, in *sentinelproto.GossipData, opts ...grpc.CallOption) (*sentinelproto.EmptyMessage, error) {
	return s.server.PublishGossip(ctx, in)
}

func (s *SentinelClientDirect) Identity(ctx context.Context, in *sentinelproto.EmptyMessage, opts ...grpc.CallOption) (*sentinelproto.IdentityResponse, error) {
	return s.server.Identity(ctx, in)
}

func (s *SentinelClientDirect) PeersInfo(ctx context.Context, in *sentinelproto.PeersInfoRequest, opts ...grpc.CallOption) (*sentinelproto.PeersInfoResponse, error) {
	return s.server.PeersInfo(ctx, in)
}

// Subscribe gossip part — the one method that has to bridge a stream.

func (s *SentinelClientDirect) SubscribeGossip(ctx context.Context, in *sentinelproto.SubscriptionData, opts ...grpc.CallOption) (sentinelproto.Sentinel_SubscribeGossipClient, error) {
	ch := make(chan *gossipReply, 1<<16)
	streamServer := &SentinelSubscribeGossipS{ch: ch, ctx: ctx}
	go func() {
		defer close(ch)
		streamServer.Err(s.server.SubscribeGossip(in, streamServer))
	}()
	return &SentinelSubscribeGossipC{ch: ch, ctx: ctx}, nil
}

func (s *SentinelClientDirect) SetSubscribeExpiry(ctx context.Context, expiryReq *sentinelproto.RequestSubscribeExpiry, opts ...grpc.CallOption) (*sentinelproto.EmptyMessage, error) {
	return s.server.SetSubscribeExpiry(ctx, expiryReq)
}

type SentinelSubscribeGossipC struct {
	ch  chan *gossipReply
	ctx context.Context
	grpc.ClientStream
}

func (c *SentinelSubscribeGossipC) Recv() (*sentinelproto.GossipData, error) {
	m, ok := <-c.ch
	if !ok || m == nil {
		return nil, io.EOF
	}
	return m.r, m.err
}
func (c *SentinelSubscribeGossipC) Context() context.Context { return c.ctx }

type SentinelSubscribeGossipS struct {
	ch  chan *gossipReply
	ctx context.Context
	grpc.ServerStream
}

type gossipReply struct {
	r   *sentinelproto.GossipData
	err error
}

func (s *SentinelSubscribeGossipS) Send(m *sentinelproto.GossipData) error {
	s.ch <- &gossipReply{r: m}
	return nil
}
func (s *SentinelSubscribeGossipS) Context() context.Context { return s.ctx }
func (s *SentinelSubscribeGossipS) Err(err error) {
	if err == nil {
		return
	}
	s.ch <- &gossipReply{err: err}
}
