// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Package devp2p provides Ethereum devp2p networking for the ETH EL profile.
// It wraps go-ethereum's p2p library for RLPx transport and peer discovery,
// implementing eth/68-71 wire protocol for block/tx propagation and sync.

package devp2p

import (
	"crypto/ecdsa"
	"errors"
	"fmt"
	"os"
	"strconv"

	gethp2p "github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/p2p/enode"
	"github.com/ethereum/go-ethereum/p2p/nat"

	"github.com/n42blockchain/N42/internal/network/eth69"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/params"
)

// ServerConfig configures the devp2p server.
type ServerConfig struct {
	// PrivateKey is the node's ECDSA private key for RLPx encryption.
	PrivateKey *ecdsa.PrivateKey
	// ListenAddr is the TCP address to listen on (e.g., ":30303").
	ListenAddr string
	// MaxPeers is the maximum number of connected peers.
	MaxPeers int
	// ChainConfig provides network ID and genesis hash for the status handshake.
	ChainConfig *params.ChainConfig
	// BootNodes are initial peers for discovery.
	BootNodes []*enode.Node
	// NAT configures NAT traversal (nil = no NAT).
	NAT nat.Interface
}

// Server manages devp2p connections and runs the eth protocol.
type Server struct {
	srv     *gethp2p.Server
	handler *EthHandler
	cfg     ServerConfig
}

// NewServer creates a new devp2p server.
func NewServer(cfg ServerConfig, handler *EthHandler) *Server {
	// PulseChain (network=369) forked from Ethereum and shares our discovery
	// bootnodes AND forkHash (07c9462e) — only networkID differs, and networkID is
	// NOT in the discv5 ENR, so discovery can't pre-filter it. Its nodes flood our
	// inbound slots and evict scarce real mainnet peers right after handshake. A
	// larger pool (plus trusted-pinning confirmed mainnet peers, see Start) keeps
	// good peers alive. Override with N42_MAX_PEERS.
	if v := os.Getenv("N42_MAX_PEERS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.MaxPeers = n
		}
	}
	if cfg.MaxPeers == 0 {
		cfg.MaxPeers = 200
	}
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = ":30303"
	}
	return &Server{cfg: cfg, handler: handler}
}

