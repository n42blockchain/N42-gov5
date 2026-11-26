// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// A light libp2p host for the state-sync client (init-snapshot
// --from-peers): it dials full nodes, completes the status handshake they
// expect from an inbound peer, and exposes each as a v11.Peer.

package sync

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/p2p/security/noise"
	"github.com/libp2p/go-libp2p/p2p/transport/tcp"
	ma "github.com/multiformats/go-multiaddr"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/common/utils"
	"github.com/n42blockchain/N42/internal/p2p"
	"github.com/n42blockchain/N42/internal/p2p/encoder"
	"github.com/n42blockchain/N42/internal/sync/v11"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/proto/sync_pb"
)

// V11Peer exposes pid on h as a v11.Peer.
func V11Peer(h host.Host, pid peer.ID) *v11.Peer {
	return &v11.Peer{Name: shortPeer(pid), Open: func(ctx context.Context, proto string, req []byte) (io.ReadCloser, error) {
		s, err := openV11(ctx, h, pid, proto, req)
		if err != nil {
			return nil, err
		}
		_ = s.SetReadDeadline(time.Now().Add(respTimeout * 4))
		return s, nil
	}}
}

// DialStateSyncPeers starts a light host (TCP, noise), connects to addrs
// (multiaddrs ending in /p2p/<id>) and sends each the status a full node
// expects within 10 s of an inbound connection. The caller closes the host.
func DialStateSyncPeers(ctx context.Context, genesis types.Hash, addrs []string) (host.Host, []*v11.Peer, error) {
	h, err := libp2p.New(
		libp2p.NoListenAddrs,
		libp2p.Transport(tcp.NewTCPTransport),
		libp2p.DefaultMuxers,
		libp2p.Security(noise.ID, noise.New),
		libp2p.UserAgent("n42-state-sync"),
	)
	if err != nil {
		return nil, nil, err
	}
	enc := encoder.SszNetworkEncoder{}
	status := &sync_pb.Status{
		GenesisHash:   utils.ConvertHashToH256(genesis),
		CurrentHeight: utils.ConvertUint256IntToH256(uint256.NewInt(0)),
	}
	statusProto := protocol.ID(p2p.RPCStatusTopicV1 + enc.ProtocolSuffix())
	h.SetStreamHandler(statusProto, func(s network.Stream) {
		defer s.Close()
		_ = s.SetDeadline(time.Now().Add(10 * time.Second))
		in := &sync_pb.Status{}
		if err := enc.DecodeWithMaxLength(s, in); err != nil {
			_ = s.Reset()
			return
		}
		_, _ = s.Write([]byte{responseCodeSuccess})
		_, _ = enc.EncodeWithMaxLength(s, status)
	})
	var peers []*v11.Peer
	for _, a := range addrs {
		m, err := ma.NewMultiaddr(a)
		if err != nil {
			_ = h.Close()
			return nil, nil, fmt.Errorf("peer address %q: %w", a, err)
		}
		info, err := peer.AddrInfoFromP2pAddr(m)
		if err != nil {
			_ = h.Close()
			return nil, nil, fmt.Errorf("peer address %q: %w", a, err)
		}
		cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		err = h.Connect(cctx, *info)
		if err == nil {
			err = sendStatus(cctx, h, info.ID, statusProto, enc, status)
		}
		cancel()
		if err != nil {
			log.Warn("state sync: peer unreachable", "addr", a, "err", err)
			continue
		}
		peers = append(peers, V11Peer(h, info.ID))
	}
	if len(peers) == 0 {
		_ = h.Close()
		return nil, nil, fmt.Errorf("state sync: none of %d peers is reachable", len(addrs))
	}
	return h, peers, nil
}

func sendStatus(ctx context.Context, h host.Host, pid peer.ID, proto protocol.ID, enc encoder.SszNetworkEncoder, st *sync_pb.Status) error {
	s, err := h.NewStream(ctx, pid, proto)
	if err != nil {
		return err
	}
	defer s.Close()
	_ = s.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := enc.EncodeWithMaxLength(s, st); err != nil {
		return err
	}
	if err := s.CloseWrite(); err != nil {
		return err
	}
	code, msg, err := ReadStatusCode(s, enc)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("status refused: %s", msg)
	}
	return nil
}
