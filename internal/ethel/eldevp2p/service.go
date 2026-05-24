// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Package eldevp2p adds Ethereum devp2p (eth/68-69 wire over RLPx + discv4/5)
// to the eth-el binary so it can dial mainnet EL peers on port 30303 and
// catch up its head from devp2p directly, bypassing a consensus-layer client
// entirely.
//
// Why this exists: ISPs / corporate networks that filter outbound TCP 9000
// (the beacon-chain p2p port) block Lighthouse / Prysm from finding any peer
// and therefore stall the entire CL-driven EL sync at "0 peers" forever.
// Outbound TCP 30303 (the eth-1 default) is rarely filtered because it's been
// in production since 2015. Driving the EL via devp2p sidesteps the CL
// blocker entirely.
//
// The service is intentionally narrow:
//
//   - On Start it boots an internal/devp2p.Server bound to a TCP port
//     (default 30303) and registers it with the Ethereum mainnet bootnodes.
//   - It provides a BlockProvider over eth-el's chaindata so peers can ask us
//     for headers we know about (eth-el's archive 0..head).
//   - The peer handshake (eth/69 Status) advertises eth-el's current head as
//     the LatestBlock so peers learn we're available.
//   - An active downloader loop will be added in a follow-up: on each new
//     peer with head > our head, fetch headers + bodies in batches, convert
//     to *common/block.Block, and feed them through the existing
//     api.EngineStateAdapter.ExecutePayload path (Phase 7.1.1.b wiring).

//go:build n42el

package eldevp2p

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"fmt"

	gethcrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/p2p/enode"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/internal/devp2p"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/params"
)

// nodeAccessor is the narrow surface the service needs from an *ethel.Node.
// Declaring it locally avoids importing ethel here (which would create a
// service-registration import cycle); production wiring passes the
// concrete Node.
type nodeAccessor interface {
	DB() kv.RoDB
	ChainConfig() *params.ChainConfig
}

// Config controls the devp2p listener.
type Config struct {
	// Enabled gates the whole subsystem. Default false because operators who
	// already drive their EL via a CL don't need this listener.
	Enabled bool
	// ListenAddr is the TCP listen address; ":30303" matches eth-1 default.
	ListenAddr string
	// MaxPeers caps simultaneous devp2p connections.
	MaxPeers int
	// BootNodes overrides the default Ethereum mainnet bootnodes list.
	BootNodes []string
}

// DefaultConfig is a follower-friendly default — listens on the standard
// eth-1 port and uses the Ethereum mainnet bootnodes shipped in params.
func DefaultConfig() Config {
	return Config{
		Enabled:    false,
		ListenAddr: ":30303",
		MaxPeers:   50,
		BootNodes:  params.EthereumMainnetBootnodes,
	}
}

// Service bridges the eth-el binary lifecycle and the devp2p subsystem.
type Service struct {
	cfg       Config
	node      nodeAccessor
	chainCfg  *params.ChainConfig
	genesis   types.Hash
	genesisTs uint64

	server  *devp2p.Server
	handler *devp2p.EthHandler
	privKey *ecdsa.PrivateKey
}

// New builds a Service. The Node must be non-nil even when Enabled is false
// so the factory can still register the no-op service.
func New(cfg Config, node nodeAccessor, genesis types.Hash, genesisTs uint64) *Service {
	chainCfg := node.ChainConfig()
	return &Service{
		cfg:       cfg,
		node:      node,
		chainCfg:  chainCfg,
		genesis:   genesis,
		genesisTs: genesisTs,
	}
}

// NewFromEthELCfg is a constructor sugar for the eth-el main wiring.
func NewFromEthELCfg(cfg conf.EthELCfg, node nodeAccessor, genesis types.Hash, genesisTs uint64) *Service {
	dc := DefaultConfig()
	// Phase 7.5 follow-up: thread EL p2p flags into conf.EthELCfg so
	// operators can override listen addr / boot nodes via CLI. For now we
	// keep the service opt-in via a fixed default — passing Enabled=false
	// means the service registers but does not start any sockets.
	return New(dc, node, genesis, genesisTs)
}

func (s *Service) Name() string { return "el-devp2p" }