// Start starts the devp2p server.
func (s *Server) Start() error {
	// Advertise eth/68 through eth/71. A large share of mainnet peers still speak
	// only older versions; advertising only the latest drops them at the
	// Status handshake (their eth/68 Status mis-decodes as eth/69), starving block
	// download. RLPx negotiates the highest COMMON eth version per peer and runs
	// that Protocol's Run; runPeer receives the negotiated version and encodes/
	// decodes the matching Status layout. Versions 69+ share the newer layout.
	ethProtos := make([]gethp2p.Protocol, 0, len(eth69.ProtocolVersions))
	for _, v := range eth69.ProtocolVersions {
		version := v
		length, err := eth69.GetProtocolLength(version)
		if err != nil {
			return err
		}
		ethProtos = append(ethProtos, gethp2p.Protocol{
			Name:    "eth",
			Version: version,
			Length:  length,
			Run: func(peer *gethp2p.Peer, rw gethp2p.MsgReadWriter) error {
				return s.handler.runPeer(peer, rw, version)
			},
		})
	}
	// snap/1 stub — without it every modern mainnet client (Geth /
	// Nethermind / Erigon / Besu / Pulse / Bera) classifies us as a
	// snapless leech and EOF-drops within ~1s of eth handshake. The
	// stub answers all GetXxx with empty XxxResp carrying the same
	// request ID; that's a valid snap/1 response meaning "no data in
	// range" and is enough to stay paired.
	snapHandler := NewSnapHandler()
	snapProto := gethp2p.Protocol{
		Name:    "snap",
		Version: 1,
		Length:  snapProtocolLength,
		Run:     snapHandler.runPeer,
	}

	s.srv = &gethp2p.Server{
		Config: gethp2p.Config{
			PrivateKey: s.cfg.PrivateKey,
			MaxPeers:   s.cfg.MaxPeers,
			// Name shows up in geth's peer list; some peer scorers
			// downrank unknown clients. Mimic a stock recent Geth so
			// we don't get penalised on string heuristics.
			Name:       "Geth/v1.17.2-stable-7c8a8a8a/linux-amd64/go1.23.0",
			ListenAddr: s.cfg.ListenAddr,
			Protocols:  append(ethProtos, snapProto),
			// DiscoveryV4 + DiscoveryV5 are NOT enabled by default —
			// without them BootstrapNodes are only used as static peers
			// and we never walk the DHT to find real serving peers.
			// Bootnodes are typically discovery-only and EOF us right
			// after the eth handshake; without discovery we'd hit only
			// them and never find an archive node.
			DiscoveryV4:      true,
			DiscoveryV5:      true,
			BootstrapNodes:   s.cfg.BootNodes,
			BootstrapNodesV5: s.cfg.BootNodes,
			NAT:              s.cfg.NAT,
			// Required so SubscribeEvents fires PeerEventTypeDrop with
			// the actual RLPx Disconnect reason in PeerEvent.Error.
			EnableMsgEvents: true,
		},
	}

	s.handler.setTrustedCallbacks(
		func(n *enode.Node) {
			if n != nil {
				s.srv.AddTrustedPeer(n)
			}
		},
		func(n *enode.Node) {
			if n != nil {
				s.srv.RemoveTrustedPeer(n)
			}
		},
	)

	if err := s.srv.Start(); err != nil {
		return fmt.Errorf("devp2p start: %w", err)
	}

	// Subscribe to peer add/drop events so we can log the actual
	// RLPx Disconnect reason from the remote side. That reason is
	// otherwise hidden — our subprotocol handler just sees io.EOF.
	// Without this, debugging 'why does geth EOF us?' is blind.
	events := make(chan *gethp2p.PeerEvent, 256)
	sub := s.srv.SubscribeEvents(events)
	go func() {
		defer sub.Unsubscribe()
		for ev := range events {
			if ev.Type != gethp2p.PeerEventTypeAdd && ev.Type != gethp2p.PeerEventTypeDrop {
				continue
			}
			id := ev.Peer.String()
			if len(id) > 16 {
				id = id[:16]
			}
			if ev.Type == gethp2p.PeerEventTypeAdd {
				log.Info("p2p: peer add", "id", id, "proto", ev.Protocol)
				continue
			}
			// Drop — the precious one. ev.Error carries the disconnect
			// reason string ("useless peer", "subprotocol error: ...",
			// "client quitting", etc.) when geth's framing layer parsed
			// a Disconnect frame before the TCP close.
			log.Warn("p2p: peer drop",
				"id", id, "proto", ev.Protocol, "reason", ev.Error)
		}
	}()

	log.Info("devp2p server started",
		"listenAddr", s.cfg.ListenAddr,
		"maxPeers", s.cfg.MaxPeers,
		"enode", s.srv.Self().URLv4())
	return nil
}

// Stop stops the devp2p server.
func (s *Server) Stop() {
	if s.srv != nil {
		s.srv.Stop()
	}
}

// Self returns the local node's enode URL.
func (s *Server) Self() *enode.Node {
	if s.srv != nil {
		return s.srv.Self()
	}
	return nil
}

// PeerCount returns the number of connected peers.
func (s *Server) PeerCount() int {
	if s.srv != nil {
		return s.srv.PeerCount()
	}
	return 0
}

// AddPeer adds a static devp2p peer by enode URL.
func (s *Server) AddPeer(rawURL string) error {
	if s == nil || s.srv == nil {
		return errors.New("devp2p server not running")
	}
	node, err := enode.ParseV4(rawURL)
	if err != nil {
		return fmt.Errorf("parse enode: %w", err)
	}
	s.srv.AddTrustedPeer(node)
	s.srv.AddPeer(node)
	return nil
}

// RemovePeer removes a static devp2p peer by enode URL.
func (s *Server) RemovePeer(rawURL string) error {
	if s == nil || s.srv == nil {
		return errors.New("devp2p server not running")
	}
	node, err := enode.ParseV4(rawURL)
	if err != nil {
		return fmt.Errorf("parse enode: %w", err)
	}
	s.srv.RemovePeer(node)
	return nil
}
