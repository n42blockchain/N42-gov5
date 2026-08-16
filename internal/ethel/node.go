// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// node.go — process root for the eth-el binary.
//
// Node owns the chaindata MDBX handle, the input/output freezers, and
// the ordered list of Services that implement bootstrap, catch-up, and
// the live Engine API loop. NewNode wires storage paths from conf.EthELCfg;
// RegisterExtras allows plug-ins such as Caplin to be mounted before
// Start. Start opens storage, then starts every Service in registration
// order; Stop walks the reverse list. Both calls are idempotent so
// signal handlers can invoke Stop multiple times safely.

package ethel

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	libLog "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
	"github.com/n42blockchain/N42/params"
)

// Node is the eth-el process root. It owns the chaindata MDBX, the
// freezer, and a list of pluggable Services that drive bootstrap, sync,
// and the live Engine API loop.
//
// The lifecycle is intentionally narrow:
//
//	n := ethel.NewNode(cfg)
//	n.RegisterExtras(extras...)   // optional plug-ins (e.g. Caplin)
//	if err := n.Start(ctx); err != nil { ... }
//	defer n.Stop()
//	<-ctx.Done()
//
// Start opens storage, then walks every Service in registration order
// (storage and freezer first, then catch-up, then live services). Stop
// walks the reverse list. Both are idempotent.
type Node struct {
	cfg conf.EthELCfg

	// Storage handles owned by the Node and shared with services.
	chainCfg     *params.ChainConfig
	engine       consensus.Engine
	db           kv.RwDB
	outFreezer   *freezer.Freezer
	inputFreezer *freezer.Freezer

	services  []Service
	factories []ServiceFactory

	startOnce sync.Once
	stopOnce  sync.Once
	startErr  error

	ctx    context.Context
	cancel context.CancelFunc
}

// NewNode constructs a Node with the given configuration. It does no I/O
// — call Start to actually open files and bring services up.
func NewNode(cfg conf.EthELCfg) (*Node, error) {
	if cfg.DataDir == "" {
		return nil, errors.New("ethel.NewNode: DataDir is required")
	}
	var chainCfg *params.ChainConfig
	if cfg.Genesis != nil {
		if cfg.Genesis.Config == nil {
			return nil, errors.New("ethel.NewNode: custom genesis has no chain config")
		}
		chainCfg = params.NormalizeConsensus(cfg.Genesis.Config)
	} else {
		var err error
		chainCfg, err = chainConfigForNetwork(cfg.Network)
		if err != nil {
			return nil, err
		}
	}
	return &Node{
		cfg:      cfg,
		chainCfg: chainCfg,
		engine:   NewEthReplayEngine(chainCfg),
	}, nil
}

// chainConfigForNetwork resolves a network name to a ChainConfig. The
// initial set covers the chains we know how to fetch leaves journals for.
func chainConfigForNetwork(name string) (*params.ChainConfig, error) {
	switch name {
	case "", "mainnet":
		return params.EthereumMainnetChainConfig, nil
	default:
		return nil, fmt.Errorf("ethel.NewNode: unsupported network %q", name)
	}
}

// DB returns the chaindata handle. Valid only between Start and Stop.
func (n *Node) DB() kv.RoDB { return n.db }

// ChainConfig returns the resolved chain spec.
func (n *Node) ChainConfig() *params.ChainConfig { return n.chainCfg }

// Register appends one or more services. Order matters: each service
// starts after every previously registered one and stops before any
// previously registered one.
func (n *Node) Register(svcs ...Service) { n.services = append(n.services, svcs...) }

// ServiceFactory builds a Service from a Node that has already opened its
// storage. Use RegisterFactory when the service constructor needs the
// chaindata MDBX or freezer handle, both of which only exist after
// Node.Start has run openStorage.
type ServiceFactory func(n *Node) Service

// RegisterFactory appends a ServiceFactory. Factories run after openStorage
// and before built-in catch-up so the resulting Services see a populated
// Node.DB() and Node.OutFreezer().
func (n *Node) RegisterFactory(f ServiceFactory) {
	n.factories = append(n.factories, f)
}

// RwDB returns the writable chaindata handle. Valid only after Start has
// run openStorage. Used by ServiceFactory implementations that need
// write access (e.g. the engineAPI plug-in's EngineStateAdapter).
func (n *Node) RwDB() kv.RwDB { return n.db }

