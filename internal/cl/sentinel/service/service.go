// Copyright 2024-2026 The N42 Authors
// This file is part of the N42 library.
//
// Ported from erigon cl/sentinel/service. Adaptations (#34): import paths
// rewritten; the H256 wrapper conversion is dropped — sentinelproto.Status
// roots are already common.Hash, so SetStatus uses req.FinalizedRoot/HeadRoot
// directly (no gointerfaces).

//go:build n42el

package service

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/golang/snappy"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/n42blockchain/N42/internal/cl/cltypes"
	"github.com/n42blockchain/N42/internal/cl/sentinel"
	"github.com/n42blockchain/N42/internal/cl/sentinel/httpreqresp"
	"github.com/n42blockchain/N42/internal/cl/utils"
	"github.com/n42blockchain/N42/internal/cl/depshim/sentinelproto"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

const gracePeerCount = 32

var _ sentinelproto.SentinelServer = (*SentinelServer)(nil)

type SentinelServer struct {
	sentinelproto.UnimplementedSentinelServer

	ctx      context.Context
	sentinel *sentinel.Sentinel

	logger log.Logger
}

func NewSentinelServer(ctx context.Context, sentinel *sentinel.Sentinel, logger log.Logger) *SentinelServer {
	return &SentinelServer{
		sentinel: sentinel,
		ctx:      ctx,
		logger:   logger,
	}
}

func (s *SentinelServer) BanPeer(_ context.Context, p *sentinelproto.Peer) (*sentinelproto.EmptyMessage, error) {
	active, _, _ := s.sentinel.GetPeersCount()
	if active < gracePeerCount {
		return &sentinelproto.EmptyMessage{}, nil
	}

	var pid peer.ID
	if err := pid.UnmarshalText([]byte(p.Pid)); err != nil {
		return nil, err
	}
	s.sentinel.Peers().SetBanStatus(pid, true)
	s.sentinel.Host().Peerstore().RemovePeer(pid)
	s.sentinel.Host().Network().ClosePeer(pid)
	return &sentinelproto.EmptyMessage{}, nil
}

func (s *SentinelServer) PublishGossip(_ context.Context, msg *sentinelproto.GossipData) (*sentinelproto.EmptyMessage, error) {
	panic("do not call this")
}

func (s *SentinelServer) SubscribeGossip(data *sentinelproto.SubscriptionData, stream sentinelproto.Sentinel_SubscribeGossipServer) error {
	panic("do not call this")
}

func (s *SentinelServer) requestPeer(ctx context.Context, pid peer.ID, req *sentinelproto.RequestData) (*sentinelproto.ResponseData, error) {
	// prepare the http request
	httpReq, err := http.NewRequest("GET", "http://service.internal/", bytes.NewBuffer(req.Data))
	if err != nil {
		return nil, err
	}

	activePeers, _, _ := s.sentinel.GetPeersCount()

	shouldBanOnFail := activePeers >= int(s.sentinel.Config().MaxPeerCount)
	// set the peer and topic we are requesting
	httpReq.Header.Set("REQRESP-PEER-ID", pid.String())
	httpReq.Header.Set("REQRESP-TOPIC", req.Topic)
	resp, err := httpreqresp.Do(s.sentinel.ReqRespHandler(), httpReq)
	if err != nil {
		// we remove, but dont ban the peer if we fail.
		return nil, err
	}
	defer resp.Body.Close()
	// some standard http error code parsing
	if resp.StatusCode < 200 || resp.StatusCode > 399 {
		errBody, _ := io.ReadAll(resp.Body)
		errorMessage := fmt.Errorf("SentinelHttp: %s", string(errBody))
		if shouldBanOnFail {
			s.sentinel.Peers().RemovePeer(pid)
			s.sentinel.Host().Peerstore().RemovePeer(pid)
			s.sentinel.Host().Network().ClosePeer(pid)
		}
		return nil, errorMessage
	}
	// we should never get an invalid response to this. our responder should always set it on non-error response
	code, err := strconv.Atoi(resp.Header.Get("REQRESP-RESPONSE-CODE"))
	if err != nil {
		return nil, err
	}
	// known error codes, just remove the peer
	responseCode := ResponseCode(code)
	if !responseCode.Success() {
		if shouldBanOnFail {
			s.sentinel.Peers().RemovePeer(pid)
			s.sentinel.Host().Peerstore().RemovePeer(pid)
			s.sentinel.Host().Network().ClosePeer(pid)
		}
		return nil, fmt.Errorf("peer error code: %d (%s). Error message: %s", code, responseCode.String(), responseCode.ErrorMessage(resp))
	}

	// read the body from the response
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	ans := &sentinelproto.ResponseData{
		Data:  data,
		Error: !responseCode.Success(),
		Peer: &sentinelproto.Peer{
			Pid: pid.String(),
		},
	}
	return ans, nil
}

func (s *SentinelServer) SendRequest(ctx context.Context, req *sentinelproto.RequestData) (*sentinelproto.ResponseData, error) {
	peer, done, err := s.sentinel.Peers().Request()
	if err != nil {
		return nil, err
	}
	defer done()
	pid := peer.Id()

	resp, err := s.requestPeer(ctx, pid, req)
	if err != nil {
		if strings.Contains(err.Error(), "protocols not supported") {
			s.sentinel.Peers().RemovePeer(pid)
			s.sentinel.Host().Peerstore().RemovePeer(pid)
			s.sentinel.Host().Network().ClosePeer(pid)
			s.sentinel.Peers().SetBanStatus(pid, true)
		}
		s.logger.Trace("[sentinel] peer gave us bad data", "peer", pid, "err", err, "topic", req.Topic)
		return nil, err
	}
	return resp, nil
}

