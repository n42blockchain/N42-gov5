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
//
// Miner orchestrator. Owns the mining loop, accepts new chain-head
// events and delegates block assembly to the worker, block sealing to
// the consensus engine. Exposes Start / Stop / SetEtherbase and wires
// in the optional AIOptimizer for MEV-aware transaction ordering and
// the deferred executor when consensus-execution separation is
// enabled.

package miner

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/internal/miner/builder"
	"github.com/n42blockchain/N42/log"
	event "github.com/n42blockchain/N42/modules/event/v2"
	"golang.org/x/sync/errgroup"
)

type Miner struct {
	mu       sync.RWMutex
	coinbase types.Address
	engine   consensus.Engine
	worker   *worker
	txsPool  common.ITxsPool

	startCh chan types.Address
	stopCh  chan struct{}

	ctx    context.Context
	cancel context.CancelFunc

	group *errgroup.Group
}

func NewMiner(ctx context.Context, cfg *conf.Config, bc common.IBlockChain, engine consensus.Engine, txsPool common.ITxsPool, isLocalBlock func(header *block.Header) bool) *Miner {
	group, errCtx := errgroup.WithContext(ctx)
	cancelCtx, cancel := context.WithCancel(errCtx)
	miner := &Miner{
		engine:  engine,
		txsPool: txsPool,
		startCh: make(chan types.Address, 1),
		stopCh:  make(chan struct{}),
		group:   group,
		ctx:     cancelCtx,
		cancel:  cancel,
		worker:  newWorker(cancelCtx, group, cfg.ChainCfg, engine, bc, txsPool, isLocalBlock, false, cfg.Miner),
	}

	return miner
}

func (m *Miner) Start() {
	m.mu.RLock()
	cb := m.coinbase
	m.mu.RUnlock()
	log.Info("start miner", "coinbase", cb)
	m.group.Go(func() error {
		return m.runLoop()
	})
	select {
	case m.startCh <- cb:
	case <-m.ctx.Done():
		log.Warn("miner context cancelled before start completed")
	}
}

func (m *Miner) runLoop() error {
	defer m.cancel()
	defer func() {
		if m.Mining() {
			m.worker.close()
		}
	}()

	downloaderFinishCh := make(chan common.DownloaderFinishEvent, 10)
	downloaderStartCh := make(chan common.DownloaderStartEvent, 10)
	finishSub, _ := event.GlobalEvent.Subscribe(downloaderFinishCh)
	startSub, _ := event.GlobalEvent.Subscribe(downloaderStartCh)
	defer finishSub.Unsubscribe()
	defer startSub.Unsubscribe()

	canStart := false
	shouldStart := false

	for {
		select {
		case <-m.ctx.Done():
			return m.ctx.Err()

		case _, ok := <-downloaderFinishCh:
			if ok {
				canStart = true
				if !m.Mining() && shouldStart {
					time.Sleep(5 * time.Second)
					m.mu.RLock()
					cb := m.coinbase
					m.mu.RUnlock()
					m.SetCoinbase(cb)
					m.worker.start()
				}
			}

		case _, ok := <-downloaderStartCh:
			if ok && m.Mining() {
				m.worker.stop()
			}

		case err := <-finishSub.Err():
			return err
		case err := <-startSub.Err():
			return err

		case addr, ok := <-m.startCh:
			if ok {
				m.SetCoinbase(addr)
				if canStart {
					m.worker.start()
				}
				shouldStart = true
			}

		case <-m.stopCh:
			shouldStart = false
			if m.Mining() {
				m.worker.stop()
			}
		}
	}
}

func (miner *Miner) Close() {
	miner.cancel()
	miner.worker.close()
	if err := miner.group.Wait(); err != nil && !errors.Is(err, context.Canceled) {
		log.Error("miner errgroup returned error", "err", err)
	}
}

func (m *Miner) Mining() bool {
	return m.worker.isRunning()
}

func (m *Miner) SetCoinbase(addr types.Address) {
	m.mu.Lock()
	m.coinbase = addr
	m.mu.Unlock()
	m.worker.setCoinbase(addr)
}

func (m *Miner) PendingBlockAndReceipts() (block.IBlock, block.Receipts) {
	return m.worker.pendingBlockAndReceipts()
}

// TriggerBlockProduction triggers an immediate block production attempt.
// Used by BFT consensus (e.g., HotStuff) to start block building when the
// node becomes the leader for a new view.
func (m *Miner) TriggerBlockProduction() {
	if !m.worker.isRunning() {
		return
	}
	interrupt := new(atomic.Int32)
	select {
	case m.worker.newWorkCh <- &newWorkReq{interrupt: interrupt, noempty: true, timestamp: time.Now().Unix()}:
	default:
		// Channel full — a block build is already in progress.
	}
}

// CommitToCanonical forces the HotStuff-committed block to be the canonical
// head, reconciling the locally-inserted candidate with the committed chain.
func (m *Miner) CommitToCanonical(hash types.Hash) error {
	type canonicalCommitter interface {
		CommitToCanonical(types.Hash) error
	}
	if cc, ok := m.worker.chain.(canonicalCommitter); ok {
		return cc.CommitToCanonical(hash)
	}
	return nil
}

// SetZKProverService sets the ZK prover service on the miner's worker.
// Must be called before Start() for the miner to submit proof requests.
func (m *Miner) SetZKProverService(svc interface {
	SubmitBlock(blockHash types.Hash, blockNum uint64, guestInput []byte) error
}) {
	m.worker.zkProverService = svc
}

// AIOptimizer is the interface for AI-enhanced transaction ordering.
type AIOptimizer interface {
	OptimizeOrdering(txs []*transaction.Transaction, baseFee *uint256.Int) []*transaction.Transaction
}

// SetAIOptimizer sets the AI block optimizer on the miner's worker.
// When set, the optimizer is used to reorder transactions for improved block value.
func (m *Miner) SetAIOptimizer(optimizer AIOptimizer) {
	m.worker.aiOptimizer = optimizer
}

// BundlePool returns the MEV bundle pool for submitting transaction bundles.
func (m *Miner) BundlePool() *builder.BundlePool {
	return m.worker.bundlePool
}