// OutFreezer returns the output freezer (leaves journal, witness, etc.).
// Valid only after Start has run openStorage.
func (n *Node) OutFreezer() *freezer.Freezer { return n.outFreezer }

// InputFreezer returns the input freezer — the handle the catch-up
// executor reads historical blocks from. In eth-el the input and
// output freezers point at the same directory (segments downloaded by
// the catchup service land alongside locally-built leaves); the Node
// keeps two distinct handles because the executor machinery treats
// them as separate read/write roles.
func (n *Node) InputFreezer() *freezer.Freezer { return n.inputFreezer }

// Engine returns the consensus.Engine the Node was constructed with.
func (n *Node) Engine() consensus.Engine { return n.engine }

// Start opens storage, registers the built-in services, walks every
// service in registration order, and returns the first failure. On
// failure it stops every service that did manage to start so the caller
// only needs to defer Stop on success.
func (n *Node) Start(ctx context.Context) error {
	n.startOnce.Do(func() {
		n.ctx, n.cancel = context.WithCancel(ctx)
		n.startErr = n.start(n.ctx)
	})
	return n.startErr
}

func (n *Node) start(ctx context.Context) error {
	if err := n.openStorage(ctx); err != nil {
		return fmt.Errorf("open storage: %w", err)
	}

	// Service order:
	//   1. <factory plug-ins> (cmd/eth-el registers bootstrap, catchup,
	//                          engineAPI, optional Caplin — in that order)
	//   2. <Register plug-ins> (rarely used; services that don't need db
	//                          access at construction time)
	//   3. live                (sentinel, last to start / first to stop)
	//
	// Everything that used to be a hard-coded built-in here has moved
	// to internal/ethel/{bootstrap,catchup,engineapi} and is injected
	// through RegisterFactory. The Node is now a pure orchestrator
	// that owns storage handles and a service list, nothing else.
	//
	// The subpackages all depend on internal/ethel (for RebuildState,
	// CatchUp, or the api package which itself imports ethel) so they
	// cannot live here without creating a cycle.
	factoryServices := make([]Service, 0, len(n.factories))
	for _, f := range n.factories {
		factoryServices = append(factoryServices, f(n))
	}

	all := append([]Service{}, factoryServices...)
	all = append(all, n.services...)
	all = append(all, newServiceFunc("live", n.runLive, n.stopLive))
	n.services = all

	for i, svc := range n.services {
		log.Info("eth-el: starting service", "name", svc.Name(), "index", i)
		if err := svc.Start(ctx); err != nil {
			n.stopRange(i) // tear down everything we already started
			return fmt.Errorf("start %s: %w", svc.Name(), err)
		}
	}
	return nil
}

// openStorage opens the chaindata MDBX and the output freezer under
// cfg.DataDir. Subsequent services rely on these handles.
func (n *Node) openStorage(ctx context.Context) error {
	if err := os.MkdirAll(n.cfg.DataDir, 0o755); err != nil {
		return err
	}

	mdbxPath := filepath.Join(n.cfg.DataDir, "chaindata")
	if err := os.MkdirAll(mdbxPath, 0o755); err != nil {
		return err
	}
	logger := libLog.New("module", "ethel-chaindb")
	modules.N42Init()
	db, err := mdbx.NewMDBX(logger).
		Path(mdbxPath).
		Label(kv.ChainDB).
		WithTableCfg(func(_ kv.TableCfg) kv.TableCfg { return modules.N42TableCfg }).
		PageSize(n.cfg.Storage.PageSize).
		MapSize(n.cfg.Storage.MapSize).
		Open(ctx)
	if err != nil {
		return fmt.Errorf("open mdbx: %w", err)
	}
	n.db = db
	if n.cfg.Genesis != nil {
		if err := n.initializeCustomGenesisCommitment(ctx); err != nil {
			db.Close()
			n.db = nil
			return fmt.Errorf("initialize custom genesis commitment: %w", err)
		}
	}

	freezerPath := filepath.Join(n.cfg.DataDir, "chain", "freezer")
	if err := os.MkdirAll(freezerPath, 0o755); err != nil {
		return err
	}
	out, err := freezer.New(freezerPath, 0)
	if err != nil {
		return fmt.Errorf("open output freezer: %w", err)
	}
	n.outFreezer = out

	// Input freezer is optional: only the catch-up phase actually needs
	// it, and the path is the same directory as the output freezer
	// because BT-downloaded segments land alongside locally-built ones.
	in, err := freezer.New(freezerPath, 0)
	if err != nil {
		return fmt.Errorf("open input freezer: %w", err)
	}
	n.inputFreezer = in

	log.Info("eth-el: storage opened",
		"chaindata", mdbxPath,
		"freezer", freezerPath,
		"frozen", in.Frozen(),
	)
	return nil
}

