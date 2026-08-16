// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// service.go — auth-RPC HTTP listener for the Ethereum Engine API.
//
// Service exposes the engine_* JSON-RPC namespace (NewPayload,
// ForkchoiceUpdated, GetPayload, GetBlobs) backed by the existing
// internal/api EngineAPI types switched into EthEL mode via
// EngineStateAdapter so that consensus-layer calls read and write the
// chaindata MDBX directly. JWT authentication (HS256, Engine API spec
// section 3.1) is mandatory whenever the listener is enabled. No admin
// or debug namespaces and no WebSocket upgrade are mounted — the
// surface is intentionally narrower than internal/node's rpcstack.

package engineapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/internal/api"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
	"github.com/n42blockchain/N42/params"
)

// Service runs the auth-RPC HTTP listener that the consensus layer talks
// to. It exposes the engine_* JSON-RPC namespaces backed by N42's
// existing api.EngineAPIv4 / EngineAPIBlob / EngineAPIV1, all switched
// into "EthEL mode" via api.EngineStateAdapter so that NewPayload and
// ForkchoiceUpdated read and write the chaindata MDBX directly.
//
// The service is intentionally narrower than internal/node's full
// rpcstack: only the engine namespace is registered, no admin / eth /
// debug methods, no WS upgrade. JWT (HS256, Engine API spec §3.1) is
// mandatory whenever the listener is enabled.
//
// The package lives one level below internal/ethel because
// internal/api/engine_state_adapter.go imports internal/ethel for
// ProcessBlock / FullStateRootVerify / Reorg, which would create an
// import cycle if this Service lived in the parent package.
type Service struct {
	cfg      conf.EngineAPICfg
	chainCfg *params.ChainConfig
	engine   consensus.Engine
	db       kv.RwDB
	freezer  *freezer.Freezer

	listener net.Listener
	server   *http.Server
	rpc      *jsonrpc.Server

	apiCore *api.API
	adapter *api.EngineStateAdapter
	v1      *api.EngineAPIV1
	txspool common.ITxsPool

	missingAncestorObserver func(types.Hash)
}

// New builds the service. The DB and freezer must be the same handles
// owned by the eth-el Node so that adapter writes land in the chaindata
// MDBX every other Node service reads from.
func New(cfg conf.EngineAPICfg, chainCfg *params.ChainConfig, engine consensus.Engine, db kv.RwDB, f *freezer.Freezer) *Service {
	return &Service{
		cfg:      cfg,
		chainCfg: chainCfg,
		engine:   engine,
		db:       db,
		freezer:  f,
	}
}

func (s *Service) Name() string { return "engineAPI" }

func (s *Service) Start(_ context.Context) error {
	if !s.cfg.Enabled {
		log.Info("eth-el: engineAPI disabled")
		return nil
	}

	jwtSecret, err := loadOrCreateJWTSecret(s.cfg.JWTSecretPath)
	if err != nil {
		return fmt.Errorf("jwt secret: %w", err)
	}

	// Build the underlying api.API with nil bc / txpool / accounts. The
	// engine namespace only consults BlockChain() through nil-safe
	// helpers (currentHead returns nil → SYNCING) and never touches
	// txspool or accountManager. State writes go through the
	// EngineStateAdapter wired below.
	s.apiCore = api.NewAPI(nil, s.db, s.engine, s.txspool, nil, s.chainCfg)
	s.adapter = api.NewEngineStateAdapter(s.db, s.freezer, s.chainCfg, s.engine).WithHeadMarker(true)

	apis := api.EngineAPIs(s.apiCore)
	for _, a := range apis {
		switch svc := a.Service.(type) {
		case *api.EngineAPIV1:
			s.v1 = svc
			svc.SetStateAdapter(s.adapter)
			svc.SetMissingAncestorObserver(s.missingAncestorObserver)
		case *api.EngineAPIBlob:
			svc.SetStateAdapter(s.adapter)
		case *api.EngineAPIv4:
			svc.SetStateAdapter(s.adapter)
		}
	}

	rpcSrv := jsonrpc.NewServer()
	for _, a := range apis {
		if err := rpcSrv.RegisterName(a.Namespace, a.Service); err != nil {
			return fmt.Errorf("register %s: %w", a.Namespace, err)
		}
	}
	// Minimal eth + net + web3 namespaces — Lighthouse / Prysm /
	// any standards-compliant CL upcheck the EL by calling
	// eth_syncing / eth_chainId / eth_blockNumber before forwarding
	// payloads. Without these the upcheck loops with
	// "Error during execution engine upcheck" and the wire is dead.
	if err := rpcSrv.RegisterName("eth", NewEthAPIMinimal(s.db, s.chainCfg)); err != nil {
		return fmt.Errorf("register eth: %w", err)
	}
	if err := rpcSrv.RegisterName("net", NewNetAPIMinimal(s.chainCfg)); err != nil {
		return fmt.Errorf("register net: %w", err)
	}
	if err := rpcSrv.RegisterName("web3", NewWeb3APIMinimal()); err != nil {
		return fmt.Errorf("register web3: %w", err)
	}
	s.rpc = rpcSrv

	mux := http.NewServeMux()
	mux.Handle("/", newJWTHandler(jwtSecret, rpcSrv))

	addr := net.JoinHostPort(s.cfg.Host, fmt.Sprintf("%d", s.cfg.Port))
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}
	s.listener = listener
	s.server = &http.Server{
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		if err := s.server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("eth-el: engineAPI listener exited", "err", err)
		}
	}()

	log.Info("eth-el: engineAPI listening",
		"addr", addr,
		"jwt", s.cfg.JWTSecretPath,
		"namespaces", "engine,eth,net,web3",
	)
	return nil
}

