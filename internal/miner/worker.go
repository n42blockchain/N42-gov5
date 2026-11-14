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
// Block building worker. Pulls pending transactions from the txpool,
// applies them to a fresh IntraBlockState snapshot, collects receipts
// and computes the candidate header. Handles uncles, reorgs, proposer
// signalling and panic recovery via runtime/debug so a single bad tx
// cannot kill the mining loop. Feeds sealed blocks back to miner.go.

package miner

import (
	"context"
	"fmt"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/n42blockchain/N42/cmd/evmsdk"
	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/envswitch"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/internal/api"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/internal/miner/builder"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

type worker struct {
	minerConf conf.MinerConfig
	engine    consensus.Engine
	chain     common.IBlockChain
	txsPool   common.ITxsPool

	coinbase    types.Address
	chainConfig *params.ChainConfig

	isLocalBlock func(header *block.Header) bool
	pendingTasks map[types.Hash]*task

	// Local frames (frame blocks only): the signer the pool's transactions
	// are framed under, and the frames built so far (see frame_fill.go).
	localFrames localFrameState

	// Cross-view speculative build state. specTask holds the parked result of
	// a speculative commitWork (guarded by specMu); specParent is the parent
	// the parked block extends. activeSpecWork publishes the immutable parent
	// and interrupt together, so real work can preserve a matching build and
	// interrupt a stale one without racing the worker's next build.
	specMu         sync.Mutex
	specTask       *task
	specParent     types.Hash
	activeSpecWork atomic.Pointer[newWorkReq]

	// sealedOnParent records the FIRST block this node sealed on a given parent
	// (keyed by parentHash). A leader re-elected across several views at the same
	// height — before that height commits — would otherwise seal a second,
	// byte-different block on the same parent; the two uncommitted siblings then
	// drive an endless branch-switch livelock (each re-proposed via
	// NotifyBlockSealed, each reverting the other's applied state — observed live
	// at 13013248: 0cd6e42a vs 0976bead, 1219 switches). Keeping only the first
	// candidate per parent makes each node produce exactly one block per height.
	// Guarded by mu; pruned below the branch-switch window as heights advance.
	sealedOnParent map[types.Hash]block.IBlock
	// sealedOnParentSlim marks the sealedOnParent entries already replaced
	// by their slim copy (recordSealedOnParent).
	sealedOnParentSlim map[types.Hash]struct{}
	// sealedByHash holds this node's recent sealed blocks by hash from the
	// moment they are sealed, before their write lands: a speculative build
	// whose parent is one of them builds on the miner tree's own post-state
	// without waiting for the write (track 3c, two-deep speculation).
	sealedByHash map[types.Hash]block.IBlock
	// sealedPost holds the post-state snapshot of each block in sealedByHash
	// that was built with one (adopt-appends mode); pruned with it.
	sealedPost map[types.Hash]*state.PostState
	// sealedExec holds the own execution result of each block in
	// sealedByHash built under deferred execution; pruned with it.
	sealedExec map[types.Hash]rawdb.ExecutedResult

	wg sync.WaitGroup
	mu sync.RWMutex

	startCh   chan struct{}
	newWorkCh chan *newWorkReq
	resultCh  chan block.IBlock
	taskCh    chan *task

	resubmitAdjustCh chan *intervalAdjust

	running int32
	newTxs  int32

	group  *errgroup.Group
	ctx    context.Context
	cancel context.CancelFunc

	newTaskHook func(*task)

	bundlePool      *builder.BundlePool // MEV bundle pool
	zkProverService interface {         // ZK prover service (nil if disabled)
		SubmitBlock(blockHash types.Hash, blockNum uint64, guestInput []byte) error
	}
	aiOptimizer AIOptimizer // AI transaction ordering optimizer (nil if disabled)

	// mobilePacketSink, when non-nil, receives the StreamPacket produced
	// for each sealed block (read log captured during build, final header
	// from FinalizeAndAssemble). Non-nil is the enable switch: without a
	// sink no recorder is wrapped and the build path is unchanged.
	// Injected via Miner.SetMobilePacketSink.
	mobilePacketSink func(pkt *evmsdk.StreamPacket, blockNumber uint64)

	// mobileAnchorRoot, when non-nil, supplies the committed mobile-registry
	// accumulator root the leader stamps into Header.MobileRegistryRoot when
	// the MobileAnchor fork is active (n42 native chain, phase 6c). Injected
	// via Miner.SetMobileAnchorRoot.
	mobileAnchorRoot func() *types.Hash

	// Block-production pacing (minerConf.BlockIntervalMs > 0). The header
	// timestamp is already drift-free (parent.Time + period, an absolute grid);
	// this throttles the WALL-CLOCK rate at which the leader seals blocks to a
	// fixed interval, anchored to an absolute grid so it never accumulates drift
	// over a long run. Zero interval = no throttle (produce flat out). These
	// fields are touched only from the single commitWork goroutine.
	pacingAnchorWall time.Time
	pacingAnchorNum  uint64
	pacingAnchorSet  bool

	// Pending-block snapshot for RPC readers, built LAZILY: updateSnapshot
	// stores the raw material and pendingBlockAndReceipts assembles on first
	// read. Building eagerly cost every block ~30-45ms of the leader's
	// critical path (CreateBloom + TxRoot + DeriveSha over 22,857 entries)
	// for an endpoint that is almost never queried.
	snapshotMu       sync.Mutex
	snapshotEnv      *environment // material; nil until the first build
	snapshotRewards  []*block.Reward
	snapshotBlock    block.IBlock // lazily assembled from snapshotEnv
	snapshotReceipts block.Receipts

	// asyncWriter is non-nil only when N42_LEADER_WRITE_ASYNC=1 (set once in
	// newWorker); nil means "off", and every call site checks for nil rather
	// than re-reading the switch, so a switched-off worker never even
	// allocates the channel. See async_write.go.
	asyncWriter *asyncBlockWriter
}

func newWorker(ctx context.Context, group *errgroup.Group, chainConfig *params.ChainConfig, engine consensus.Engine, bc common.IBlockChain, txsPool common.ITxsPool, isLocalBlock func(header *block.Header) bool, init bool, minerConf conf.MinerConfig) *worker {
	c, cancel := context.WithCancel(ctx)
	worker := &worker{
		engine:       engine,
		chain:        bc,
		txsPool:      txsPool,
		chainConfig:  chainConfig,
		startCh:      make(chan struct{}, 1),
		group:        group,
		isLocalBlock: isLocalBlock,
		ctx:          c,
		cancel:       cancel,
		taskCh:       make(chan *task),
		// Capacity 1: TriggerBlockProduction (leader-driven HotStuff) sends with
		// a non-blocking select — an UNBUFFERED channel silently dropped the
		// request whenever runLoop wasn't parked exactly on the receive (e.g.
		// mid clearPending or a previous build), so the leader never proposed
		// for its view and every view timed out (observed live: "TC formed,
		// I am the new leader" followed by nothing, on all 7 nodes). One slot
		// queues exactly one pending build; runLoop drains it when free, and a
		// full slot means a build is already pending — dropping then is correct.
		newWorkCh:        make(chan *newWorkReq, 1),
		resultCh:         make(chan block.IBlock),
		pendingTasks:     make(map[types.Hash]*task),
		sealedOnParent:   make(map[types.Hash]block.IBlock),
		sealedByHash:     make(map[types.Hash]block.IBlock),
		sealedPost:       make(map[types.Hash]*state.PostState),
		sealedExec:       make(map[types.Hash]rawdb.ExecutedResult),
		minerConf:        minerConf,
		resubmitAdjustCh: make(chan *intervalAdjust, resubmitAdjustChanSize),
		bundlePool:       builder.NewBundlePool(),
	}
	if LeaderWriteAsyncOn() {
		worker.asyncWriter = newAsyncBlockWriter(worker)
	}
	recommit := worker.minerConf.Recommit
	if recommit < minPeriodInterval {
		recommit = minPeriodInterval
	}

	// recoverWrap converts panics in errgroup goroutines to errors,
	// preventing a single goroutine panic from killing the entire process.
	recoverWrap := func(name string, fn func() error) func() error {
		return func() (err error) {
			defer func() {
				if r := recover(); r != nil {
					log.Errorf("panic in miner %s: %v\nstack: %s", name, r, debug.Stack())
					err = fmt.Errorf("panic in %s: %v", name, r)
				}
			}()
			return fn()
		}
	}

	// machine verify
	group.Go(recoverWrap("MachineVerify", func() error {
		return api.MachineVerify(ctx)
	}))

	group.Go(recoverWrap("workLoop", func() error {
		return worker.workLoop(recommit)
	}))

	group.Go(recoverWrap("runLoop", func() error {
		return worker.runLoop()
	}))

	group.Go(recoverWrap("taskLoop", func() error {
		return worker.taskLoop()
	}))

	group.Go(recoverWrap("resultLoop", func() error {
		return worker.resultLoop()
	}))

	if init {
		worker.startCh <- struct{}{}
	}

	return worker
}

func (w *worker) start() {
	atomic.StoreInt32(&w.running, 1)
	w.startCh <- struct{}{}
}

func (w *worker) stop() {
	atomic.StoreInt32(&w.running, 0)
}

func (w *worker) close() {
	// Cancel context to signal all goroutines to stop
	if w.cancel != nil {
		w.cancel()
	}
}

func (w *worker) isRunning() bool {
	return atomic.LoadInt32(&w.running) == 1
}

func (w *worker) setCoinbase(addr types.Address) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.coinbase = addr
}

func init() {
	envswitch.Declare("N42_MINER_PARALLEL_FILL", envswitch.Perf, "Block-STM fill of the builder.")
}
