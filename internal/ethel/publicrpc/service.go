// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Public (non-JWT) JSON-RPC service for eth-el — the Blockscout-facing endpoint.
// It reuses the shared internal/api handlers (eth/net/web3) and internal/tracers
// (debug_/trace_) over the eth-el datadir: block/tx/receipt/log reads go through
// the rawdb-backed ethelChain adapter, and state reads through API.State() wired
// to the eth-el per-mode reader (modestate). A capability gate (rpccaps) rejects
// methods the node's data mode cannot serve, so an unsupported call gets a clean
// "not available in mode X" instead of a wrong answer.
//
// This is the eth-el half of the two-mode Blockscout integration; the n42
// self-chain serves the same namespaces through internal/node.startRPC. See
// docs/integration/BLOCKSCOUT_V11_COMPATIBILITY.md.

package publicrpc

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/n42blockchain/N42/internal/api"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/internal/ethel/rpccaps"
	"github.com/n42blockchain/N42/internal/ethel/snapshotreader"
	"github.com/n42blockchain/N42/internal/tracers"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb"
	rpc "github.com/n42blockchain/N42/modules/rpc/jsonrpc"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"

	"github.com/n42blockchain/N42/common/types"
)

// Config controls the public RPC listener.
type Config struct {
	Enabled bool
	Host    string
	Port    int
	// Mode is the data-availability mode this node serves (archive/full/m1/m0);
	// it selects both the state reader and the rpccaps gate.
	Mode rpccaps.Mode
	// HashedCanonical selects the reth-2.2 hashed-canonical state model
	// (HashedAccounts/HashedStorage, no PlainState). When set, latest-state reads
	// go through the hashed reader; historical reads are rejected until the
	// reverse-apply reader is wired. Mirrors the node's --hashed-canonical flag.
	HashedCanonical bool
	// Snapshot/Code are needed only for M1 (snapshot+hot) state reads; nil for
	// full/archive (which read PlainState or hashed) and M0 (not state-backed).
	Snapshot *snapshotreader.Segment
	Code     state.CodeSource
}

// Service is the public RPC server lifecycle.
type Service struct {
	cfg      Config
	chainCfg *params.ChainConfig
	db       kv.RwDB
	rpc      *rpc.Server
	server   *http.Server
	listener net.Listener
}

// Disabled returns an inert service (Start/Stop no-op) — used when construction
// fails or the feature is off, so the eth-el service registry never holds a nil.
func Disabled() *Service { return &Service{cfg: Config{Enabled: false}} }

// ParseMode maps a mode name ("archive"/"full"/"m1"/"m0") to the rpccaps.Mode
// used for state selection + the capability gate. Unknown → Archive (the most
// permissive, matching the recommended Blockscout backend).
func ParseMode(s string) rpccaps.Mode {
	switch s {
	case "full":
		return rpccaps.Full
	case "m1", "M1":
		return rpccaps.M1
	case "m0", "M0":
		return rpccaps.M0
	default:
		return rpccaps.Archive
	}
}

// New builds the service. db+engine+chainCfg are the eth-el node's handles.
func New(cfg Config, chainCfg *params.ChainConfig, engine consensus.Engine, db kv.RwDB) (*Service, error) {
	if chainCfg == nil {
		return nil, fmt.Errorf("publicrpc: nil chain config")
	}

	// Per-mode state reader for the post-state of block N (the block whose state
	// a query targets). Selects hashed vs plain vs snapshot per config.
	stateReader := func(tx kv.Tx, postStateBlock uint64) (state.StateReader, error) {
		return buildStateReader(cfg, tx, postStateBlock)
	}

	chain := newEthelChain(db, engine, chainCfg, stateReader)
	core := api.NewAPI(chain, db, engine, nil, nil, chainCfg)
	core.SetStateReaderProvider(stateReader)

	srv := rpc.NewServer()

	// eth / web3 / net from the shared handlers. The txpool + n42 namespaces are
	// intentionally skipped: an eth-el read node has no mempool (rpccaps gates
	// mempool methods) and no n42 consensus evidence.
	for _, a := range core.Apis() {
		switch a.Namespace {
		case "eth", "web3", "net":
			if err := srv.RegisterName(a.Namespace, a.Service); err != nil {
				return nil, fmt.Errorf("register %s: %w", a.Namespace, err)
			}
		}
	}
	// debug_ (callTracer) + trace_ (Parity) from the tracer package, over the
	// same eth-el API (its StateAtBlock/StateAtTransaction reach the eth-el state
	// through chain.StateAt).
	for _, a := range tracers.APIs(core) {
		if err := srv.RegisterName(a.Namespace, a.Service); err != nil {
			return nil, fmt.Errorf("register %s: %w", a.Namespace, err)
		}
	}

	return &Service{cfg: cfg, chainCfg: chainCfg, db: db, rpc: srv}, nil
}