// SetTxPool shares eth-el's in-process transaction pool with Engine payload
// construction. Call before Start.
func (s *Service) SetTxPool(pool common.ITxsPool) {
	s.txspool = pool
}

// SetMissingAncestorObserver connects unknown-parent Engine payloads to the
// active devp2p downloader.
func (s *Service) SetMissingAncestorObserver(observer func(types.Hash)) {
	s.missingAncestorObserver = observer
	if s.v1 != nil {
		s.v1.SetMissingAncestorObserver(observer)
	}
}

// HasBlockHash reports whether the Engine adapter/overlay already knows hash.
func (s *Service) HasBlockHash(hash types.Hash) bool {
	return s != nil && s.v1 != nil && s.v1.HasBlockHash(hash)
}

// ImportSyncedParisBlock validates a block fetched over devp2p using the
// Engine newPayload path and returns its wire status.
func (s *Service) ImportSyncedParisBlock(blk *block.Block) (string, *types.Hash, error) {
	if s == nil || s.v1 == nil {
		return "", nil, errors.New("engine API v1 is not started")
	}
	status, err := s.v1.ImportSyncedParisBlock(context.Background(), blk)
	if err != nil || status == nil {
		return "", nil, err
	}
	return status.Status, status.LatestValidHash, nil
}

func (s *Service) Stop() error {
	if s.server == nil {
		return nil
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("engineAPI shutdown: %w", err)
	}
	if s.rpc != nil {
		s.rpc.Stop()
	}
	s.server = nil
	s.listener = nil
	return nil
}

// MarkRejectedPayloadHash bridges invalid-chain detection from the devp2p
// downloader into the Engine API's rejected-ancestor cache.
func (s *Service) MarkRejectedPayloadHash(hash, latestValidHash types.Hash) {
	if s == nil || s.apiCore == nil {
		return
	}
	s.apiCore.MarkRejectedPayloadHash(hash, &latestValidHash)
}

// loadOrCreateJWTSecret reads a 32-byte hex-encoded secret from path. If
// path is empty or the file does not exist, a fresh secret is generated
// and (when path != "") persisted. The auto-create branch matches what
// geth/erigon do for first-run convenience; an explicit --engine.jwt
// path remains the recommended setup for shared CL+EL deployments.
func loadOrCreateJWTSecret(path string) ([]byte, error) {
	if path != "" {
		if data, err := os.ReadFile(path); err == nil {
			secret := strings.TrimSpace(string(data))
			secret = strings.TrimPrefix(secret, "0x")
			b, err := hex.DecodeString(secret)
			if err != nil {
				return nil, fmt.Errorf("decode jwt secret: %w", err)
			}
			if len(b) != 32 {
				return nil, fmt.Errorf("jwt secret must be 32 bytes, got %d", len(b))
			}
			return b, nil
		} else if !os.IsNotExist(err) {
			return nil, err
		}
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	if path != "" {
		if err := os.WriteFile(path, []byte(hex.EncodeToString(secret)), 0o600); err != nil {
			return nil, fmt.Errorf("write jwt secret: %w", err)
		}
		log.Info("eth-el: generated JWT secret", "path", path)
	}
	return secret, nil
}
