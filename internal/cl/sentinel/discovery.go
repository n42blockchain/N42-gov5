// Copyright 2024-2026 The N42 Authors
// This file is part of the N42 library.
//
// Ported from erigon cl/sentinel, trimmed to the block-only B+ path (#34):
// peer discovery (listenForPeers) + connect + handshake-on-connect. The
// attestation-subnet peer-search/coverage machinery (findPeersForSubnets,
// getSubnetCoverage, pruneExcessPeers, proactiveSubnetPeerSearch) is dropped —
// block-only fork choice does not optimize per-subnet coverage; the subnet
// search hook is a no-op. proactiveSubnetPeerSearch is kept as a stub so
// sentinel.Start's `go s.proactiveSubnetPeerSearch()` is unchanged.

//go:build n42el

package sentinel

import (
	"context"
	"errors"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
	"golang.org/x/sync/semaphore"

	"github.com/n42blockchain/N42/internal/cl/clparams"
	"github.com/n42blockchain/N42/internal/cl/p2p"
	"github.com/n42blockchain/N42/internal/p2p/enode"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

const (
	goRoutinesOpeningPeerConnections = 4
)

// proactiveSubnetPeerSearch is a no-op in the block-only B+ build: attestation
// subnet coverage is not optimized (the attestation gossip subnets are
// deferred). Kept so sentinel.Start's goroutine launch is unchanged.
func (s *Sentinel) proactiveSubnetPeerSearch() {}

func (s *Sentinel) ConnectWithPeer(ctx context.Context, info peer.AddrInfo, sem *semaphore.Weighted) (err error) {
	if sem != nil {
		defer sem.Release(1)
	}
	if info.ID == s.p2p.Host().ID() {
		return nil
	}
	if s.peers.BanStatus(info.ID) {
		return errors.New("refused to connect to bad peer")
	}
	ctxWithTimeout, cancel := context.WithTimeout(ctx, clparams.MaxDialTimeout)
	defer cancel()
	if s.p2p.Host().Network().Connectedness(info.ID) == network.Connected {
		return nil
	}
	err = s.p2p.Host().Connect(ctxWithTimeout, info)
	if err != nil {
		return err
	}
	log.Trace("[caplin] Connected with peer", "peer", info.ID)
	return nil
}

// connectWithAllPeers connects with a list of addrs; only errors on multiaddr parse failure.
func (s *Sentinel) connectWithAllPeers(multiAddrs []multiaddr.Multiaddr) error {
	addrInfos, err := peer.AddrInfosFromP2pAddrs(multiAddrs...)
	if err != nil {
		return err
	}
	for _, peerInfo := range addrInfos {
		go func(peerInfo peer.AddrInfo) {
			if err := s.connectSem.Acquire(s.ctx, 1); err != nil {
				return
			}
			if err := s.ConnectWithPeer(s.ctx, peerInfo, s.connectSem); err != nil {
				log.Debug("[Sentinel] Could not connect with peer", "err", err)
			} else {
				log.Debug("[Sentinel] Connected with peer", "peer", peerInfo.ID)
			}
		}(peerInfo)
	}
	return nil
}

func (s *Sentinel) stickToPeers(peers []multiaddr.Multiaddr) {
	// connect to static peers every few minutes
	go func() {
		for {
			if err := s.connectWithAllPeers(peers); err != nil {
				log.Debug("[Sentinel] Could not connect with static peers", "err", err)
			}
			time.Sleep(3 * time.Minute)
		}
	}()
}

func (s *Sentinel) listenForPeers() {
	enodes := []*enode.Node{}
	for _, node := range s.cfg.NetworkConfig.StaticPeers {
		newNode, err := enode.Parse(enode.ValidSchemes, node)
		if err == nil {
			enodes = append(enodes, newNode)
		} else {
			log.Warn("Could not connect to static peer", "peer", node, "reason", err)
		}
	}
	log.Info("CL Sentinel static peers", "len", len(enodes))
	if s.cfg.NoDiscovery {
		return
	}
	multiAddresses := p2p.ConvertToMultiAddr(enodes)
	s.stickToPeers(multiAddresses)

	iterator := s.listener.RandomNodes()
	defer iterator.Close()
	for {
		if err := s.ctx.Err(); err != nil {
			log.Debug("Stopping Ethereum 2.0 peer discovery", "err", err)
			break
		}

		exists := iterator.Next()
		if !exists {
			continue
		}
		node := iterator.Node()

		if s.HasTooManyPeers() {
			log.Trace("[Sentinel] Not looking for peers, at peer limit")
			time.Sleep(100 * time.Millisecond)
			continue
		}

		peerInfo, _, err := p2p.ConvertToAddrInfo(node)
		if err != nil {
			log.Error("[Sentinel] Could not convert to peer info", "err", err)
			continue
		}
		s.pidToEnr.Store(peerInfo.ID, node)
		s.pidToEnodeId.Store(peerInfo.ID, node.ID())
		// Skip Peer if IP was private, unless local discovery is enabled.
		if !s.cfg.P2PConfig.LocalDiscovery && node.IP().IsPrivate() {
			continue
		}

		if err := s.connectSem.Acquire(s.ctx, 1); err != nil {
			if errors.Is(err, context.Canceled) {
				break
			}
			log.Error("[caplin] Failed to acquire sem for opening peer connection", "err", err)
			continue
		}

		go func() {
			if err := s.ConnectWithPeer(s.ctx, *peerInfo, s.connectSem); err != nil {
				log.Trace("[Sentinel] Could not connect with peer", "err", err)
			}
		}()

	}
}

func (s *Sentinel) onConnection(_ network.Network, conn network.Conn) {
	go func() {
		peerId := conn.RemotePeer()

		if s.HasTooManyPeers() {
			log.Trace("[Sentinel] Rejecting peer, at peer limit")
			s.p2p.Host().Peerstore().RemovePeer(peerId)
			s.p2p.Host().Network().ClosePeer(peerId)
			s.peers.RemovePeer(peerId)
			return
		}

		valid, err := s.handshaker.ValidatePeer(peerId)
		if err != nil {
			// Handshake transport error (stream reset, timeout, etc.) — keep the peer.
			log.Debug("[Sentinel] Handshake transport error (keeping connection)", "peer", peerId, "err", err)
		}

		if !valid && err == nil {
			// Handshake succeeded but fork digest mismatched — peer is on a different fork.
			log.Debug("[Sentinel] Fork mismatch, disconnecting peer", "peer", peerId)
			s.p2p.Host().Peerstore().RemovePeer(peerId)
			s.p2p.Host().Network().ClosePeer(peerId)
			s.peers.RemovePeer(peerId)
			return
		}

		if !valid {
			// Handshake had a transport error AND returned invalid — keep anyway.
			s.peers.RecordHandshakeFailure(peerId)
		} else {
			// we were able to succesfully connect, so add this peer to our pool
			s.peers.AddPeer(peerId)
			log.Trace("[Sentinel] Peer validated and added", "peer", peerId)
		}
	}()
}
