// Copyright 2024-2026 The N42 Authors
// This file is part of the N42 library.
//
// Ported from erigon cl/p2p. Import paths rewritten to N42 in-repo equivalents.

//go:build n42el

package p2p

import (
	"context"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"golang.org/x/sync/semaphore"

	"github.com/n42blockchain/N42/internal/cl/clparams"
	"github.com/n42blockchain/N42/internal/p2p/discover"
	"github.com/n42blockchain/N42/internal/p2p/enr"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

func (p *p2pManager) connectToBootnodes(ctx context.Context, discoverConfig discover.Config) error {
	for i := range discoverConfig.Bootnodes {
		if err := discoverConfig.Bootnodes[i].Record().Load(enr.WithEntry("tcp", new(enr.TCP))); err != nil {
			if !enr.IsNotFound(err) {
				log.Error("[Sentinel] Could not retrieve tcp port")
			}
			continue
		}
	}
	multiAddresses := ConvertToMultiAddr(discoverConfig.Bootnodes)
	addrInfos, err := peer.AddrInfosFromP2pAddrs(multiAddresses...)
	if err != nil {
		return err
	}
	for _, peerInfo := range addrInfos {
		go func(peerInfo peer.AddrInfo) {
			if err := p.connectWithPeer(ctx, peerInfo, nil); err != nil {
				log.Trace("[Sentinel] Could not connect with peer", "err", err)
			}
		}(peerInfo)
	}
	return nil
}

// connectWithPeer is used to attempt to connect and add the peer to our pool.
// it errors when if fail to connect with the peer, for instance, if it fails the handshake.
func (p *p2pManager) connectWithPeer(ctx context.Context, info peer.AddrInfo, sem *semaphore.Weighted) (err error) {
	if sem != nil {
		defer sem.Release(1)
	}
	if info.ID == p.Host().ID() {
		return nil
	}
	ctxWithTimeout, cancel := context.WithTimeout(ctx, clparams.MaxDialTimeout)
	defer cancel()
	err = p.Host().Connect(ctxWithTimeout, info)
	if err != nil {
		return err
	}
	return nil
}

func (p *p2pManager) peerMonitor(ctx context.Context) {
	ticker := time.NewTicker(time.Second * 30)
	for {
		select {
		case <-ticker.C:
			connected := 0
			closed := 0
			emptyAddrs := 0
			peers := p.Host().Network().Peers()
			for _, peer := range peers {
				if p.Host().Network().Connectedness(peer) == network.Connected {
					connected++
					peerInfo := p.Host().Network().Peerstore().PeerInfo(peer)
					if len(peerInfo.Addrs) == 0 {
						p.connectWithPeer(ctx, peerInfo, nil)
						emptyAddrs++
					}
				} else {
					p.Host().Network().ClosePeer(peer)
					closed++
				}
			}
			log.Trace("[caplin p2p] reporting connected peers", "connected", connected, "closed", closed, "emptyAddrs", emptyAddrs)
		case <-ctx.Done():
			return
		}
	}
}
