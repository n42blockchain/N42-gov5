// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Package devp2p provides Ethereum devp2p networking for the ETH EL profile.
// It wraps go-ethereum's p2p library for RLPx transport and peer discovery,
// implementing eth/68-69 wire protocol for block/tx propagation and sync.

package devp2p

import (
	"crypto/ecdsa"
	"fmt"

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
	if cfg.MaxPeers == 0 {
		cfg.MaxPeers = 50
	}
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = ":30303"
	}
	return &Server{cfg: cfg, handler: handler}
}

// Start starts the devp2p server.
func (s *Server) Start() error {
	ethProto := gethp2p.Protocol{
		Name:    "eth",
		Version: eth69.ETH68,
		Length:  17, // eth/68 message count
		Run:     s.handler.runPeer,
	}

	s.srv = &gethp2p.Server{
		Config: gethp2p.Config{
			PrivateKey:     s.cfg.PrivateKey,
			MaxPeers:       s.cfg.MaxPeers,
			Name:           "n42-ethel/v1",
			ListenAddr:     s.cfg.ListenAddr,
			Protocols:      []gethp2p.Protocol{ethProto},
			BootstrapNodes: s.cfg.BootNodes,
			NAT:            s.cfg.NAT,
		},
	}

	if err := s.srv.Start(); err != nil {
		return fmt.Errorf("devp2p start: %w", err)
	}

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
