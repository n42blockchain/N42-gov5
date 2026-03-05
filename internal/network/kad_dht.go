// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package network

import (
	"context"
	"fmt"
	"runtime/debug"
	"time"

	kademliaDHT "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/peer"
	discovery "github.com/libp2p/go-libp2p/p2p/discovery/routing"

	"github.com/n42blockchain/N42/log"
)

type KadDHT struct {
	*kademliaDHT.IpfsDHT

	service *Service

	routingDiscovery *discovery.RoutingDiscovery

	ctx    context.Context
	cancel context.CancelFunc
}

func NewKadDht(ctx context.Context, s *Service, isServer bool, bootstrappers ...peer.AddrInfo) (*KadDHT, error) {
	c, cancel := context.WithCancel(ctx)

	mode := kademliaDHT.ModeClient
	if isServer {
		mode = kademliaDHT.ModeServer
	}

	dht, err := kademliaDHT.New(c, s.host, kademliaDHT.Mode(mode), kademliaDHT.BootstrapPeers(bootstrappers...))
	if err != nil {
		log.Error("failed to create DHT", "err", err)
		cancel()
		return nil, err
	}

	return &KadDHT{
		IpfsDHT:          dht,
		routingDiscovery: discovery.NewRoutingDiscovery(dht),
		ctx:              c,
		cancel:           cancel,
		service:          s,
	}, nil
}

func (k *KadDHT) Start() error {
	if err := k.Bootstrap(k.ctx); err != nil {
		log.Error("failed to bootstrap DHT", "err", err)
		return err
	}

	k.routingDiscovery.Advertise(k.ctx, DiscoverProtocol)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Error("panic in loopDiscoverRemote", "panic", r, "stack", string(debug.Stack()))
			}
		}()
		k.loopDiscoverRemote()
	}()
	return nil
}

func (k *KadDHT) loopDiscoverRemote() {
	// Start with a short initial interval, then switch to a longer one.
	const initialInterval = 10 * time.Second
	const steadyInterval = 60 * time.Second

	ticker := time.NewTicker(initialInterval)
	defer ticker.Stop()

	for {
		select {
		case <-k.ctx.Done():
			return
		case <-ticker.C:
			peerChan, err := k.routingDiscovery.FindPeers(k.ctx, DiscoverProtocol)
			if err != nil {
				log.Warn("failed to find peers", "err", err)
			} else {
				for p := range peerChan {
					if p.ID != k.service.host.ID() {
						log.Trace("found peer", "info", fmt.Sprintf("%s", p.String()))
						k.service.HandlePeerFound(p)
					}
				}
			}
			ticker.Reset(steadyInterval)
		}
	}
}