// initializeCustomGenesisCommitment seeds the hashed leaves and TrieOf* cache
// from the PlainState written by `n42 init`. The wire downloader's first block
// may be empty; without this bootstrap it has no dirty accounts from which to
// discover the pre-existing genesis state and incorrectly computes the empty
// trie root.
func (n *Node) initializeCustomGenesisCommitment(ctx context.Context) error {
	tx, err := n.db.BeginRw(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if head := rawdb.ReadCurrentFullBlockNumber(tx); head != nil && *head > 0 {
		return nil
	}
	if err := RebuildHashedState(tx); err != nil {
		return err
	}
	root, err := CalcStateRoot(tx)
	if err != nil {
		return err
	}
	genesisHash, err := rawdb.ReadCanonicalHash(tx, 0)
	if err != nil {
		return err
	}
	genesisHeader := rawdb.ReadHeader(tx, genesisHash, 0)
	if genesisHeader == nil {
		return errors.New("custom genesis header missing")
	}
	if root != genesisHeader.Root {
		return fmt.Errorf("custom genesis root mismatch: computed %s stored %s", root.Hex(), genesisHeader.Root.Hex())
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	log.Info("eth-el: custom genesis commitment initialized", "root", root.Hex())
	return nil
}

// LeavesDir is the on-disk location of the leaves journal freezer
// (under cfg.DataDir/chain/freezer). The bootstrap service writes
// downloaded leaves here and then calls RebuildState on it.
func (n *Node) LeavesDir() string {
	return filepath.Join(n.cfg.DataDir, "chain", "freezer")
}

// runLive is the persistent sentinel. It runs until ctx.Done(), at which
// point Stop walks the reverse service chain. Future commits will plug a
// real Engine API server here.
func (n *Node) runLive(ctx context.Context) error {
	log.Info("eth-el: live mode entered (waiting for ctx.Done)")
	<-ctx.Done()
	log.Info("eth-el: live mode exited")
	return nil
}

func (n *Node) stopLive() error { return nil }

// HasPopulatedState reports whether the chaindata MDBX already contains
// any account rows. Used by the bootstrap service to make its fetch +
// RebuildState step a no-op on re-start.
func (n *Node) HasPopulatedState(ctx context.Context) (bool, error) {
	tx, err := n.db.BeginRo(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	c, err := tx.Cursor("Account")
	if err != nil {
		return false, nil // table missing → empty
	}
	defer c.Close()
	k, _, err := c.First()
	if err != nil {
		return false, err
	}
	return k != nil, nil
}

// Stop walks every started service in reverse order and closes storage.
// Safe to call multiple times.
func (n *Node) Stop() error {
	var stopErr error
	n.stopOnce.Do(func() {
		if n.cancel != nil {
			n.cancel()
		}
		stopErr = n.stopRange(len(n.services))
		if n.outFreezer != nil {
			n.outFreezer.Close()
			n.outFreezer = nil
		}
		if n.inputFreezer != nil {
			n.inputFreezer.Close()
			n.inputFreezer = nil
		}
		if n.db != nil {
			n.db.Close()
			n.db = nil
		}
	})
	return stopErr
}

// stopRange runs Stop on the first n services in reverse order, returning
// the first error (but always attempting every Stop).
func (n *Node) stopRange(upto int) error {
	var firstErr error
	for i := upto - 1; i >= 0; i-- {
		svc := n.services[i]
		log.Info("eth-el: stopping service", "name", svc.Name())
		if err := svc.Stop(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("stop %s: %w", svc.Name(), err)
		}
	}
	return firstErr
}