func (s *SentinelServer) SendPeerRequest(ctx context.Context, reqWithPeer *sentinelproto.RequestDataWithPeer) (*sentinelproto.ResponseData, error) {
	pid, err := peer.Decode(reqWithPeer.Pid)
	if err != nil {
		return nil, err
	}
	req := &sentinelproto.RequestData{
		Data:  reqWithPeer.Data,
		Topic: reqWithPeer.Topic,
	}
	resp, err := s.requestPeer(ctx, pid, req)
	if err != nil {
		if strings.Contains(err.Error(), "protocols not supported") {
			s.sentinel.Peers().RemovePeer(pid)
			s.sentinel.Host().Peerstore().RemovePeer(pid)
			s.sentinel.Host().Network().ClosePeer(pid)
			s.sentinel.Peers().SetBanStatus(pid, true)
		}
		s.logger.Trace("[sentinel] peer gave us bad data", "peer", pid, "err", err, "topic", req.Topic)
		return nil, err
	}
	return resp, nil
}

func (s *SentinelServer) Identity(ctx context.Context, in *sentinelproto.EmptyMessage) (*sentinelproto.IdentityResponse, error) {
	pid, enr, p2pAddresses, discoveryAddresses, metadata := s.sentinel.Identity()
	return &sentinelproto.IdentityResponse{
		Pid:                pid,
		Enr:                enr,
		P2PAddresses:       p2pAddresses,
		DiscoveryAddresses: discoveryAddresses,
		Metadata: &sentinelproto.Metadata{
			Seq:      metadata.SeqNumber,
			Attnets:  fmt.Sprintf("%x", metadata.Attnets),
			Syncnets: fmt.Sprintf("%x", *metadata.Syncnets),
		},
	}, nil
}

func (s *SentinelServer) SetStatus(_ context.Context, req *sentinelproto.Status) (*sentinelproto.EmptyMessage, error) {
	// Status roots are common.Hash in the N42 sentinelproto shim — no H256 conversion.
	s.sentinel.SetStatus(&cltypes.Status{
		ForkDigest:     utils.Uint32ToBytes4(req.ForkDigest),
		FinalizedRoot:  req.FinalizedRoot,
		HeadRoot:       req.HeadRoot,
		FinalizedEpoch: req.FinalizedEpoch,
		HeadSlot:       req.HeadSlot,
	})
	return &sentinelproto.EmptyMessage{}, nil
}

func (s *SentinelServer) GetPeers(_ context.Context, _ *sentinelproto.EmptyMessage) (*sentinelproto.PeerCount, error) {
	count, connected, disconnected := s.sentinel.GetPeersCount()
	return &sentinelproto.PeerCount{
		Active:       uint64(count),
		Connected:    uint64(connected),
		Disconnected: uint64(disconnected),
	}, nil
}

func (s *SentinelServer) PeersInfo(ctx context.Context, r *sentinelproto.PeersInfoRequest) (*sentinelproto.PeersInfoResponse, error) {
	peersInfos := s.sentinel.GetPeersInfos()
	if r.Direction == nil && r.State == nil {
		return peersInfos, nil
	}
	filtered := &sentinelproto.PeersInfoResponse{
		Peers: make([]*sentinelproto.Peer, 0, len(peersInfos.Peers)),
	}
	for _, peer := range peersInfos.Peers {
		if r.Direction != nil && peer.Direction != *r.Direction {
			continue
		}
		if r.State != nil && peer.State != *r.State {
			continue
		}
		filtered.Peers = append(filtered.Peers, peer)
	}
	return filtered, nil
}

func (s *SentinelServer) SetSubscribeExpiry(ctx context.Context, expiryReq *sentinelproto.RequestSubscribeExpiry) (*sentinelproto.EmptyMessage, error) {
	panic("do not call this")
}

type ResponseCode int

func (r ResponseCode) String() string {
	switch r {
	case 0:
		return "success"
	case 1:
		return "invalid request"
	case 2:
		return "server error"
	case 3:
		return "resource unavailable"
	}
	return "unknown"
}

func (r ResponseCode) Success() bool {
	return r == 0
}

func (r ResponseCode) ErrorMessage(resp *http.Response) string {
	if r == 0 || r == 1 {
		return ""
	}
	// Error response bodies are Snappy-compressed per the ETH2 req/resp spec.
	rawReader := bufio.NewReader(resp.Body)
	// Read and discard the varint length prefix (uncompressed length).
	for {
		b, err := rawReader.ReadByte()
		if err != nil {
			remaining, _ := io.ReadAll(rawReader)
			return string(remaining)
		}
		if b&0x80 == 0 {
			break // last byte of varint
		}
	}
	sr := snappy.NewReader(rawReader)
	decoded, err := io.ReadAll(sr)
	if err != nil {
		remaining, _ := io.ReadAll(rawReader)
		return string(remaining)
	}
	return string(decoded)
}