// currentHead reads the canonical head number under a short read tx (0 if
// unavailable) — the gate's Latest/Historical scope reference.
func (s *Service) currentHead() uint64 {
	if s.db == nil {
		return 0
	}
	tx, err := s.db.BeginRo(context.Background())
	if err != nil {
		return 0
	}
	defer tx.Rollback()
	return headBlockNumber(tx)
}

// Name identifies the service in the eth-el service registry.
func (s *Service) Name() string { return "publicRPC" }

// Start binds the listener and serves. No-op when disabled.
func (s *Service) Start(_ context.Context) error {
	if !s.cfg.Enabled {
		log.Info("eth-el: public RPC disabled")
		return nil
	}
	handler := newCapabilityGate(s.cfg.Mode, s.currentHead, s.rpc)

	addr := net.JoinHostPort(s.cfg.Host, fmt.Sprintf("%d", s.cfg.Port))
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("publicrpc listen %s: %w", addr, err)
	}
	s.listener = listener
	s.server = &http.Server{
		Handler:      handler,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}
	go func() {
		if err := s.server.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Error("eth-el: public RPC listener exited", "err", err)
		}
	}()
	log.Info("eth-el: public RPC listening", "addr", addr, "mode", s.cfg.Mode.String())
	return nil
}

// Stop shuts the server down.
func (s *Service) Stop() error {
	if s.server == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.server.Shutdown(ctx)
}

// buildStateReader selects the modules/state reader for the post-state of
// postStateBlock. Hashed-canonical serves the tip through HashedStateReader and
// rejects historical (the reverse-apply reader is a follow-up); plain
// full/archive read PlainState(tx, postStateBlock+1); M1 reads the cold snapshot.
func buildStateReader(cfg Config, tx kv.Tx, postStateBlock uint64) (state.StateReader, error) {
	if tx == nil {
		return nil, fmt.Errorf("publicrpc: nil chaindata tx")
	}
	if cfg.HashedCanonical {
		head := headBlockNumber(tx)
		if postStateBlock >= head {
			r := state.NewHashedStateReader(tx)
			if cfg.Code != nil {
				r.SetCodeSource(cfg.Code)
			}
			return r, nil
		}
		// Historical: reconstruct via the history index + changesets over the tip
		// hashed base. Only Archive retains full history; Full keeps latest only.
		if cfg.Mode != rpccaps.Archive {
			return nil, fmt.Errorf("publicrpc: historical state (block %d < head %d) not available in %s mode", postStateBlock, head, cfg.Mode)
		}
		r := state.NewHashedHistoricalReader(tx, postStateBlock+1)
		if cfg.Code != nil {
			r.SetCodeSource(cfg.Code)
		}
		return r, nil
	}
	switch cfg.Mode {
	case rpccaps.Full, rpccaps.Archive:
		return state.NewPlainState(tx, postStateBlock+1), nil
	case rpccaps.M1:
		if cfg.Snapshot == nil {
			return nil, fmt.Errorf("publicrpc: M1 needs a snapshot segment")
		}
		return snapshotreader.NewStateReader(cfg.Snapshot, cfg.Code), nil
	default:
		return nil, fmt.Errorf("publicrpc: mode %s is not state-backed", cfg.Mode)
	}
}

// headBlockNumber reads the current canonical head number from the eth-el DB.
func headBlockNumber(tx kv.Tx) uint64 {
	if n := rawdb.ReadCurrentBlockNumber(tx); n != nil {
		return *n
	}
	if hash := rawdb.ReadHeadBlockHash(tx); hash != (types.Hash{}) {
		if n := rawdb.ReadHeaderNumber(tx, hash); n != nil {
			return *n
		}
	}
	// eldevp2p advances the chain without writing either of the above, so on
	// those datadirs both lookups miss and eth_blockNumber would report 0 for a
	// node that is fully caught up — including to the weekly runbook's own
	// external check. Fall back to the marker eldevp2p does write.
	if n, ok := ethel.ReadHeadMarker(tx); ok {
		return n
	}
	return 0
}