// Start brings the devp2p listener up. Idempotent on the disabled path.
func (s *Service) Start(_ context.Context) error {
	if !s.cfg.Enabled {
		log.Info("eth-el: el-devp2p disabled by config")
		return nil
	}
	if s.node == nil || s.node.DB() == nil {
		return errors.New("eldevp2p: chaindata not ready")
	}

	key, err := gethcrypto.GenerateKey()
	if err != nil {
		return fmt.Errorf("gen p2p key: %w", err)
	}
	s.privKey = key

	provider := &chaindataProvider{db: s.node.DB()}
	handler, err := devp2p.NewEthHandler(s.chainCfg, s.genesis, s.genesisTs, provider)
	if err != nil {
		return fmt.Errorf("eldevp2p handler: %w", err)
	}
	s.handler = handler

	boot := make([]*enode.Node, 0, len(s.cfg.BootNodes))
	for _, raw := range s.cfg.BootNodes {
		n, err := enode.Parse(enode.ValidSchemes, raw)
		if err != nil {
			log.Warn("eth-el: skip invalid bootnode", "raw", raw, "err", err)
			continue
		}
		boot = append(boot, n)
	}

	s.server = devp2p.NewServer(devp2p.ServerConfig{
		PrivateKey:  key,
		ListenAddr:  s.cfg.ListenAddr,
		MaxPeers:    s.cfg.MaxPeers,
		ChainConfig: s.chainCfg,
		BootNodes:   boot,
	}, handler)

	if err := s.server.Start(); err != nil {
		return fmt.Errorf("eldevp2p start: %w", err)
	}
	log.Info("eth-el: el-devp2p started",
		"addr", s.cfg.ListenAddr,
		"bootnodes", len(boot),
		"enode", s.server.Self().URLv4())
	return nil
}

// Stop shuts the devp2p listener down.
func (s *Service) Stop() error {
	if s.server != nil {
		s.server.Stop()
		s.server = nil
	}
	return nil
}

// PeerCount returns the live devp2p peer count, or zero when the service is
// disabled or stopped. Useful for diagnostics endpoints.
func (s *Service) PeerCount() int {
	if s.server == nil {
		return 0
	}
	return s.server.PeerCount()
}

// chaindataProvider implements devp2p.BlockProvider over the eth-el
// chaindata MDBX. Reads only — peer queries (GetBlockHeaders /
// GetBlockBodies) walk rawdb without touching state.
type chaindataProvider struct {
	db kv.RoDB
}

func (p *chaindataProvider) CurrentHead() (*block.Header, types.Hash, error) {
	tx, err := p.db.BeginRo(context.Background())
	if err != nil {
		return nil, types.Hash{}, err
	}
	defer tx.Rollback()
	headHash := rawdb.ReadHeadBlockHash(tx)
	if headHash == (types.Hash{}) {
		// No head stored — return the genesis-ish header so the handshake
		// still completes. Peers will see we're empty and skip us.
		return &block.Header{}, types.Hash{}, nil
	}
	num := rawdb.ReadHeaderNumber(tx, headHash)
	if num == nil {
		return nil, types.Hash{}, errors.New("eldevp2p: head hash without number")
	}
	hdr := rawdb.ReadHeader(tx, headHash, *num)
	if hdr == nil {
		return nil, types.Hash{}, errors.New("eldevp2p: head header missing")
	}
	return hdr, headHash, nil
}

func (p *chaindataProvider) GetHeaderByNumber(number uint64) (*block.Header, error) {
	tx, err := p.db.BeginRo(context.Background())
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	hash, err := rawdb.ReadCanonicalHash(tx, number)
	if err != nil {
		return nil, err
	}
	if hash == (types.Hash{}) {
		return nil, nil
	}
	return rawdb.ReadHeader(tx, hash, number), nil
}

func (p *chaindataProvider) GetHeaderByHash(hash types.Hash) (*block.Header, error) {
	tx, err := p.db.BeginRo(context.Background())
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	num := rawdb.ReadHeaderNumber(tx, hash)
	if num == nil {
		return nil, nil
	}
	return rawdb.ReadHeader(tx, hash, *num), nil
}
