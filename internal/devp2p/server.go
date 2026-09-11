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
	"time"

	gethcommon "github.com/ethereum/go-ethereum/common"
	gethp2p "github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/p2p/dnsdisc"
	"github.com/ethereum/go-ethereum/p2p/enode"
	"github.com/ethereum/go-ethereum/p2p/nat"
	gethparams "github.com/ethereum/go-ethereum/params"

	n42types "github.com/n42blockchain/N42/common/types"
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
	// Genesis is the chain's genesis hash. It selects the public DNS node
	// list (EIP-1459) the dialer draws candidates from; zero disables it.
	Genesis n42types.Hash
	// DiscoveryURLs overrides the enrtree:// lists derived from Genesis.
	// Empty means "use the well-known list for this chain".
	DiscoveryURLs []string
}

// Server manages devp2p connections and runs the eth protocol.
type Server struct {
	srv     *gethp2p.Server
	handler *EthHandler
	cfg     ServerConfig
	dialMix *enode.FairMix
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
	//
	// Dial candidates come from the public EIP-1459 DNS node list for this
	// chain (all.mainnet.ethdisco.net and friends) MIXED with the discv4/
	// discv5 tables. The tables are shared with every chain that forked
	// Ethereum and kept the bootnodes — measured on this host, ~19 of 20
	// discovered nodes answer the Status handshake with a foreign networkID —
	// so a dialer fed only by them spends its slots on PulseChain et al. The
	// DNS list carries only nodes crawled on THIS network.
	//
	// The mix has to be ours: p2p.Server installs its own discv4/discv5 feeds
	// ONLY when no protocol supplies DialCandidates (setupDiscovery), so
	// handing it a DNS-only iterator would silently switch the DHT off — which
	// it did, dropping dial volume from ~170 to 5 connections a minute. The
	// table iterators are attached after Start, when the server has them.
	dialCandidates := s.dialCandidates()
	ethProtos := make([]gethp2p.Protocol, 0, len(eth69.ProtocolVersions))
	for _, v := range eth69.ProtocolVersions {
		version := v
		length, err := eth69.GetProtocolLength(version)
		if err != nil {
			return err
		}
		ethProtos = append(ethProtos, gethp2p.Protocol{
			Name:           "eth",
			Version:        version,
			Length:         length,
			DialCandidates: dialCandidates,
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
	s.addTableSources()

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
	if s.dialMix != nil {
		s.dialMix.Close() // also closes the DNS + table sources
		s.dialMix = nil
	}
}

// discmixTimeout bounds how long the dialer waits on one source before taking
// a node from any other. Matches go-ethereum's eth backend.
const discmixTimeout = 5 * time.Second

// dialCandidates builds the dial source mix and seeds it with the EIP-1459 DNS
// node lists for this chain. The DHT tables are added later by addTableSources,
// once p2p.Server has started them.
func (s *Server) dialCandidates() enode.Iterator {
	s.dialMix = enode.NewFairMix(discmixTimeout)
	urls := s.cfg.DiscoveryURLs
	if len(urls) == 0 {
		if url := gethparams.KnownDNSNetwork(gethcommon.Hash(s.cfg.Genesis), "all"); url != "" {
			urls = []string{url}
		}
	}
	if len(urls) == 0 {
		return s.dialMix
	}
	it, err := dnsdisc.NewClient(dnsdisc.Config{}).NewIterator(urls...)
	if err != nil {
		// Not fatal: the DHT sources below still feed the dialer.
		log.Warn("devp2p: DNS discovery unavailable, dialing from the DHT tables only",
			"urls", urls, "err", err)
		return s.dialMix
	}
	log.Info("devp2p: DNS discovery enabled", "urls", urls)
	s.dialMix.AddSource(enode.WithSourceName("dns", it))
	return s.dialMix
}

// addTableSources attaches the discv4/discv5 random-node iterators to the dial
// mix. Must run after Start — the tables do not exist before it.
func (s *Server) addTableSources() {
	if s.dialMix == nil || s.srv == nil {
		return
	}
	if v4 := s.srv.DiscoveryV4(); v4 != nil {
		s.dialMix.AddSource(enode.WithSourceName("discv4", v4.RandomNodes()))
	}
	if v5 := s.srv.DiscoveryV5(); v5 != nil {
		s.dialMix.AddSource(enode.WithSourceName("discv5", v5.RandomNodes()))
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
