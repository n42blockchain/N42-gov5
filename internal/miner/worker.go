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
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"runtime/debug"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	mapset "github.com/deckarep/golang-set"
	"github.com/holiman/uint256"
	"golang.org/x/sync/errgroup"

	"github.com/n42blockchain/N42/cmd/evmsdk"
	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/metrics"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/api"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/internal/consensus/misc"
	"github.com/n42blockchain/N42/internal/miner/builder"
	"github.com/n42blockchain/N42/internal/streamverify"
	vm2 "github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/internal/zkprover"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/layered"
	"github.com/n42blockchain/N42/log"
	event "github.com/n42blockchain/N42/modules/event/v2"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/modules/state/commitment"
	"github.com/n42blockchain/N42/modules/state/witness"
	"github.com/n42blockchain/N42/params"
)

var (
	blockSignGauge      = prometheus.GetOrCreateCounter("block_sign_counter", true)
	blocksMinedCounter  = prometheus.GetOrCreateCounter("miner_blocks_mined_total", true)
	miningErrorsCounter = prometheus.GetOrCreateCounter("miner_errors_total", true)
	blockMiningTimer    = prometheus.GetOrCreateSummary("miner_block_mining_seconds")
)

func usesTimerDrivenSealing(engine consensus.Engine) bool {
	return engine == nil || engine.Type().UsesTimerDrivenSealing()
}

// blockSealNotifier is implemented by leader-driven consensus engines (HotStuff)
// so the miner can start a Proposal for a block right after it has sealed,
// persisted, and direct-pushed THAT block — binding propose and push to the same
// block (required by import-gated voting).
type blockSealNotifier interface {
	NotifyBlockSealed(hash, txHash types.Hash)
}

// siblingLookup is implemented by the blockchain so the leader can converge on a
// single same-height candidate (fix A): the lowest-hash locally-known block at a
// height extending a given parent. See BlockChain.LowestSiblingAtHeight.
type siblingLookup interface {
	LowestSiblingAtHeight(number uint64, parentHash types.Hash) (block.IBlock, bool)
	// BadSibling reports a block this node failed to validate; such a block
	// is never re-proposed, whichever path would have picked it.
	BadSibling(hash types.Hash) bool
}

// leaderAware is implemented by leader-driven consensus engines (HotStuff) so
// the miner can gate block production on leadership.
type leaderAware interface {
	IsCurrentLeader() bool
}

// shouldProduceNow reports whether this node should build a block now. Timer/PoW
// engines always produce. Leader-driven engines (HotStuff) produce only when this
// node is the current view's leader, so a single node produces each block (then
// direct-pushes it to peers); otherwise every node forks its own chain.
func (w *worker) shouldProduceNow() bool {
	if usesTimerDrivenSealing(w.engine) {
		return true
	}
	if la, ok := w.engine.(leaderAware); ok {
		return la.IsCurrentLeader()
	}
	return true
}

type task struct {
	receipts  []*block.Receipt
	state     *state.IntraBlockState
	block     block.IBlock
	createdAt time.Time
	nopay     map[types.Address]*uint256.Int
	// post is the block's post-state snapshot (adopt-appends mode only): a
	// chained speculative build reads the parent's effects from it while the
	// parent's write is still in flight.
	post *state.PostState
	// exec is the block's own execution result under deferred execution:
	// recorded with the sealed block so a chained build stamps it into the
	// next header before this block's write lands.
	exec *rawdb.ExecutedResult

	// Seal-path phase timings — OBSERVABILITY ONLY. The leader's
	// ViewStart→ProposalSent window spans three goroutines (commit → taskLoop →
	// resultLoop); carrying the timings on the task lets resultLoop emit ONE
	// line covering the whole window instead of three scattered ones.
	//
	// finalize/witness/assemble are written by commit() BEFORE the task is sent
	// on taskCh, so the channel send publishes them safely.
	finalize time.Duration // FinalizeAndAssemble: state root #1 on the isolated tree
	witness  time.Duration // witness generation + ZK submit (JMT chains only)
	assemble time.Duration // whole commit(): finalize + witness + entire-event

	// sealStart is written by taskLoop under w.mu (same critical section that
	// publishes the task into pendingTasks) and read by resultLoop under
	// w.mu.RLock, so it needs no separate synchronisation.
	sealStart time.Time
	// blsNanos is written by taskLoop after Seal returns and read by resultLoop
	// on another goroutine — atomic. Reads 0 if Seal's delivery goroutine beats
	// the store, which is harmless for a diagnostic counter.
	blsNanos atomic.Int64

	// S22 (docs/QS_BLOCK_TIME_BUDGET.md 6cs/6ct) seal-path stamps, extending
	// the pattern above further back: commitWork/the speculative park-hit
	// pair set these BEFORE the task ever reaches taskCh; taskLoop and
	// resultLoop only read them. All zero (and the "miner: seal path" line
	// that reads them is not emitted) unless N42_CONTENTION_DIAG=1
	// (contentionDiagEnabled, seal_path_diag.go) -- diagnostic only, no
	// behaviour change.
	//
	// triggerAt is the CONFIRMING trigger's own newWorkReq.enqueuedAt --
	// i.e. "build trigger received" (OutputViewChanged -> TriggerBlockProduction),
	// threaded through commitWorkGuarded/commitWork as a new parameter. For a
	// speculative-hit task this is the trigger that CONFIRMED it, not the
	// earlier speculative request that built it.
	triggerAt time.Time
	// buildBeginAt is commitWork's own entry (its local `start`) for
	// whichever commitWork call actually produced this task's block -- the
	// original speculative call for a hit, this same call for a fresh build.
	buildBeginAt time.Time
	// specParkedAt/specHitAt are set only for a task that went through the
	// speculative park-then-hit path (worker.go's takeSpecTask); both stay
	// zero for a fresh (non-speculative) build.
	specParkedAt time.Time
	specHitAt    time.Time
	// paceEnterAt/paceDur cover whichever paceBlock call actually applies to
	// this task (the hit path pays it when handing the parked block over;
	// the fresh path pays it inline before the fill).
	paceEnterAt time.Time
	paceDur     time.Duration
	// taskChSentAt is stamped immediately before this task is handed to
	// taskCh (both the hit path and the fresh path); taskQWaitMs
	// (sealStart - taskChSentAt) is derived from it and sealStart in the
	// final log line, not stored separately.
	taskChSentAt time.Time
}

type newWorkReq struct {
	interrupt *atomic.Int32
	noempty   bool
	timestamp int64
	// parentHash, when non-zero, pins the build to this parent — the
	// consensus-mandated HighQC block for a leader-driven proposal — instead
	// of the local chain head.
	parentHash types.Hash
	// speculative marks a cross-view speculative build: this node just voted
	// for the block at parentHash and expects to lead the NEXT view, whose
	// proposal will extend that very block on the happy path. The build runs
	// now — during the current view's vote rounds — and the result is parked
	// (not sealed, not proposed) until TriggerBlockProduction confirms the
	// parent. A wrong guess is discarded; a speculative request may be
	// dropped or interrupted at any time in favour of real work.
	speculative bool
	// enqueuedAt is when the request was handed to newWorkCh. runLoop reports
	// the wait between that moment and the start of commitWork -- the worker
	// goroutine is single, so a build that is already running (a speculative
	// guess, most often) delays the real request behind it, and that delay is
	// invisible in every phase timer the build itself keeps. Zero when the
	// producer did not set it; the log line is then omitted rather than
	// reporting a duration measured from the epoch.
	enqueuedAt time.Time
}

type generateParams struct {
	timestamp  uint64
	parentHash types.Hash
	coinbase   types.Address
	random     types.Hash
	noTxs      bool
}

type environment struct {
	ancestors mapset.Set
	family    mapset.Set
	tcount    int
	gasPool   *common.GasPool
	coinbase  types.Address

	header   *block.Header
	txs      []*transaction.Transaction
	receipts []*block.Receipt
}

func (env *environment) copy() *environment {
	cpy := &environment{
		ancestors: env.ancestors.Clone(),
		family:    env.family.Clone(),
		tcount:    env.tcount,
		coinbase:  env.coinbase,
		header:    block.CopyHeader(env.header),
		receipts:  copyReceipts(env.receipts),
	}
	if env.gasPool != nil {
		gasPool := *env.gasPool
		cpy.gasPool = &gasPool
	}

	cpy.txs = make([]*transaction.Transaction, len(env.txs))
	copy(cpy.txs, env.txs)
	return cpy
}

const (
	commitInterruptNone int32 = iota
	commitInterruptNewHead
	commitInterruptResubmit
	commitInterruptTimeout
)

const (
	minPeriodInterval      = 1 * time.Second // 1s
	staleThreshold         = 7
	resubmitAdjustChanSize = 10

	// maxRecommitInterval is the maximum time interval to recreate the sealing block with
	// any newly arrived transactions.
	maxRecommitInterval = 12 * time.Second

	intervalAdjustRatio = 0.1

	intervalAdjustBias = 200 * 1000.0 * 1000.0
)

var (
	errBlockInterruptedByNewHead  = errors.New("new head arrived while building block")
	errBlockInterruptedByRecommit = errors.New("recommit interrupt while building block")
	errBlockInterruptedByTimeout  = errors.New("timeout while building block")
)

// intervalAdjust represents a resubmitting interval adjustment.
type intervalAdjust struct {
	ratio float64
	inc   bool
}

type worker struct {
	minerConf conf.MinerConfig
	engine    consensus.Engine
	chain     common.IBlockChain
	txsPool   common.ITxsPool

	coinbase    types.Address
	chainConfig *params.ChainConfig

	isLocalBlock func(header *block.Header) bool
	pendingTasks map[types.Hash]*task

	// Cross-view speculative build state. specTask holds the parked result of
	// a speculative commitWork (guarded by specMu); specParent is the parent
	// the parked block extends. activeSpecInterrupt points at the interrupt of
	// a speculative build in flight, so a real production trigger can abort it
	// instead of queueing behind it.
	specMu              sync.Mutex
	specTask            *task
	specParent          types.Hash
	activeSpecInterrupt atomic.Pointer[atomic.Int32]
	// activeSpecParent is the parent of the speculative build currently
	// running, so a real trigger can tell "the guess in flight IS this block"
	// from "the guess is for another parent".
	activeSpecParent atomic.Pointer[types.Hash]

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

func (w *worker) runLoop() error {
	defer w.cancel()
	defer w.stop()
	for {
		select {
		case <-w.ctx.Done():
			return w.ctx.Err()
		case req := <-w.newWorkCh:
			if !req.enqueuedAt.IsZero() {
				log.Info("miner: work queue wait", "waitNs", time.Since(req.enqueuedAt).Nanoseconds(),
					"speculative", req.speculative)
			}
			err := w.commitWorkGuarded(req)
			if err != nil && req.speculative {
				// An aborted or failed guess costs nothing; do not alarm.
				log.Debug("speculative build abandoned", "err", err)
				err = nil
			}
			if err != nil {
				log.Error("runLoop error", "err", err)
			}
		}
	}
}

// commitWorkGuarded turns a panic inside one build into that build's error.
// runLoop stops the worker when it returns, so a panic that escaped from a
// build used to take the miner down for good (see handleSealed).
func (w *worker) commitWorkGuarded(req *newWorkReq) (err error) {
	defer func() {
		if r := recover(); r != nil {
			log.Errorf("panic in miner build; abandoning it: %v\nstack: %s", r, debug.Stack())
			err = fmt.Errorf("panic in miner build: %v", r)
		}
	}()
	return w.commitWork(req.interrupt, req.noempty, req.timestamp, req.parentHash, req.speculative, req.enqueuedAt)
}

func (w *worker) resultLoop() error {
	defer w.cancel()
	defer w.stop()

	for {
		select {
		case <-w.ctx.Done():
			return w.ctx.Err()
		case blk := <-w.resultCh:
			w.handleSealed(blk)
		}
	}
}

// handleSealed persists, pushes and proposes one sealed block. It runs under
// its own recover so a panic on one block costs that block, not the worker:
// resultLoop stops the worker when it returns, so a panic that escaped from
// here left the node silently skipping every leader view for the life of the
// process (round 35r: node3 lost its miner at 12:08 and missed 18 views of one
// leg, each a 6 s view timeout).
func (w *worker) handleSealed(blk block.IBlock) {
	// S22 (docs/QS_BLOCK_TIME_BUDGET.md 6cs/6ct): this is resultLoop's own
	// entry for this sealed result -- as early in this function as possible,
	// right after the two constant-time guards above, so it is a close proxy
	// for "resultLoop received this from resultCh". Used below (once the
	// pending task is found) to derive resQWaitMs: the gap between Seal()
	// itself finishing (task.sealStart + task.blsNanos) and this stamp is the
	// time this exact sealed block spent waiting for resultLoop to be free --
	// i.e. queued behind whatever handleSealed(v) is still doing (its own
	// WriteBlockWithState, most likely) on this SAME single goroutine.
	var tHandleSealedEnter time.Time
	if contentionDiagEnabled {
		tHandleSealedEnter = time.Now()
	}
	defer func() {
		if r := recover(); r != nil {
			id := "nil"
			if blk != nil {
				id = blk.Hash().Hex()[:12]
			}
			log.Errorf("panic handling sealed block %s; dropping it: %v\nstack: %s", id, r, debug.Stack())
			miningErrorsCounter.Inc()
		}
	}()
	if blk == nil {
		return
	}
	blockNumber, err := requireBlockNumber(blk, "block number unavailable")
	if err != nil {
		log.Error("Ignoring sealed block", "err", err, "hash", blk.Hash())
		return
	}

	// Short circuit when receiving duplicate result caused by resubmitting.
	if w.chain.HasBlock(blk.Hash(), blockNumber.Uint64()) {
		if usesTimerDrivenSealing(w.engine) {
			return
		}
		if sl, ok := w.chain.(siblingLookup); ok && sl.BadSibling(blk.Hash()) {
			log.Warn("miner: deterministic rebuild equals a block this node failed to validate; not re-proposing it",
				"number", blockNumber.Uint64(), "hash", blk.Hash().Hex()[:12])
			return
		}
		// Leader-driven (HotStuff): the deterministic rebuild collides
		// with a block already imported in an EARLIER round (restart at a
		// stalled height re-enters overlapping views, so the identical
		// candidate is already in the DB — imported but never committed).
		// It still must be proposed for THIS view: skip the state
		// re-write, but re-push and notify so the Proposal goes out.
		log.Info("miner: re-proposing already-imported block", "number", blockNumber.Uint64(), "hash", blk.Hash().Hex()[:12])
		if err := w.chain.SealedBlock(blk); err != nil {
			log.Warn("miner: re-push of existing block failed", "err", err)
		}
		if bsn, ok := w.engine.(blockSealNotifier); ok {
			bsn.NotifyBlockSealed(blk.Hash(), blk.TxHash())
		}
		return
	}

	parentHash := blk.ParentHash()
	w.rememberSealed(blk)

	// Fix A — cross-view same-height convergence: if a strictly-lower-hash
	// sibling extending THIS parent is already known locally (sealed here or
	// received from another view's leader), re-propose it instead of
	// importing this divergent higher-hash candidate. When a height misses
	// its first commit, each later view's leader otherwise builds a distinct
	// block (different ConsensusEvidence per view), scattering import-gated
	// votes so no candidate reaches the 2f+1 quorum — the multi-minute
	// convergence stall. Deterministic lowest-hash selection (matches the
	// forkchoice tie-break) makes every leader pick the SAME candidate, so
	// votes stack on one block. See docs/hotstuff-view-convergence-followup.md.
	// Only at the single legitimate proposal height (committed head + 1):
	// a stale leader building at an already-committed height must NOT be
	// redirected to a dead sibling there — that re-injects a candidate
	// conflicting with the committed block (observed live at 13014242:
	// sibling re-proposal at a committed height seeded a canonical-chain
	// discontinuity). Let the normal import/duplicate path absorb it.
	if sl, ok := w.chain.(siblingLookup); ok &&
		blockNumber.Uint64() == w.chain.CurrentBlock().Number64().Uint64()+1 {
		if low, exists := sl.LowestSiblingAtHeight(blockNumber.Uint64(), parentHash); exists &&
			low.Hash() != blk.Hash() &&
			bytes.Compare(low.Hash().Bytes(), blk.Hash().Bytes()) < 0 {
			log.Info("miner: converging on lowest-hash sibling; re-proposing it",
				"number", blockNumber.Uint64(), "parent", parentHash.Hex()[:12],
				"kept", low.Hash().Hex()[:12], "dropped", blk.Hash().Hex()[:12])
			if err := w.chain.SealedBlock(low); err != nil {
				log.Warn("miner: re-push of lowest sibling failed", "err", err)
			}
			if bsn, ok := w.engine.(blockSealNotifier); ok {
				bsn.NotifyBlockSealed(low.Hash(), low.TxHash())
			}
			return
		}
	}

	// Height-level single-candidate guard (HotStuff-2): keep only the first
	// block sealed on a given parent. A later view's leader re-entering Seal
	// on the same still-uncommitted parent produces a divergent sibling;
	// importing it would fork the applied state and thrash the branch-switch
	// unwind between the two siblings forever. Re-propose the kept block for
	// THIS view instead — it is already imported and is what consensus is
	// building on. Checked BEFORE import so the sibling never touches state.
	if kept := w.firstSealedOnParent(parentHash); kept != nil && kept.Hash() != blk.Hash() {
		// S34 (docs/OPEN_ISSUES.md, the stale-re-proposal item;
		// docs/QS_BLOCK_TIME_BUDGET.md 6dk): "kept" is only ever the FIRST
		// block sealed on parentHash -- it says nothing about whether that
		// height has since been committed and written. Round 35zzzk view
		// 6557: a leftover handleSealed call for a divergent sibling at the
		// height that had JUST been committed (parentHash was the tip BEFORE
		// that commit) found "kept" already equal to the just-committed
		// block itself and re-proposed it -- an already-decided block,
		// re-injected as if it were a fresh candidate, which the engine's
		// own self-justify guard (proposal.go) now also refuses. This check
		// avoids even attempting it: if the chain's own current head is
		// already at or past this height, the height was decided while this
		// stale task was in flight, so nothing here is worth proposing.
		// Best-effort, not the safety net -- CurrentBlock() reflects a
		// completed WRITE, which can in principle still be in flight when
		// this runs; the engine's own unconditional guard is what actually
		// closes the defect regardless of this check's timing.
		if w.chain.CurrentBlock().Number64().Uint64() >= blockNumber.Uint64() {
			log.Info("miner: not re-proposing a sibling at an already-decided height",
				"number", blockNumber.Uint64(), "parent", parentHash.Hex()[:12],
				"kept", kept.Hash().Hex()[:12], "dropped", blk.Hash().Hex()[:12],
				"currentBlock", w.chain.CurrentBlock().Number64().Uint64())
			return
		}
		log.Info("miner: suppressing divergent same-height sibling; re-proposing first sealed block",
			"number", blockNumber.Uint64(), "parent", parentHash.Hex()[:12],
			"kept", kept.Hash().Hex()[:12], "dropped", blk.Hash().Hex()[:12])
		if err := w.chain.SealedBlock(kept); err != nil {
			log.Warn("miner: re-push of kept sibling failed", "err", err)
		}
		if bsn, ok := w.engine.(blockSealNotifier); ok {
			bsn.NotifyBlockSealed(kept.Hash(), kept.TxHash())
		}
		return
	}

	// S26 (docs/OPEN_ISSUES.md "A quorum-committed block that no node
	// stored"): record THIS block as the sole candidate for parentHash now,
	// at seal time -- not after its write completes (writeAndFinish used to
	// do this, "after a successful import"). recordSealedOnParent is what the
	// guard above reads; recording it late left a window, widest with
	// N42_LEADER_WRITE_ASYNC=1 but present on any slow write, where a second,
	// independently-building seal on the SAME parent (a parked speculative
	// task finishing while this block's write is still in flight) found
	// firstSealedOnParent empty and proceeded to push/propose a divergent
	// sibling instead of being suppressed here. Moving the record here closes
	// that window; the vote-rule fix in the hotstuff package is the
	// independent, sufficient backstop if this one is ever missed.
	w.recordSealedOnParent(parentHash, blk)

	var (
		sealhash = w.engine.SealHash(blk.Header())
		hash     = blk.Hash()
	)
	w.mu.RLock()
	task, exist := w.pendingTasks[sealhash]
	var sealStart time.Time
	if exist {
		sealStart = task.sealStart
	}
	w.mu.RUnlock()
	if !exist {
		log.Error("Block found but no relative pending task", "number", blockNumber.Uint64(), "sealhash", sealhash, "hash", hash)
		return
	}

	// Deep copy receipts and set block location fields to prevent write-write conflicts
	// when different blocks share the same sealhash.

	// Push-before-write (N42_PUSH_BEFORE_WRITE, off by default): hand
	// the sealed block to peers BEFORE committing it, so their import
	// runs alongside this node's write instead of after it. The
	// stale-seal predicate the write path would apply is evaluated
	// first, against a read snapshot, so a block the write is about to
	// reject still reaches nobody. The Proposal is NOT moved: it stays
	// after a successful write below.
	var tPush time.Time
	var dPush time.Duration
	pushedEarly := false
	// The write path's stale-seal gate, evaluated first against a read
	// snapshot and decisive: a stale candidate is dropped before its write,
	// not only before its push. The gate inside the write runs only for
	// isolated QMDB seals; a build whose speculative reload failed fell back
	// to the live computer, so its stale block reached CommitBlock, read
	// through the build's rolled-back transaction and panicked the worker
	// (round 35r, node3).
	var tCheckEnter time.Time
	var dCheck time.Duration
	c, hasCheck := w.chain.(sealParentChecker)
	if hasCheck {
		if contentionDiagEnabled {
			tCheckEnter = time.Now()
		}
		cerr := w.checkSealParentApplied(c, blk, parentHash)
		if contentionDiagEnabled {
			dCheck = time.Since(tCheckEnter)
		}
		if cerr != nil {
			log.Info("miner: sealed block is stale before its write; dropping",
				"number", blockNumber.Uint64(), "hash", hash.Hex()[:12], "err", cerr)
			return
		}
	}
	if PushBeforeWrite() {
		// Fail safe: without the stale-seal check there is no way to know
		// the write would accept this block, so keep today's order rather
		// than broadcast one the write may reject.
		canPush := hasCheck
		if !hasCheck {
			log.Warn("push-before-write: chain cannot answer the stale-seal check; keeping write-then-push")
		}
		if canPush {
			tPush = time.Now()
			if perr := w.chain.SealedBlock(blk); perr != nil {
				log.Error("push-before-write: broadcast failed", "err", perr)
			} else {
				pushedEarly = true
			}
			dPush = time.Since(tPush)
		}
	}
	// Propose-before-write (N42_PROPOSE_BEFORE_WRITE, off by default,
	// needs the early push): hand the Proposal to the engine as soon
	// as the body is with the peers, so the prepare round and the
	// followers' imports run beside this node's write. Round 33: with
	// only the push moved, the follower's import started earlier but
	// the view still waited for the Proposal that trailed a ~150 ms
	// write at ~95k transactions. The engine's onBlockReady reads
	// nothing from the database; the leader's durable consensus state
	// is its vote journal, written before any signature leaves; and a
	// write that fails after this point leaves a block the leader
	// itself must re-fetch, which the followers decide on regardless.
	proposedEarly := false
	var tProposeEarly time.Time
	var dProposeEarly time.Duration
	if pushedEarly && ProposeBeforeWrite() {
		if bsn, ok := w.engine.(blockSealNotifier); ok {
			if contentionDiagEnabled {
				tProposeEarly = time.Now()
			}
			bsn.NotifyBlockSealed(blk.Hash(), blk.TxHash())
			if contentionDiagEnabled {
				dProposeEarly = time.Since(tProposeEarly)
			}
			proposedEarly = true
		}
	}

	// The receipts copy (163k allocations) is needed by the write, not by
	// the push or the Proposal: done after both leave.
	var tCopyStart time.Time
	if contentionDiagEnabled {
		tCopyStart = time.Now()
	}
	receipts := make([]*block.Receipt, len(task.receipts))
	var logs []*block.Log
	for i, taskReceipt := range task.receipts {
		receipt := new(block.Receipt)
		*receipt = *taskReceipt
		receipt.BlockHash = hash
		receipt.BlockNumber = blk.Number64()
		receipt.TransactionIndex = uint(i)

		receipt.Logs = make([]*block.Log, len(taskReceipt.Logs))
		for j, taskLog := range taskReceipt.Logs {
			lg := new(block.Log)
			*lg = *taskLog
			lg.BlockHash = hash
			receipt.Logs[j] = lg
		}
		receipts[i] = receipt
		logs = append(logs, receipt.Logs...)
	}
	var dCopy time.Duration
	if contentionDiagEnabled {
		dCopy = time.Since(tCopyStart)
	}

	if task.exec != nil {
		if eh, ok := w.chain.(interface {
			RememberExecutedResult(types.Hash, rawdb.ExecutedResult)
		}); ok {
			eh.RememberExecutedResult(blk.Hash(), *task.exec)
		}
	}

	// S23 (docs/QS_BLOCK_TIME_BUDGET.md 6ct/6cu, N42_LEADER_WRITE_ASYNC=1):
	// everything from here on -- the S19 journal-latch wait, the write
	// itself, and everything that today assumes "the write already
	// returned" (pendingTasks cleanup, counters, the seal-path/propose-
	// phases/"Successfully sealed" log lines, the ChainHighestBlock event) --
	// moves into writeAndFinish, called either
	// synchronously here (switch off: byte-for-byte today's control flow,
	// same goroutine, same order) or from the dedicated writer goroutine
	// (switch on: resultLoop returns immediately after Enqueue, free to
	// receive and push/propose the NEXT sealed result without waiting for
	// THIS write).
	job := &writeJob{
		blk: blk, receipts: receipts, logs: logs, task: task,
		sealhash: sealhash, hash: hash, parentHash: parentHash, blockNumber: blockNumber.Uint64(),
		sealStart: sealStart, tHandleSealedEnter: tHandleSealedEnter,
		tCheckEnter: tCheckEnter, dCheck: dCheck,
		tCopyStart: tCopyStart, dCopy: dCopy,
		tPush: tPush, dPush: dPush, pushedEarly: pushedEarly,
		tProposeEarly: tProposeEarly, dProposeEarly: dProposeEarly, proposedEarly: proposedEarly,
	}
	if w.asyncWriter != nil {
		w.asyncWriter.Enqueue(job)
		return
	}
	w.writeAndFinish(job)
}

// writeAndFinish runs the S19 journal-latch wait, WriteBlockWithState
// itself, and everything handleSealed does today only after a successful
// write -- extracted so the switch-off (synchronous, called directly from
// handleSealed) and switch-on (called from asyncBlockWriter.run, S23,
// N42_LEADER_WRITE_ASYNC=1) paths share IDENTICAL logic. Returns whether
// the write succeeded (informational only; the caller does not currently
// branch on it -- a failed write already logs and returns exactly as
// handleSealed always has).
func (w *worker) writeAndFinish(job *writeJob) bool {
	blk, task := job.blk, job.task
	hash, sealhash := job.hash, job.sealhash
	blockNumber := job.blockNumber
	receipts, logs := job.receipts, job.logs
	sealStart, tHandleSealedEnter := job.sealStart, job.tHandleSealedEnter
	tCheckEnter, dCheck := job.tCheckEnter, job.dCheck
	tCopyStart, dCopy := job.tCopyStart, job.dCopy
	tPush, dPush, pushedEarly := job.tPush, job.dPush, job.pushedEarly
	tProposeEarly, dProposeEarly, proposedEarly := job.tProposeEarly, job.dProposeEarly, job.proposedEarly

	// N42_LEADER_WRITE_AFTER_JOURNAL (S19, docs/QS_BLOCK_TIME_BUDGET.md 6cn/
	// 6co): delay the START of the write below until this node's own
	// journalCommitVote for THIS block has succeeded (or the configured
	// timeout elapses, or the view is abandoned), so the journal's small
	// MDBX write finds the writer idle instead of queueing behind this one.
	// Only meaningful when the Proposal already left (proposedEarly): that is
	// what starts the chain of events (onBlockReady -> tryFormPrepareQC ->
	// journalCommitVote) this wait is waiting on. Without an early Proposal
	// (switch off, or push-before-write off/failed) NotifyBlockSealed has not
	// even run yet at this point -- there is nothing to wait for, so this is
	// skipped rather than spending the full timeout on a signal that cannot
	// possibly arrive yet. Never holds w.mu or any chain lock while waiting.
	// S23: this wait now runs on the dedicated writer goroutine when the
	// switch is on, not on resultLoop -- moving it there too (not just the
	// write) is what keeps resultLoop free the whole time, matching the
	// task's own requirement that the S19 latch wait "must NOT block
	// resultLoop either".
	var lwWait time.Duration
	lwWhy := "off"
	if LeaderWriteAfterJournalOn() {
		if !proposedEarly {
			lwWhy = "not-proposed-yet"
		} else if cw, ok := w.engine.(commitVoteJournalWaiter); ok {
			lwWait, lwWhy = cw.WaitForCommitVoteJournal(hash, LeaderWriteAfterJournalTimeout())
		} else {
			lwWhy = "unsupported"
		}
	}

	tWrite := time.Now()
	err := w.chain.WriteBlockWithState(blk, receipts, task.state, task.nopay)
	dWrite := time.Since(tWrite)
	if err != nil {
		if proposedEarly {
			log.Warn("propose-before-write: the write failed AFTER the Proposal left; this node re-fetches its own block if the fleet commits it",
				"number", blk.Number64().Uint64(), "hash", blk.Hash().Hex()[:12], "err", err)
		}
		if errors.Is(err, internal.ErrStaleSeal) {
			// The applied head moved past this seal's parent while it
			// was in flight (a competing same-height candidate won, OR --
			// S23 only -- an earlier queued write of this node's OWN onto
			// the same parent chain failed; see pendingWrite's doc comment,
			// async_write.go, for why this path is exactly as safe here as
			// it always has been for an ordinary sibling race).
			log.Info("Sealed block lost to a competing candidate; dropping",
				"number", blk.Number64().Uint64(), "hash", blk.Hash().Hex()[:12])
			return false
		}
		log.Error("Failed writing block to chain", "err", err)
		if bc, ok := w.chain.(*internal.BlockChain); ok {
			bc.ForgetSealedHeader(blk.Hash())
		}
		miningErrorsCounter.Inc()
		return false
	}
	w.mu.Lock()
	delete(w.pendingTasks, sealhash)
	w.mu.Unlock()
	blocksMinedCounter.Inc()
	blockMiningTimer.UpdateDuration(task.createdAt)

	// S22 (docs/QS_BLOCK_TIME_BUDGET.md 6cs/6ct): ONE line covering every
	// named step from this view's build trigger to the push, plus the two
	// queue waits U1 asked for directly. sealStart/blsNanos double as
	// "engine.Seal enter"/"BLS sign start-end" (Seal's own call is bracketed
	// by exactly these two existing timers, taskLoop's tSeal a few lines
	// above sealStart); resQWaitMs is the gap between Seal() itself finishing
	// (sealStart+blsNanos) and resultLoop actually receiving this result
	// (tHandleSealedEnter) -- queued behind whatever handleSealed(v) was
	// still doing (its own write, most likely) on this single goroutine.
	// taskQWaitMs is the same idea for taskCh (taskChSentAt -> sealStart).
	// S23 adds wqWaitMs/wqDepth: the NEW queue wait, this write's own
	// enqueue call on resultLoop (zero when the switch is off, or when this
	// write's own enqueue did not have to block).
	if contentionDiagEnabled {
		blsEnd := sealStart.Add(time.Duration(task.blsNanos.Load()))
		log.Info("miner: seal path",
			"number", blockNumber,
			"triggerTMs", tMs(task.triggerAt),
			"buildBeginTMs", tMs(task.buildBeginAt),
			"specParkedTMs", tMs(task.specParkedAt),
			"specHitTMs", tMs(task.specHitAt),
			"paceEnterTMs", tMs(task.paceEnterAt),
			"paceDurMs", task.paceDur.Milliseconds(),
			"taskSentTMs", tMs(task.taskChSentAt),
			"taskQWaitMs", waitMs(task.taskChSentAt, sealStart),
			"taskPickedTMs", tMs(sealStart),
			"sealEnterTMs", tMs(sealStart),
			"checkEnterTMs", tMs(tCheckEnter),
			"checkExitTMs", tMs(tCheckEnter.Add(dCheck)),
			"blsStartTMs", tMs(sealStart),
			"blsEndTMs", tMs(blsEnd),
			"resultRecvTMs", tMs(tHandleSealedEnter),
			"resQWaitMs", waitMs(blsEnd, tHandleSealedEnter),
			"copyStartTMs", tMs(tCopyStart),
			"copyEndTMs", tMs(tCopyStart.Add(dCopy)),
			"pushStartTMs", tMs(tPush),
			"pushEndTMs", tMs(tPush.Add(dPush)),
			"proposeStartTMs", tMs(tProposeEarly),
			"proposeEndTMs", tMs(tProposeEarly.Add(dProposeEarly)),
			"lwWaitMs", lwWait.Milliseconds(),
			"lwWhy", lwWhy,
			"writeStartTMs", tMs(tWrite),
			"writeEndTMs", tMs(tWrite.Add(dWrite)),
			"wqWaitMs", job.wqWaitMs,
			"wqDepth", job.wqDepth,
		)
	}

	body := blk.Body()
	var verifierCount, rewardCount int
	if body != nil {
		verifierCount = len(body.Verifier())
		rewardCount = len(body.Reward())
	}
	blockSignGauge.Set(uint64(verifierCount))

	if len(logs) > 0 {
		event.GlobalEvent.Send(common.NewLogsEvent{Logs: logs})
	}

	log.Info("🔨 Successfully sealed new block",
		"sealhash", sealhash,
		"hash", hash,
		"number", blockNumber,
		"used gas", blk.GasUsed(),
		"diff", blk.Difficulty().Uint64(),
		"headerTime", time.Unix(int64(blk.Time()), 0).Format(time.RFC3339),
		"verifierCount", verifierCount,
		"rewardCount", rewardCount,
		"elapsed", common.PrettyDuration(time.Since(task.createdAt)),
		"txs", len(blk.Transactions()))

	if !pushedEarly {
		tPush = time.Now()
		if err = w.chain.SealedBlock(blk); err != nil {
			log.Error("Failed Broadcast block to p2p network", "err", err)
			return true
		}
		dPush = time.Since(tPush)
	}
	// For leader-driven consensus (HotStuff), start the Proposal for THIS
	// exact sealed block — the one we just persisted and direct-pushed — so
	// the proposed (and committed) block is byte-for-byte what followers
	// receive and import. Doing this here (not in Seal) binds propose↔push to
	// the same block, which import-gated voting requires.
	tNotify := time.Now()
	if !proposedEarly {
		if bsn, ok := w.engine.(blockSealNotifier); ok {
			bsn.NotifyBlockSealed(blk.Hash(), blk.TxHash())
		}
	}
	dNotify := time.Since(tNotify)

	// Leader seal→propose breakdown: ONE line per produced block covering
	// the window "miner: build phases" stops measuring at (it is emitted
	// before commit()) up to the Proposal being handed to the engine.
	// `write` is the whole WriteBlockWithState — "blockwrite phases"
	// (blockchain_write.go) splits it further. `lwWait`/`lwWhy` (S19,
	// N42_LEADER_WRITE_AFTER_JOURNAL) is the wait spliced in just before
	// `write` starts, if the switch is on: how long this write's own start
	// was delayed, and why it stopped waiting ("journal": the commit-vote
	// journal succeeded; "abandoned": the view timed out before that;
	// "timeout": neither happened inside the configured ceiling; "off": the
	// switch is unset; "not-proposed-yet": no early Proposal to wait on;
	// "unsupported": w.engine does not implement commitVoteJournalWaiter).
	log.Info("miner: propose phases",
		"n", blockNumber, "txs", len(blk.Transactions()),
		"finalize", task.finalize, "witness", task.witness, "assemble", task.assemble,
		"bls", time.Duration(task.blsNanos.Load()), "seal2res", time.Since(sealStart),
		"write", dWrite, "push", dPush, "pushedEarly", pushedEarly, "proposedEarly", proposedEarly, "notify", dNotify,
		"lwWait", lwWait, "lwWhy", lwWhy,
		"total", time.Since(task.createdAt), "tMs", time.Now().UnixMilli())

	// S26: recordSealedOnParent now runs in handleSealed, at seal time,
	// before push/propose -- see the comment there. Recording it again here
	// would be a no-op (first write wins) kept only as history.
	if concrete, ok := blk.(*block.Block); ok {
		event.GlobalEvent.Send(common.ChainHighestBlock{Block: *concrete, Inserted: true})
	}
	return true
}

// firstSealedOnParent returns the first block this node sealed+imported on the
// given parent, or nil. Used to suppress a later view's divergent same-height
// sibling (see the sealedOnParent field doc).
// rememberSealed records a just-sealed block by hash (bounded to the last
// 16 by number) so a speculative build can extend it before it is written.
func (w *worker) rememberSealed(blk block.IBlock) {
	if blk == nil {
		return
	}
	if bc, ok := w.chain.(*internal.BlockChain); ok {
		if h, ok := blk.Header().(*block.Header); ok {
			bc.RememberSealedHeader(h)
		}
	}
	sealhash := w.engine.SealHash(blk.Header())
	w.mu.Lock()
	defer w.mu.Unlock()
	w.sealedByHash[blk.Hash()] = blk
	if t := w.pendingTasks[sealhash]; t != nil {
		if t.post != nil {
			w.sealedPost[blk.Hash()] = t.post
		}
		if t.exec != nil {
			w.sealedExec[blk.Hash()] = *t.exec
		}
	}
	if len(w.sealedByHash) > 16 {
		var oldest types.Hash
		oldestNum := ^uint64(0)
		for h, b := range w.sealedByHash {
			if n := b.Number64(); n != nil && n.Uint64() < oldestNum {
				oldestNum, oldest = n.Uint64(), h
			}
		}
		delete(w.sealedByHash, oldest)
		delete(w.sealedPost, oldest)
		delete(w.sealedExec, oldest)
	}
}

// unwrittenOwnPostStates returns the post-state snapshots of the own sealed
// blocks from parent down to (excluding) the applied lineage, newest first.
// nil when any block on that path is not ours or has no snapshot: the build
// then waits for the parent's write like a plain speculative build.
func (w *worker) unwrittenOwnPostStates(parent types.Hash, bc *internal.BlockChain) []*state.PostState {
	var out []*state.PostState
	h := parent
	for range 16 {
		own := w.ownSealed(h)
		if own == nil || own.Number64() == nil {
			return nil
		}
		w.mu.RLock()
		post := w.sealedPost[h]
		w.mu.RUnlock()
		if post == nil {
			return nil
		}
		out = append(out, post)
		num := own.Number64().Uint64()
		if num == 0 || bc.HasAppliedBlock(own.ParentHash(), num-1) {
			return out
		}
		h = own.ParentHash()
	}
	return nil
}

func (w *worker) ownSealed(hash types.Hash) block.IBlock {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.sealedByHash[hash]
}

// checkSealParentApplied is handleSealed's stale-seal gate, factored out so
// the S23 bypass decision is independently testable. c.CheckSealParentApplied
// is the real, authoritative-as-of-last-commit DB check; the bypass (when
// N42_LEADER_WRITE_ASYNC=1) additionally treats blk's parent as applied when
// it matches whatever w.asyncWriter itself most recently accepted for
// writing -- see pendingWrite's doc comment (async_write.go) for why an
// optimistic pass here is safe even when it turns out to be wrong.
func (w *worker) checkSealParentApplied(c sealParentChecker, blk block.IBlock, parentHash types.Hash) error {
	if w.asyncWriter != nil {
		if expHash, _, ok := w.asyncWriter.ExpectedParent(); ok && expHash == parentHash {
			return nil
		}
	}
	return c.CheckSealParentApplied(blk)
}

// ownPendingSpeculation reports whether this speculative build extends a
// block this node sealed and has not applied yet. The miner tree already
// holds that block's post-state (its own build), so the build neither waits
// for the write nor aligns the applied branch -- the write of the parent
// and the hit check (AppliedHeadIs at hand-over) keep it honest, and a lost
// parent takes the child down with it (PeelAll on the next build).
func (w *worker) ownPendingSpeculation(speculative bool, parent types.Hash, bc *internal.BlockChain) bool {
	if !speculative || bc == nil {
		return false
	}
	own := w.ownSealed(parent)
	if own == nil || own.Number64() == nil {
		return false
	}
	return !bc.AppliedHeadIsExactly(parent, own.Number64().Uint64())
}

func (w *worker) firstSealedOnParent(parent types.Hash) block.IBlock {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.sealedOnParent[parent]
}

// recordSealedOnParent records blk as the sole candidate for its parent (first
// SEAL wins -- called from handleSealed before push/propose, S26; a write that
// is still in flight when a second, independently-built seal on the same
// parent arrives must not leave this map empty) and prunes entries older than
// the 256-block branch-switch window — a sibling that far back can no longer
// be unwound/switched, so it needs no suppression record.
func (w *worker) recordSealedOnParent(parent types.Hash, blk block.IBlock) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, ok := w.sealedOnParent[parent]; !ok {
		w.sealedOnParent[parent] = blk
	}
	if len(w.sealedOnParent) <= 512 {
		return
	}
	number := blk.Number64().Uint64()
	if number <= 256 {
		return
	}
	cutoff := number - 256
	for p, b := range w.sealedOnParent {
		if b.Number64().Uint64() < cutoff {
			delete(w.sealedOnParent, p)
		}
	}
}

func (w *worker) taskLoop() error {
	defer w.cancel()
	defer w.stop()

	var (
		stopCh chan struct{}
		prev   types.Hash
	)

	interrupt := func() {
		if stopCh != nil {
			close(stopCh)
			stopCh = nil
		}
	}

	for {
		select {
		case <-w.ctx.Done():
			return w.ctx.Err()
		case task := <-w.taskCh:

			if w.newTaskHook != nil {
				w.newTaskHook(task)
			}

			sealHash := w.engine.SealHash(task.block.Header())
			if sealHash == prev && usesTimerDrivenSealing(w.engine) {
				// Timer/PoW engines: the recommit loop re-submits identical work —
				// skip. Leader-driven engines (HotStuff) must NOT dedup here: block
				// time is deterministic (parent time + period), so a NEW view's
				// leader legitimately rebuilds the IDENTICAL block and must re-enter
				// Seal to propose it in that view. Swallowing the retry wedges the
				// chain: the first build (possibly as a follower) poisons `prev`,
				// and every later leader's rebuild is silently dropped — no proposal
				// ever goes out while views time out forever (observed live on a
				// 7-node restart at a stalled height).
				continue
			}
			interrupt()
			stopCh, prev = make(chan struct{}), sealHash
			w.mu.Lock()
			// Same critical section that publishes the task, so resultLoop's
			// pendingTasks read under RLock also observes sealStart.
			task.sealStart = time.Now()
			w.pendingTasks[sealHash] = task
			w.mu.Unlock()

			hash := task.block.Hash()
			stateRoot := task.block.StateRoot()
			tSeal := time.Now()
			// NOTE: do NOT push task.block here — it is unsealed. HotStuff seals the
			// block (appends the BLS sig to Extra), changing its hash, and proposes/
			// commits the SEALED block. The reliable direct push happens in
			// resultLoop after sealing, so followers receive the same sealed block
			// that consensus commits (otherwise CommitToCanonical can't find it).
			if err := w.engine.Seal(w.chain, task.block, w.resultCh, stopCh); err != nil {
				w.mu.Lock()
				delete(w.pendingTasks, sealHash)
				w.mu.Unlock()
				log.Warn("delete task", "sealHash", sealHash, "hash", hash, "stateRoot", stateRoot, "err", err)
				if errors.Is(err, consensus.ErrNotEnoughSign) {
					time.Sleep(1 * time.Second)
					w.startCh <- struct{}{}
				}
			} else {
				task.blsNanos.Store(int64(time.Since(tSeal)))
				log.Debug("send task", "sealHash", sealHash, "hash", hash, "stateRoot", stateRoot)
			}
		}
	}
}

// paceBlock throttles the wall-clock seal rate to a fixed interval on an
// absolute grid (drift-free over long runs). Set the anchor on the first paced
// block, then hold each subsequent block until its grid slot
// (anchor + (num-anchorNum)*interval). A block that runs late has a slot
// already in the past, so it seals immediately and the schedule self-corrects
// with no accumulated drift. Interval 0 disables the throttle entirely
// (produce flat out — e.g. benchmarking). Consensus is unaffected:
// header.Time stays the deterministic parent.Time+period value.
func (w *worker) paceBlock(num uint64) error {
	iv := w.minerConf.BlockIntervalMs
	if iv <= 0 || !w.isRunning() {
		return nil
	}
	interval := time.Duration(iv) * time.Millisecond
	if !w.pacingAnchorSet || num < w.pacingAnchorNum {
		w.pacingAnchorWall, w.pacingAnchorNum, w.pacingAnchorSet = time.Now(), num, true
		return nil
	}
	target := w.pacingAnchorWall.Add(time.Duration(num-w.pacingAnchorNum) * interval)
	wait := time.Until(target)
	// Cap a single wait at one interval. With rotating leaders each node
	// seals only every Nth block, so its own grid can demand up to
	// N*interval in one go — long enough to trip the view timeout and
	// start a TC storm. One-interval waits still pace the NETWORK to
	// ~interval per block (every leader holds its slot), and the solo
	// case (num advances by 1 per seal) is unaffected.
	if wait > interval {
		wait = interval
		w.pacingAnchorWall, w.pacingAnchorNum = time.Now().Add(interval), num
	}
	// Re-anchor if we have fallen more than one full interval behind the
	// grid (clock jump, long stall) so catch-up bursts stay bounded.
	if wait < -interval {
		w.pacingAnchorWall, w.pacingAnchorNum = time.Now(), num
	} else if wait > 0 {
		// Report the throttle's actual cost. paceBlock sits between commitWork
		// and sealStart -- on the speculative-hit path (91% of builds) it runs
		// immediately before the task reaches taskCh -- which is inside the
		// 247-373 ms window that ViewStart -> seal start has never accounted
		// for. Round 16 measured the two candidates it was aimed at, the
		// consensus gates and the miner's work queue, at 0.03 ms and 0.01 ms
		// combined, so the time is somewhere in here. Whether this throttle
		// fires under load has been ARGUED twice from the cadence and never
		// measured; the argument says it cannot (a late block's slot is in the
		// past, so wait is negative), and the decay phase's exact 250 ms/block
		// says it certainly does when blocks are cheap.
		log.Info("miner: pacing wait", "num", num, "waitNs", wait.Nanoseconds())
		timer := time.NewTimer(wait)
		select {
		case <-timer.C:
		case <-w.ctx.Done():
			timer.Stop()
			return w.ctx.Err()
		}
	}
	return nil
}

// takeSpecTask returns the parked speculative task when it extends parent, and
// clears the parking spot either way — a mismatch means the guess is stale.
func (w *worker) takeSpecTask(parent types.Hash) *task {
	w.specMu.Lock()
	defer w.specMu.Unlock()
	t := w.specTask
	match := t != nil && w.specParent == parent
	w.specTask, w.specParent = nil, types.Hash{}
	if match {
		return t
	}
	return nil
}

func (w *worker) commitWork(interrupt *atomic.Int32, noempty bool, timestamp int64, parentHash types.Hash, speculative bool, triggerAt time.Time) error {
	log.Info("miner: commitWork begin", "speculative", speculative) // diagnostic: pairs with "build triggered"
	start := time.Now()

	// S11 diagnostics (N42_BUILD_STALL_DIAG=1; docs/QS_BLOCK_TIME_BUDGET.md
	// 6by): pf accumulates named step timings between here and the start of
	// the fill; wd dumps every goroutine's stack if the fill is not reached
	// within a few seconds. Both nil (zero cost) when the switch is off. wd
	// is disarmed by the deferred Cancel on every return path -- a normal
	// completion cancels it explicitly at the fill boundary in
	// fillTransactions; any early return here or in a callee just lets this
	// defer catch it.
	var pf *prefillTimes
	if buildStallDiagEnabled {
		pf = &prefillTimes{buildStart: start}
	}
	wd := newBuildStallWatchdog(buildStallDiagEnabled, log.LogDir())
	defer wd.Cancel()

	w.mu.RLock()
	coinbase := w.coinbase
	w.mu.RUnlock()
	if w.isRunning() && coinbase == (types.Address{}) {
		return errors.New("coinbase is empty")
	}

	// Speculative HIT: the previous view voted for parentHash, the build ran
	// during that view's vote rounds, and consensus now confirms the guess.
	// Pay the pacing wait the parked build skipped, then hand the block
	// straight to the sealer — the whole build phase is off the critical path.
	if !speculative && parentHash != (types.Hash{}) {
		if st := w.takeSpecTask(parentHash); st != nil {
			// The parked task was built on parentHash, but the applied head
			// may have moved since (another view's proposal at the same
			// height was applied locally and later abandoned). The rebuild
			// path below re-aligns the applied branch; this fast path does
			// not, so a stale seal would be rejected by
			// checkQMDBLeaderSealParent with nothing left to rebuild — the
			// view is lost. One marker read keeps the fast path honest.
			stale := false
			if bc, ok := w.chain.(*internal.BlockChain); ok && !bc.AppliedHeadIs(parentHash) {
				stale = true
			}
			if !stale {
				num := uint64(0)
				if n := st.block.Number64(); n != nil {
					num = n.Uint64()
				}
				if contentionDiagEnabled {
					st.paceEnterAt = time.Now()
				}
				if err := w.paceBlock(num); err != nil {
					return err
				}
				if contentionDiagEnabled {
					st.paceDur = time.Since(st.paceEnterAt)
					st.specHitAt = time.Now()
					st.triggerAt = triggerAt
				}
				log.Info("miner: speculative build hit", "number", num, "parent", parentHash.Hex()[:12], "tMs", time.Now().UnixMilli())
				if contentionDiagEnabled {
					st.taskChSentAt = time.Now()
				}
				select {
				case w.taskCh <- st:
				case <-w.ctx.Done():
					return w.ctx.Err()
				}
				return nil
			}
			log.Info("miner: speculative build discarded, applied head moved", "parent", parentHash.Hex()[:12])
		}
	}

	if speculative {
		// Publish our interrupt so a real production trigger can abort us, and
		// make sure only one speculative build runs at a time. The parent goes
		// with it: a trigger for the SAME parent lets us finish instead.
		w.activeSpecInterrupt.Store(interrupt)
		p := parentHash
		w.activeSpecParent.Store(&p)
		defer func() { w.activeSpecInterrupt.Store(nil); w.activeSpecParent.Store(nil) }()
	}

	// Consensus-pinned parent (HotStuff HighQC block): the world state must BE
	// that parent's post-state before we execute the payload — align it first
	// (reverts any locally-applied uncommitted sibling; no-op when already
	// aligned). AlignAppliedBranch is serialized with imports: the miner's QMDB
	// computer is isolated, so it must never peel the live tree directly. Doing
	// so can steal an importing block's in-flight undo after ComputeRoot but
	// before persistence, advancing PlainState/marker while rolling the tree
	// back one block.
	var dPersistWait time.Duration
	ownPending := false
	if bc, ok := w.chain.(*internal.BlockChain); ok {
		ownPending = w.ownPendingSpeculation(speculative, parentHash, bc)
	}
	// The layers are gathered BEFORE the read transaction opens: a parent
	// written between the two would be missing from both the store snapshot
	// and the overlay.
	var postLayers []*state.PostState
	if ownPending {
		if bc, ok := w.chain.(*internal.BlockChain); ok {
			postLayers = w.unwrittenOwnPostStates(parentHash, bc)
		}
		if postLayers == nil {
			log.Warn("miner: own unwritten parent has no post-state snapshot; waiting for its write instead",
				"parent", parentHash.Hex()[:12])
			ownPending = false
		} else {
			log.Info("miner: speculative build chains on own unwritten block",
				"parent", parentHash.Hex()[:12], "layers", len(postLayers), "accounts", postLayers[0].Accounts())
		}
	}
	if parentHash != (types.Hash{}) && !ownPending {
		if bc, ok := w.chain.(*internal.BlockChain); ok {
			// Early-vote overlap: the view can advance while the parent's
			// persistence is still in flight on this node. The build reads
			// PlainState, which lands atomically with the header — wait for it
			// rather than aligning against (and mis-reading) the pre-parent
			// state. Normally sub-millisecond; 2s covers a stalled write.
			// Persisted, then AlignAppliedBranch below: the align is what
			// unwinds a locally-applied sibling so the consensus parent can
			// be imported. Waiting here for the parent to be APPLIED
			// (580d2f32) ran before that unwind and could never succeed on a
			// node that had applied a sibling -- round 35p: every view of a
			// tenure timed out on "consensus parent not applied in time".
			wd.SetStep("persistWait")
			tPersist := time.Now()
			if !bc.WaitBlockPersisted(parentHash, 2*time.Second) {
				return fmt.Errorf("consensus parent %x not persisted in time", parentHash[:8])
			}
			// Round 35z9: `align` was 551 ms of a 1,358 ms build at 163k
			// transactions, on the path between one commit and the next
			// proposal, and there was no way to tell the wait for the parent's
			// write from the unwind that follows it. Two numbers, so the next
			// round knows which half to attack.
			dPersistWait = time.Since(tPersist)
			if pf != nil {
				pf.persistWait = dPersistWait
			}
			pblk, _ := w.chain.GetBlockByHash(parentHash)
			if pblk == nil {
				return fmt.Errorf("consensus parent %x not in local db", parentHash[:8])
			}
			// Nothing to unwind when the applied state is exactly the
			// consensus parent, which is every block of a healthy chain.
			// AlignAppliedBranch takes bc.lock and so waits behind the
			// parent's own write; skipping it there took 230 ms off the
			// leader's 512 ms align (round 35za).
			alignNeeded := !bc.AppliedHeadIsExactly(parentHash, pblk.Number64().Uint64())
			wd.SetStep("alignAppliedBranch")
			tAlignCall := time.Now()
			if err := func() error {
				if !alignNeeded {
					return nil
				}
				return bc.AlignAppliedBranch(pblk.Number64().Uint64()+1, parentHash)
			}(); err != nil {
				if pf != nil {
					pf.align += time.Since(tAlignCall)
					pf.lockWait += bc.TakeBuildStallLockWait()
				}
				if speculative {
					// A guess is not worth a forced import: the align fallback
					// below re-imports the consensus parent with switch
					// authority, which is only justified when consensus has
					// actually mandated this parent. Give up quietly.
					return fmt.Errorf("speculative align failed: %w", err)
				}
				// The QC block is in the DB but on a branch this node never
				// applied (it didn't vote in that view). Import it WITH switch
				// authority — the QC is the consensus mandate — then re-align.
				wd.SetStep("insertChainAuthorized")
				tInsert := time.Now()
				_, ierr := bc.InsertChainAuthorized([]block.IBlock{pblk})
				if pf != nil {
					pf.insertParent += time.Since(tInsert)
					pf.lockWait += bc.TakeBuildStallLockWait()
				}
				if ierr != nil {
					return fmt.Errorf("import consensus parent %x: %w (align: %v)", parentHash[:8], ierr, err)
				}
				wd.SetStep("alignAppliedBranch")
				tAlignCall2 := time.Now()
				err = bc.AlignAppliedBranch(pblk.Number64().Uint64()+1, parentHash)
				if pf != nil {
					pf.align += time.Since(tAlignCall2)
					pf.lockWait += bc.TakeBuildStallLockWait()
				}
				if err != nil {
					return fmt.Errorf("align applied branch to consensus parent %x: %w", parentHash[:8], err)
				}
			} else if pf != nil {
				pf.align += time.Since(tAlignCall)
				pf.lockWait += bc.TakeBuildStallLockWait()
			}
			// The align is a no-op when the applied head is BELOW the parent
			// (unwindForReimport leaves "not applied yet" to the future queue),
			// and the build would then run on the wrong base. Round 35zg B1:
			// after the leg's restart node6's startup revert sat at 14029922,
			// consensus named 14029923 as the parent, the speculative build of
			// 14029924 ran on a tree reloaded at 14029922, the parent was
			// re-imported one second later, the parked task matched by hash and
			// the fleet rejected an empty block with three different roots --
			// the OPEN_ISSUES "three roots after a restart" shape. Never build on
			// a parent that is not the applied head: a speculative build gives
			// up (the leader gate's deferred resume re-triggers after the
			// parent applies); a production build imports the parent with
			// switch authority first and checks again.
			if !bc.AppliedHeadIsExactly(parentHash, pblk.Number64().Uint64()) {
				if speculative {
					return fmt.Errorf("speculative build: consensus parent %x not applied yet", parentHash[:8])
				}
				wd.SetStep("insertChainAuthorized")
				tInsert2 := time.Now()
				_, ierr := bc.InsertChainAuthorized([]block.IBlock{pblk})
				if pf != nil {
					pf.insertParent += time.Since(tInsert2)
					pf.lockWait += bc.TakeBuildStallLockWait()
				}
				if ierr != nil {
					return fmt.Errorf("import consensus parent %x before building: %w", parentHash[:8], ierr)
				}
				if !bc.AppliedHeadIsExactly(parentHash, pblk.Number64().Uint64()) {
					return fmt.Errorf("consensus parent %x still not the applied head after import", parentHash[:8])
				}
			}
		}
	}

	wd.SetStep("prepareWork")
	tPrepareWork := time.Now()
	current, err := w.prepareWork(&generateParams{timestamp: uint64(timestamp), coinbase: coinbase, parentHash: parentHash})
	if pf != nil {
		pf.headerPrepare = time.Since(tPrepareWork)
	}
	if err != nil {
		log.Error("cannot prepare work", "err", err)
		return err
	}
	if n := current.header.Number; n != nil {
		wd.SetBlockNumber(n.Uint64())
	}

	// Block-production pacing (skipped for speculative builds, which run early
	// on purpose; the grid wait is paid when the parked block is HANDED OVER,
	// see the speculative-hit path in this function's caller flow).
	var tPaceEnter time.Time
	var dPace time.Duration
	if !speculative {
		if contentionDiagEnabled {
			tPaceEnter = time.Now()
		}
		if err := w.paceBlock(current.header.Number.Uint64()); err != nil {
			return err
		}
		if contentionDiagEnabled {
			dPace = time.Since(tPaceEnter)
		}
	}

	tAlign := time.Since(start)
	wd.SetStep("roTxBegin")
	tRoTx := time.Now()
	tx, err := w.chain.DB().BeginRo(w.ctx)
	if pf != nil {
		pf.roTxBegin = time.Since(tRoTx)
	}
	if err != nil {
		log.Error("work.commitWork failed", err)
		return err
	}
	defer tx.Rollback()

	var stateReader state.StateReader = state.NewPlainStateReader(tx)
	if cache := layered.ExtractCache(w.chain.DB()); cache != nil {
		stateReader = state.NewCachedStateReader(stateReader, cache)
	}
	// A chained build reads its unwritten own ancestors' effects from their
	// post-state snapshots, oldest innermost (round 35zr: without this the
	// coinbase was credited on the pre-parent balance and the root diverged).
	for i := len(postLayers) - 1; i >= 0; i-- {
		stateReader = state.NewPostStateReader(postLayers[i], stateReader)
	}

	// Under the recorder and the tracer: the parallel fill seeds this layer
	// with the delta-credited recipients read across its workers, and the
	// block-end fold's reads -- logged above it in the same order as before
	// -- become map hits.
	prefetch := state.NewAccountPrefetch(stateReader)
	stateReader = prefetch

	// Wrap state reader with TracingReader when JMT is enabled to record
	// all state accesses for witness generation.
	var tracingReader *witness.TracingReader
	if bc, ok := w.chain.(*internal.BlockChain); ok && bc.IsJMTEnabled() {
		tracingReader = witness.NewTracingReader(stateReader)
		stateReader = tracingReader
	}

	// Wrap with the mobile-verification read-log recorder when a packet
	// sink is wired (docs/mobile-attestation-design.md §4). Same observer
	// pattern as TracingReader above: capture-only, execution unchanged.
	var readLogRecorder *streamverify.ReadLogRecorder
	if w.mobilePacketSink != nil {
		readLogRecorder = streamverify.NewReadLogRecorder(stateReader, nil)
		stateReader = readLogRecorder
	}

	stateWriter := state.NewNoopWriter()
	ibs := state.New(stateReader)
	// Carried explicitly: the parallel fill's per-worker readers must layer
	// the same snapshots, and the recorder wrappers above hide them from a
	// walk of the reader chain.
	ibs.SetPostStateLayers(postLayers)
	ibs.SetAccountPrefetch(prefetch)
	// Inject an isolated root computer so the assembled block's stateRoot uses
	// the active commitment scheme (e.g. QMDB twig forest). Speculative builds
	// must not mutate the live tree, so this is a fresh DB-backed instance,
	// discarded with the block. Without it, IntermediateRoot falls back to an
	// empty MPT root and the produced block can never become canonical.
	if bcForRoot, ok := w.chain.(*internal.BlockChain); ok {
		// The parent's state root lets the miner tree recognise that the block
		// it built last IS this build's parent (NewMinerRootComputer's
		// own-block fast path).
		// The parent's EXECUTED root: its header's Root before the fork, this
		// node's result of it under deferred execution (the header then
		// carries the grandparent's).
		parentRoot := types.Hash{}
		if current.header.Number.Uint64() > 0 {
			if pr, perr := w.parentExecutedResult(tx, current.header); perr == nil {
				parentRoot = pr.Root
			} else if w.chainConfig.IsDeferredExecution(current.header.Time) {
				return fmt.Errorf("miner: deferred execution: %w", perr)
			}
		}
		wd.SetStep("specTreeReload")
		tReloadCall := time.Now()
		rc, rcErr := bcForRoot.NewMinerRootComputer(tx, parentRoot)
		if pf != nil {
			pf.specTreeReload += time.Since(tReloadCall)
			pf.rootLockWait += bcForRoot.TakeBuildStallRootLockWait()
		}
		if rcErr != nil {
			// A block sealed on the fallback (live or empty) root can never
			// become canonical; abandon the build instead of proposing it.
			return fmt.Errorf("miner: speculative root computer unavailable: %w", rcErr)
		}
		if rc != nil {
			ibs.SetRootComputer(rc)
		}
	}
	tReload := time.Since(start)
	ibs.BeginWriteSnapshot()
	ibs.BeginWriteCodes()

	var headers []*block.Header
	getHeader := func(hash types.Hash, number uint64) *block.Header {
		h := rawdb.ReadHeader(tx, hash, number)
		if h != nil {
			headers = append(headers, h)
		}
		return h
	}

	// Start-of-block system operations (EIP-4788 beacon root, EIP-2935 parent
	// blockhash ring buffer) — the SAME hooks StateProcessor.Process runs on
	// the import side. Building without them produced a state root missing the
	// system contracts' storage writes, so with import-side root verification
	// on, every follower rejected every locally built block (proposer/computed
	// mismatch on the very first sealed block).
	wd.SetStep("blockStartSyscalls")
	tBlockStart := time.Now()
	if err := internal.ProcessExecutionBlockStart(current.header.ParentBeaconRoot, w.chainConfig, ibs, current.header, w.engine); err != nil {
		return fmt.Errorf("miner block-start system calls: %w", err)
	}
	if pf != nil {
		pf.blockStart = time.Since(tBlockStart)
	}

	// N42 native chain (phase 6c): stamp the committed mobile-registry root into
	// the header when the MobileAnchor fork is active. This is a HEADER
	// commitment only — like the CommitteePool's ParentBeaconRoot link, it binds
	// the root into the block hash (RLP) with NO state-trie write, so it needs no
	// system contract, no genesis alloc and no replay to activate. The full
	// history lives in the rawdb anchor log (phase 6b). Gated on IsMobileAnchor;
	// a nil provider or inactive fork leaves the header field nil (byte-identical
	// pre-fork / on eth-el).
	if w.mobileAnchorRoot != nil && w.chainConfig.IsMobileAnchor(current.header.Time) {
		current.header.MobileRegistryRoot = w.mobileAnchorRoot()
	}

	tPrep := time.Since(start)
	wd.SetStep("pendingSnapshot")
	err = w.fillTransactions(interrupt, current, ibs, getHeader, pf, wd)
	switch {
	case err == nil:
		w.resubmitAdjustCh <- &intervalAdjust{inc: false}
	case errors.Is(err, errBlockInterruptedByRecommit):
		gaslimit := current.header.GasLimit
		ratio := float64(gaslimit-current.gasPool.Gas()) / float64(gaslimit)
		if ratio < 0.1 {
			ratio = 0.1
		}
		w.resubmitAdjustCh <- &intervalAdjust{
			ratio: ratio,
			inc:   true,
		}
	default:
		return fmt.Errorf("fill transactions: %w", err)
	}

	// End-of-block system calls (EIP-7002 withdrawal / EIP-7251 consolidation
	// requests) — mirror of StateProcessor.Process before Finalize, for the
	// same build/verify state-root equivalence as the block-start hooks above.
	if _, err := internal.ProcessExecutionBlockEnd(nil, w.chainConfig, ibs, current.header, w.engine); err != nil {
		return fmt.Errorf("miner block-end system calls: %w", err)
	}

	// Build-phase breakdown: the dropped-seal hunt found ~6s builds with a
	// 6ms seal and no visible spender - this line is the missing evidence.
	log.Info("miner: build phases",
		"align", tAlign, "persistWait", dPersistWait, "reload", tReload-tAlign, "syscalls", tPrep-tReload,
		"fillTx", time.Since(start)-tPrep, "total", time.Since(start),
		// tMs is the build's END; tMs - total is its start. Round 35zzq could not
		// place either against the QC, and the gap between them is the cycle now.
		"tMs", time.Now().UnixMilli())
	// w.commit() is the rest of commitWork: it assembles and finalizes the
	// block, creates the task and hands it to taskCh, where taskLoop stamps
	// sealStart. Everything between "build phases" (logged immediately above)
	// and sealStart is therefore in here, and that is the remainder of the
	// unaccounted 247-373 ms once the gates (0.03 ms) and the work queue
	// (0.01 ms) are ruled out. task.assemble and task.finalize are recorded
	// INSIDE this call, so they are before sealStart, not inside seal2res --
	// an earlier subtraction in docs/QS_BLOCK_TIME_BUDGET.md had them on the
	// wrong side.
	tCommit := time.Now()
	err = w.commit(current, stateWriter, ibs, start, headers, tracingReader, readLogRecorder, speculative, parentHash, triggerAt, tPaceEnter, dPace)
	log.Info("miner: commit phases", "speculative", speculative, "commitNs", time.Since(tCommit).Nanoseconds())
	if err != nil {
		log.Errorf("w.commit failed, error %v\n", err)
		return err
	}

	return nil
}

// recalcRecommit recalculates the resubmitting interval upon feedback.
func recalcRecommit(minRecommit, prev time.Duration, target float64, inc bool) time.Duration {
	var (
		prevF = float64(prev.Nanoseconds())
		next  float64
	)
	if inc {
		next = prevF*(1-intervalAdjustRatio) + intervalAdjustRatio*(target+intervalAdjustBias)
		max := float64(maxRecommitInterval.Nanoseconds())
		if next > max {
			next = max
		}
	} else {
		next = prevF*(1-intervalAdjustRatio) + intervalAdjustRatio*(target-intervalAdjustBias)
		min := float64(minRecommit.Nanoseconds())
		if next < min {
			next = min
		}
	}
	return time.Duration(int64(next))
}

func (w *worker) workLoop(recommit time.Duration) error {
	defer w.cancel()
	defer w.stop()
	var (
		interrupt   *atomic.Int32
		minRecommit = recommit // minimal resubmit interval specified by user.
		timestamp   int64      // timestamp for each round of sealing.
	)

	newBlockCh := make(chan common.ChainHighestBlock, 10)
	defer close(newBlockCh)

	newBlockSub, _ := event.GlobalEvent.Subscribe(newBlockCh)
	defer newBlockSub.Unsubscribe()

	timer := time.NewTimer(0)
	defer timer.Stop()
	<-timer.C // discard the initial tick

	commit := func(noempty bool, s int32) {
		if interrupt != nil {
			interrupt.Store(s)
		}
		interrupt = new(atomic.Int32)
		select {
		case w.newWorkCh <- &newWorkReq{interrupt: interrupt, noempty: noempty, timestamp: timestamp}:
		case <-w.ctx.Done():
			return
		}
		timer.Reset(recommit)
	}

	clearPending := func(number *uint256.Int) {
		w.mu.Lock()
		for h, t := range w.pendingTasks {
			threshold := uint256.NewInt(0).Add(t.block.Number64(), uint256.NewInt(staleThreshold))
			if number.Cmp(threshold) >= 0 {
				delete(w.pendingTasks, h)
			}
		}
		w.mu.Unlock()
	}

	for {
		select {
		case <-w.ctx.Done():
			return w.ctx.Err()

		case <-w.startCh:
			clearPending(w.chain.CurrentBlock().Number64())
			// Leader-driven engines (HotStuff) produce ONLY via
			// TriggerBlockProduction, which pins the build to the consensus-
			// mandated parent (the HighQC/LockedQC block). An event-driven
			// commit() here builds on the local head with no pin — for a current
			// leader that is a second, competing build at the same height (a
			// sibling generator racing the pinned proposal).
			if !usesTimerDrivenSealing(w.engine) {
				continue
			}
			if !w.shouldProduceNow() {
				continue
			}
			timestamp = time.Now().Unix()
			commit(false, commitInterruptNewHead)

		case blockEvent := <-newBlockCh:
			clearPending(blockEvent.Block.Number64())
			// See the startCh case: no event-driven unpinned builds on
			// leader-driven engines.
			if !usesTimerDrivenSealing(w.engine) {
				continue
			}
			if !w.shouldProduceNow() {
				continue
			}
			timestamp = time.Now().Unix()
			commit(false, commitInterruptNewHead)

		case err := <-newBlockSub.Err():
			return err

		case <-timer.C:
			// HotStuff BFT: block production is leader-driven, not timer-driven.
			// Only the leader triggers production via TriggerBlockProduction().
			if w.isRunning() && usesTimerDrivenSealing(w.engine) {
				commit(true, commitInterruptResubmit)
			}

		case adjust := <-w.resubmitAdjustCh:
			before := recommit
			if adjust.inc {
				target := float64(recommit.Nanoseconds()) / adjust.ratio
				recommit = recalcRecommit(minRecommit, recommit, target, true)
				log.Trace("Increase miner recommit interval", "from", before, "to", recommit)
			} else {
				recommit = recalcRecommit(minRecommit, recommit, float64(minRecommit.Nanoseconds()), false)
				log.Trace("Decrease miner recommit interval", "from", before, "to", recommit)
			}
		}
	}
}

// parallelFillEnabled gates the builder's Block-STM fill (N42_MINER_PARALLEL_FILL=1).
func parallelFillEnabled() bool { return os.Getenv("N42_MINER_PARALLEL_FILL") == "1" }

// pf and wd are S11's diagnostic pre-fill timer/watchdog (both nil unless
// N42_BUILD_STALL_DIAG=1; docs/QS_BLOCK_TIME_BUDGET.md 6by). This function
// is where the pre-fill total completes (pending-pool snapshot + stale
// trim) and where the parallel/serial fill decision is made, so it is also
// where pf's summary line is logged and wd is cancelled -- the literal
// "start of the fill" boundary.
func (w *worker) fillTransactions(interrupt *atomic.Int32, env *environment, ibs *state.IntraBlockState, getHeader func(hash types.Hash, number uint64) *block.Header, pf *prefillTimes, wd *buildStallWatchdog) (retErr error) {
	header := env.header
	headerNumber, err := requireHeaderNumber(header, "mining header number unavailable")
	if err != nil {
		return err
	}

	// Pre-allocate with estimated max tx count to avoid append-growth
	// in the hot commit loop. 21000 is the minimum gas per tx. The estimate is
	// exact for a transfer-saturated block (480M/21000 = 22,857 slots, ~360KB
	// across both slices); the old 1024 cap forced five doubling copies per
	// full block to save memory nobody was short of.
	estCap := int(env.gasPool.Gas() / 21000)
	if estCap > 1<<20 {
		estCap = 1 << 20 // sanity bound for absurd gas limits
	}
	env.txs = make([]*transaction.Transaction, 0, estCap)
	env.receipts = make(block.Receipts, 0, estCap)

	// Bound the packed size so the sealed block can actually propagate. Gas is
	// NOT a proxy for wire size: ~9500 plain transfers fit comfortably inside a
	// raised gas limit yet encode to >1 MiB, above the p2p bound, and a block no
	// follower can receive livelocks import-gated HotStuff voting forever. See
	// block_size.go.
	sizeLimiter := newBlockSizeLimiter(header)

	noop := state.NewNoopWriter()
	vmConfig := vm2.Config{}

	// EIP-7928: when the BAL fork is active, harvest each committed tx's post-value
	// writes via the shared BALCapture (a pure observer wrapping noop) so the block
	// access list hash can be bound into the header below. The importer recomputes
	// it the same way (StateProcessor.Process), so the two agree by construction.
	// balCap is nil (zero overhead) when the fork is off.
	var writer state.StateWriter = noop
	var balCap *internal.BALCapture
	if w.chainConfig.IsBAL(env.header.Time) {
		balCap = internal.NewBALCapture(noop)
		writer = balCap
	}
	finalizeBAL := func() error {
		if balCap == nil {
			return nil
		}
		h, err := balCap.HashFor(internal.TxHashes(env.txs))
		if err != nil {
			return fmt.Errorf("compute block access list hash: %w", err)
		}
		env.header.BlockAccessListHash = &h
		return nil
	}
	defer func() {
		if err := finalizeBAL(); err != nil && retErr == nil {
			retErr = err
		}
	}()

	commitTx := func(txn *transaction.Transaction) error {
		ibs.Prepare(txn.Hash(), types.Hash{}, env.tcount)
		gasSnap := env.gasPool.Gas()
		gasUsedSnap := header.GasUsed
		snap := ibs.Snapshot()
		log.Debug("addTransactionsToMiningBlock", "txn hash", txn.Hash())

		if balCap != nil {
			balCap.BeginTx(txn.Hash())
		}
		coinbase := env.coinbase
		receipt, _, err := internal.ApplyTransaction(w.chainConfig, internal.GetHashFn(header, getHeader), w.engine, &coinbase, env.gasPool, ibs, writer, env.header, txn, &header.GasUsed, vmConfig)
		if err != nil {
			if balCap != nil {
				balCap.DiscardTx()
			}
			ibs.RevertToSnapshot(snap)
			env.gasPool = new(common.GasPool).AddGas(gasSnap)
			header.GasUsed = gasUsedSnap
			return err
		}
		if balCap != nil {
			balCap.CommitTx()
		}

		env.txs = append(env.txs, txn)
		env.receipts = append(env.receipts, receipt)
		return nil
	}

	blockNumber := headerNumber.Uint64()

	// Phase 1: Execute MEV bundles at block top (highest-paying first).
	if w.bundlePool != nil {
		wd.SetStep("mevBundles")
		bundles := w.bundlePool.GetBundles(blockNumber, header.Time)
		for _, bundle := range bundles {
			if env.gasPool.Gas() < params.TxGas {
				break
			}
			// Size-account the whole bundle up front: it is committed
			// atomically, so it must fit atomically. The estimate is an upper
			// bound (revert-allowed transactions that fail are dropped from the
			// block but still counted), which only ever under-packs.
			bundleSize, sizeErr := bundlePayloadSize(bundle.Txs)
			if sizeErr != nil {
				log.Warn("skipping unencodable MEV bundle", "err", sizeErr)
				continue
			}
			if !sizeLimiter.fits(bundleSize) {
				if sizeLimiter.exhausted() {
					break
				}
				continue
			}
			// Try to commit entire bundle atomically.
			snap := ibs.Snapshot()
			gasSnap := env.gasPool.Gas()
			gasUsedSnap := header.GasUsed
			startTxCount := len(env.txs)
			startReceiptCount := len(env.receipts)
			startTCount := env.tcount
			balSnap := 0
			if balCap != nil {
				balSnap = balCap.Snapshot()
			}
			bundleOk := true

			for _, tx := range bundle.Txs {
				err := commitTx(tx)
				if err != nil {
					if bundle.IsRevertAllowed(tx.Hash()) {
						continue // Allowed to fail
					}
					bundleOk = false
					break
				}
				env.tcount++
			}

			if bundleOk {
				sizeLimiter.add(bundleSize)
			} else {
				// Revert entire bundle.
				ibs.RevertToSnapshot(snap)
				env.gasPool = new(common.GasPool).AddGas(gasSnap)
				header.GasUsed = gasUsedSnap
				env.txs = env.txs[:startTxCount]
				env.receipts = env.receipts[:startReceiptCount]
				env.tcount = startTCount
				if balCap != nil {
					balCap.RevertToSnapshot(balSnap)
				}
			}
		}
	}

	// Phase 2: Fill remaining space with regular transactions sorted by effective tip.
	wd.SetStep("pendingSnapshot")
	tPending := time.Now()
	pending := w.txsPool.Pending(false)
	dPending := time.Since(tPending)
	pendingRawAccts := len(pending)
	if len(pending) == 0 {
		pf.logIfSlow(blockNumber, dPending, 0)
		wd.Cancel()
		return nil
	}

	// Round 28: after a large block lands the pool's reorg lags the
	// speculative build by up to 1.7 s, and the build then EXECUTES the
	// previous block's mined transactions one by one (ErrNonceTooLow, ~5 us
	// each, ~0.7 s for a 163k block) before reaching anything fresh. Read
	// each account's state nonce once and drop the stale prefix here
	// instead: O(accounts) reads for O(stale transactions) executions saved.
	// Semantics unchanged -- those transactions fail the same way inside.
	// Read through the state READER, never the IntraBlockState: a read there
	// creates a state object, and a touched-but-unchanged object is one more
	// no-op entry in an append-only commitment -- a root the followers, who
	// never touched it, cannot reproduce.
	staleTrimmed := 0
	wd.SetStep("staleTrim")
	tTrim := time.Now()
	reader := ibs.GetStateReader()
	for addr, list := range pending {
		var nonce uint64
		if acc, rerr := reader.ReadAccountData(addr); rerr == nil && acc != nil {
			nonce = acc.Nonce
		} else if rerr != nil {
			continue // cannot tell; let execution decide as before
		}
		i := 0
		for i < len(list) && list[i].Nonce() < nonce {
			i++
		}
		if i == 0 {
			continue
		}
		staleTrimmed += i
		if i == len(list) {
			delete(pending, addr)
		} else {
			pending[addr] = list[i:]
		}
	}
	dTrim := time.Since(tTrim)
	if len(pending) == 0 {
		pf.logIfSlow(blockNumber, dPending, dTrim)
		wd.Cancel()
		return nil
	}
	txSet := builder.NewTxByPriceAndNonce(pending, header.BaseFee)

	// S11: this is the true "start of the fill" boundary -- everything above
	// is pre-fill setup (align/import, RoTx open, tree reload, header
	// prepare, pool snapshot, stale trim); everything below (parallel or
	// serial) is the fill itself. Log the summary and disarm the watchdog
	// here, whichever fill path runs next.
	pf.logIfSlow(blockNumber, dPending, dTrim)
	wd.Cancel()

	// Round 35k: the builder's fill was the leader's largest single phase at
	// 163k transactions (commit 611-683 ms, ~3.8 us/tx serial), with the
	// import already on Block-STM at ~200 ms. Pick the candidates in the
	// same price-and-nonce order without executing (gas by each
	// transaction's limit, so the block cannot exceed the ceiling), run them
	// through the same executor the followers use, drop the ones that fail
	// their pre-check (they wrote nothing; a dropped nonce fails its sender's
	// later candidates the same way), and hand the survivors to assemble.
	// Bundles, BAL capture and fee-recipient candidates keep the serial path.
	if parallelFillEnabled() && balCap == nil && env.tcount == 0 && !w.chainConfig.IsBAL(env.header.Time) {
		if bc, ok := w.chain.(*internal.BlockChain); ok {
			tPickP := time.Now()
			budget := env.gasPool.Gas()
			candidates := make([]*transaction.Transaction, 0, estCap)
			for {
				tx := txSet.Peek()
				if tx == nil {
					break
				}
				if tx.Gas() > budget {
					txSet.Pop()
					if budget < params.TxGas {
						break
					}
					continue
				}
				txSize, decision, sizeErr := sizeLimiter.admit(tx)
				if sizeErr != nil || decision != packAccept {
					if decision == packStop {
						break
					}
					txSet.Pop() // packSkipAccount, or unencodable: skip the account
					continue
				}
				sizeLimiter.add(txSize)
				budget -= tx.Gas()
				candidates = append(candidates, tx)
				txSet.Shift()
			}
			dPickP := time.Since(tPickP)
			tRun := time.Now()
			included, receipts, usedGas, failed, perr := bc.BuildParallel(header, candidates, ibs, internal.GetHashFn(header, getHeader))
			if perr == nil {
				env.txs = append(env.txs, included...)
				env.receipts = append(env.receipts, receipts...)
				env.tcount += len(included)
				header.GasUsed += usedGas
				if err := env.gasPool.SubGas(usedGas); err != nil {
					return err
				}
				log.Info("miner: parallel fill", "candidates", len(candidates), "included", len(included), "failed", failed,
					"pick", dPickP, "run", time.Since(tRun), "pendingSnapshot", dPending, "trim", dTrim, "staleTrimmed", staleTrimmed)
				return nil
			}
			if !errors.Is(perr, internal.ErrParallelNotApplicable) {
				return perr
			}
			// Not applicable: rebuild the set and take the serial path.
			txSet = builder.NewTxByPriceAndNonce(pending, header.BaseFee)
			sizeLimiter = newBlockSizeLimiter(header)
		}
	}
	log.Tracef("fillTransactions pending accounts:%d", len(pending))
	// Round 27: a block that comes out a fifth full with a pool the harness
	// reports at 200,000 pending is only diagnosable if the build says what
	// the POOL handed it. Counted once per build, reported on the breakdown.
	pendingTxs := 0
	for _, list := range pending {
		pendingTxs += len(list)
	}

	// Accounts skipped because their head transaction cannot pay the base fee.
	// Reported once per build: a block that comes out empty with a full pool is
	// otherwise indistinguishable from a block that had nothing to include, and
	// the difference is the whole diagnosis.
	priceOut := 0
	// Why accounts left the set (round 28: a 10k pack from a 449k pool).
	var popGas, popNonceHigh, skipNonceLow, popOther int
	lookupWait0 := commitment.QMDBLookupWaitNanos()
	// fillTx phase accumulators — see the breakdown log below the loop.
	var dPick, dCommit, dHeap time.Duration

	for {
		if interrupt != nil {
			if signal := interrupt.Load(); signal != commitInterruptNone {
				// The deferred finalizeBAL binds the hash for the recommit
				// case (which the caller still seals); new-head aborts the
				// build entirely via commitWork's default error branch.
				return signalToErr(signal)
			}
		}
		if env.gasPool.Gas() < params.TxGas {
			log.Trace("Not enough gas for further transactions", "have", env.gasPool, "want", params.TxGas)
			break
		}

		tSpan := time.Now()
		tx := txSet.Peek()
		if tx == nil {
			break
		}

		txSize, decision, sizeErr := sizeLimiter.admit(tx)
		if decision == packStop {
			log.Trace("Block size budget exhausted, stopping tx packing",
				"txs", len(env.txs), "remaining", sizeLimiter.remaining())
			break
		}
		if decision == packSkipAccount {
			switch {
			case sizeErr != nil:
				log.Error("failed to encode pending transaction for size accounting",
					"hash", tx.Hash(), "err", sizeErr)
			case sizeLimiter.tooLargeForAnyBlock(txSize):
				log.Warn("transaction too large to ever fit a block, skipping",
					"hash", tx.Hash(), "size", txSize, "budget", sizeLimiter.budget)
			}
			txSet.Pop()
			continue
		}
		tExec := time.Now()
		dPick += tExec.Sub(tSpan)

		err := commitTx(tx)
		dCommit += time.Since(tExec)
		tShift := time.Now()
		switch {
		case err == nil:
			sizeLimiter.add(txSize)
			env.tcount++
			txSet.Shift() // Move to next tx from same account
		case errors.Is(err, internal.ErrGasLimitReached):
			popGas++
			txSet.Pop() // Skip this account entirely
		case errors.Is(err, internal.ErrNonceTooHigh):
			popNonceHigh++
			txSet.Pop() // Nonce gap, skip account
		case errors.Is(err, internal.ErrNonceTooLow):
			skipNonceLow++
			txSet.Shift() // Try next nonce from same account
		case errors.Is(err, internal.ErrFeeCapTooLow):
			// The account cannot pay this block's base fee. Skip the whole
			// account: its later transactions are gated behind this nonce, so
			// none of them can be included however much they bid.
			//
			// This is the common case, not an anomaly. A saturated chain drives
			// the base fee above a fixed-price load's cap by design (EIP-1559
			// settles at the gas target), and every transaction in the pool then
			// fails here. Shifting instead of popping walked the entire pool per
			// build, and logging each failure at Error put thousands of lines in
			// the log per block -- the empty block that produced them was three
			// times more expensive to build than a full one.
			priceOut++
			txSet.Pop()
		default:
			popOther++
			log.Error("miningCommitTx failed", "error", err)
			txSet.Shift()
		}
		dHeap += time.Since(tShift)
	}
	lookupWait := time.Duration(commitment.QMDBLookupWaitNanos() - lookupWait0)

	// One line per non-trivial build: where fillTx's time actually goes.
	// pick = Peek + size admit, commit = ApplyTransaction (the EVM work an
	// importer would also pay), heap = Shift/Pop re-sorting. The importer
	// executes the same transactions in ~5-6µs each; whatever pick+heap add on
	// top of commit is the builder's own overhead.
	if env.tcount > 1000 {
		log.Info("miner: fillTx breakdown", "pendingAccts", len(pending), "pendingRawAccts", pendingRawAccts, "pendingTxs", pendingTxs, "priceOut", priceOut,
			"pendingSnapshot", dPending, "trim", dTrim,
			"popGas", popGas, "popNonceHigh", popNonceHigh, "skipNonceLow", skipNonceLow, "staleTrimmed", staleTrimmed, "popOther", popOther, "lookupWait", lookupWait,
			"txs", env.tcount, "pick", dPick, "commit", dCommit, "heap", dHeap)
	}

	if priceOut > 0 {
		log.Info("miner: accounts priced out by the base fee",
			"accounts", priceOut, "of", len(pending), "packed", len(env.txs), "baseFee", header.BaseFee)
	}

	// Prune old bundles.
	if w.bundlePool != nil {
		w.bundlePool.PruneBundles(blockNumber)
	}

	// Bind the BAL hash even for an empty block or a block containing only bundle
	// transactions. Once the fork is active the header field is mandatory.
	return nil
}

func (w *worker) prepareWork(param *generateParams) (*environment, error) {
	w.mu.RLock()
	defer w.mu.RUnlock()

	timestamp := param.timestamp

	currentBlock := w.chain.CurrentBlock()
	if currentBlock == nil {
		return nil, errors.New("current block is nil")
	}
	currentHeader := currentBlock.Header()
	if currentHeader == nil {
		return nil, errors.New("current block header is nil")
	}
	parent, ok := currentHeader.(*block.Header)
	if !ok || parent == nil {
		return nil, errors.New("invalid current block header type")
	}
	if param.parentHash != (types.Hash{}) {
		b := w.ownSealed(param.parentHash)
		if b == nil {
			b, _ = w.chain.GetBlockByHash(param.parentHash)
		}
		if b == nil {
			return nil, errors.New("missing parent")
		}
		parent, ok = b.Header().(*block.Header)
		if !ok || parent == nil {
			return nil, errors.New("invalid parent header type")
		}
	}

	if parent.Time >= param.timestamp {
		timestamp = parent.Time + 1
	}

	parentNumber, err := requireHeaderNumber(parent, "parent block number unavailable")
	if err != nil {
		return nil, err
	}

	header := &block.Header{
		ParentHash: parent.Hash(),
		Coinbase:   param.coinbase,
		Number:     uint256.NewInt(0).Add(parentNumber, uint256.NewInt(1)),
		GasLimit:   CalcGasLimit(parent.GasLimit, w.minerConf.GasCeil),
		Time:       uint64(timestamp),
		Difficulty: uint256.NewInt(0),
		BaseFee:    uint256.NewInt(0),
	}

	// Set baseFee and GasLimit if we are on an EIP-1559 chain
	headerNumber, err := requireHeaderNumber(header, "mining header number unavailable")
	if err != nil {
		return nil, err
	}
	if w.chainConfig.IsLondon(headerNumber.Uint64()) {
		header.BaseFee, _ = uint256.FromBig(misc.CalcBaseFee(w.chainConfig, parent))
		if !w.chainConfig.IsLondon(parentNumber.Uint64()) {
			parentGasLimit := parent.GasLimit * params.ElasticityMultiplier
			header.GasLimit = CalcGasLimit(parentGasLimit, w.minerConf.GasCeil)
		}
	}

	if err := w.engine.Prepare(w.chain, header); err != nil {
		return nil, err
	}

	return w.makeEnv(parent, header, param.coinbase), nil
}

func (w *worker) makeEnv(parent *block.Header, header *block.Header, coinbase types.Address) *environment {
	env := &environment{
		ancestors: mapset.NewSet(),
		family:    mapset.NewSet(),
		coinbase:  coinbase,
		header:    header,
		gasPool:   new(common.GasPool).AddGas(fillGasBudget(header.GasLimit)),
	}

	for _, ancestor := range w.chain.GetBlocksFromHash(parent.ParentHash, 3) {
		env.family.Add(ancestor.Hash())
		env.ancestors.Add(ancestor.Hash())
	}

	return env
}

func (w *worker) commit(env *environment, writer state.WriterWithChangeSets, ibs *state.IntraBlockState, start time.Time, needHeaders []*block.Header, tracingReader *witness.TracingReader, readLogRecorder *streamverify.ReadLogRecorder, speculative bool, specParent types.Hash, triggerAt time.Time, tPaceEnter time.Time, dPace time.Duration) error {
	if !w.isRunning() {
		return nil
	}

	// Phase timings (observability only). NOTE: the "miner: build phases" line
	// is emitted by commitWork BEFORE it calls commit(), so it does NOT cover
	// FinalizeAndAssemble — and that is where state root #1 is computed
	// (engine.Finalize → ibs.IntermediateRoot). These counters close that gap.
	tCommitStart := time.Now()
	var dWitness time.Duration

	envCopy := env.copy()
	dCopy := time.Since(tCommitStart)
	deferredExec := block.DeferredAt(envCopy.header.Time)
	if deferredExec {
		// The header carries the parent's executed result; this block's own
		// is recorded below and appears in the next header.
		rtx, terr := w.chain.DB().BeginRo(w.ctx)
		if terr != nil {
			return terr
		}
		pr, perr := w.headerStampResult(rtx, envCopy.header)
		rtx.Rollback()
		if perr != nil {
			return fmt.Errorf("miner: deferred execution: %w", perr)
		}
		envCopy.header.Root, envCopy.header.ReceiptHash, envCopy.header.Bloom, envCopy.header.GasUsed = pr.Root, pr.ReceiptHash, pr.Bloom, pr.GasUsed
	}
	iblock, rewards, unpay, err := w.engine.FinalizeAndAssemble(w.chain, envCopy.header, ibs, envCopy.txs, nil, envCopy.receipts)
	if err != nil {
		return err
	}
	var exec *rawdb.ExecutedResult
	if deferredExec {
		root, ok := ibs.LastIntermediateRoot()
		if !ok {
			return errors.New("miner: deferred execution: the build computed no root")
		}
		r := internal.ExecutedResultFor(w.chainConfig, envCopy.header.Number.Uint64(), root, envCopy.receipts)
		exec = &r
	}
	dFinalize := time.Since(tCommitStart)

	// Mobile-verification packet: the recorder captured this block's read
	// log during the build; pair it with the FINAL header (receipts root,
	// state root, gas used are set by FinalizeAndAssemble) and hand off to
	// the sink. Best-effort by design — packet production must never fail
	// a block.
	tPacket := time.Now()
	if w.mobilePacketSink != nil && readLogRecorder != nil {
		if finalHeader, ok := iblock.Header().(*block.Header); ok {
			// Built off the critical path: packet construction walked the whole
			// read log (~35-75ms on a full block) inside assemble. The header is
			// COPIED first — the sealer appends the BLS signature to this
			// block's Extra concurrently — while the tx slice and the recorder
			// are immutable once the build is done. Delivery was already
			// best-effort; asynchrony only moves where the effort is spent.
			hdrCopy := block.CopyHeader(finalHeader)
			num, nerr := requireBlockNumber(iblock, "sealed block number unavailable")
			if nerr == nil {
				txs, rec, sink := envCopy.txs, readLogRecorder, w.mobilePacketSink
				go func() {
					if pkt, perr := streamverify.BuildStreamPacket(hdrCopy, txs, rec); perr != nil {
						log.Warn("mobileverify: packet build failed", "number", hdrCopy.Number.Uint64(), "err", perr)
					} else {
						sink(pkt, num.Uint64())
					}
				}()
			}
		}
	}
	dPacket := time.Since(tPacket)

	// Generate witness after FinalizeAndAssemble when JMT + TracingReader are active.
	if tracingReader != nil {
		tWitness := time.Now()
		if bc, ok := w.chain.(*internal.BlockChain); ok && bc.JMTCommitment() != nil {
			gen, gerr := witness.NewGenerator(bc.JMTCommitment())
			if gerr != nil {
				log.Warn("Failed to create witness generator", "err", gerr)
			}
			var bw *witness.BlockWitness
			var werr error
			if gen != nil {
				parentRoot := bc.CurrentBlock().StateRoot()
				bw, werr = gen.Generate(parentRoot, tracingReader, ibs.CodeHashes())
			}
			if werr != nil {
				log.Warn("Failed to generate block witness", "err", werr)
			} else {
				bc.StoreWitness(iblock.Hash(), bw)
				iblockNumber, numberErr := requireBlockNumber(iblock, "finalized block number unavailable")
				if numberErr != nil {
					log.Warn("Block witness generated without block number", "err", numberErr)
				} else {
					log.Debug("Block witness generated", "block", iblockNumber.Uint64(), "accounts", len(bw.AccountProofs), "storage", len(bw.StorageProofs))

					// Async submit to ZK prover if configured.
					if w.zkProverService != nil {
						parentHdr := bc.CurrentBlock().Header()
						go func(blkHash types.Hash, blkNum uint64, bwCopy *witness.BlockWitness, ph block.IHeader) {
							guestInput, err := zkprover.BuildGuestInput(w.chainConfig, iblock, ph, bwCopy)
							if err != nil {
								log.Warn("Failed to build guest input for ZK prover", "err", err)
								return
							}
							if err := w.zkProverService.SubmitBlock(blkHash, blkNum, guestInput); err != nil {
								log.Warn("Failed to submit block to ZK prover", "err", err)
							}
						}(iblock.Hash(), iblockNumber.Uint64(), bw, parentHdr)
					}
				}
			}
		}
		dWitness = time.Since(tWitness)
	}

	envHeaderNumber, err := requireHeaderNumber(envCopy.header, "mining header number unavailable")
	if err != nil {
		return err
	}
	// The Entire event re-marshals every transaction of the block — ~23k
	// encodings and a full snapshot copy on a saturated 480M block, ~100ms of
	// the leader's critical path. Its only consumers are on-demand RPC
	// subscriptions (minedBlock / aggregate-sign streams), which almost never
	// exist on a validator, so ask before building rather than after.
	if w.chainConfig.IsBeijing(envHeaderNumber.Uint64()) &&
		event.GlobalEvent.HasSubscribers(common.MinedEntireEvent{}) {
		txs := make([][]byte, len(envCopy.txs))
		for i, tx := range envCopy.txs {
			txs[i], err = tx.Marshal()
			if err != nil {
				return fmt.Errorf("failed to marshal transaction %d: %w", i, err)
			}
		}
		concreteHeader, ok := iblock.Header().(*block.Header)
		if !ok || concreteHeader == nil {
			return errors.New("invalid finalized block header type")
		}

		entire := state.Entire{
			Header:       concreteHeader,
			Transactions: txs,
			Snap:         ibs.Snap(),
			Proof:        types.Hash{},
		}
		cs := ibs.CodeHashes()
		hs := make(state.HashCodes, 0, len(cs))
		for k, v := range cs {
			hs = append(hs, &state.HashCode{Hash: k, Code: v})
		}
		sort.Sort(hs)

		event.GlobalEvent.Send(common.MinedEntireEvent{
			Entire: state.EntireCode{
				Codes:    hs,
				Headers:  needHeaders,
				Entire:   entire,
				Rewards:  rewards,
				CoinBase: envCopy.coinbase,
			},
		})
	}

	tSnap := time.Now()
	w.updateSnapshot(envCopy, rewards)
	dSnap := time.Since(tSnap)
	if len(envCopy.txs) > 1000 {
		log.Info("miner: assemble breakdown",
			"txs", len(envCopy.txs), "copy", dCopy, "finalize", dFinalize-dCopy,
			"packet", dPacket, "snapshot", dSnap,
			"other", time.Since(tCommitStart)-dFinalize-dPacket-dSnap)
	}

	commitRewardCount := 0
	if b := iblock.Body(); b != nil {
		commitRewardCount = len(b.Reward())
	}

	// Speculative build: park the finished task for the next view's trigger
	// instead of sealing it. Sealing signs and PROPOSES; that right arrives
	// only with TriggerBlockProduction, which will collect this via
	// takeSpecTask when the guessed parent is confirmed.
	// Snapshot the block's effects while the state is still ours alone: the
	// write path mutates the objects, and a chained build may read the
	// snapshot at any time after the seal.
	var post *state.PostState
	if internal.MinerAdoptAppends() {
		post = state.CapturePostState(ibs)
	}

	if speculative {
		var tParked time.Time
		if contentionDiagEnabled {
			tParked = time.Now()
		}
		w.specMu.Lock()
		w.specTask = &task{
			receipts: envCopy.receipts, block: iblock, createdAt: time.Now(), state: ibs, nopay: unpay, post: post, exec: exec,
			finalize: dFinalize, witness: dWitness, assemble: time.Since(tCommitStart),
			buildBeginAt: start, specParkedAt: tParked, triggerAt: triggerAt,
		}
		w.specParent = specParent
		w.specMu.Unlock()
		num := uint64(0)
		if n := iblock.Number64(); n != nil {
			num = n.Uint64()
		}
		log.Info("miner: speculative build parked", "number", num, "txs", envCopy.tcount, "parent", specParent.Hex()[:12], "tMs", time.Now().UnixMilli())
		return nil
	}

	var tTaskSent time.Time
	if contentionDiagEnabled {
		tTaskSent = time.Now()
	}
	select {
	case w.taskCh <- &task{
		receipts: envCopy.receipts, block: iblock, createdAt: time.Now(), state: ibs, nopay: unpay, post: post, exec: exec,
		finalize: dFinalize, witness: dWitness, assemble: time.Since(tCommitStart),
		buildBeginAt: start, triggerAt: triggerAt, paceEnterAt: tPaceEnter, paceDur: dPace, taskChSentAt: tTaskSent,
	}:
		blockNumber := uint64(0)
		if number := iblock.Number64(); number != nil {
			blockNumber = number.Uint64()
		}
		log.Debug("Commit new sealing work",
			"number", blockNumber,
			"sealhash", w.engine.SealHash(iblock.Header()),
			"txs", envCopy.tcount,
			"gas", iblock.GasUsed(),
			"elapsed", common.PrettyDuration(time.Since(start)),
			"headerTime", time.Unix(int64(iblock.Time()), 0).Format(time.RFC3339),
			"rewardCount", commitRewardCount,
		)
	case <-w.ctx.Done():
		return w.ctx.Err()
	}
	return nil
}

func copyReceipts(receipts []*block.Receipt) []*block.Receipt {
	result := make([]*block.Receipt, len(receipts))
	for i, r := range receipts {
		cpy := *r
		result[i] = &cpy
	}
	return result
}

func (w *worker) pendingBlockAndReceipts() (block.IBlock, block.Receipts) {
	w.snapshotMu.Lock()
	defer w.snapshotMu.Unlock()
	if w.snapshotBlock == nil && w.snapshotEnv != nil {
		w.snapshotBlock = block.NewBlockFromReceipt(
			w.snapshotEnv.header,
			w.snapshotEnv.txs,
			nil,
			w.snapshotEnv.receipts,
			w.snapshotRewards,
		)
	}
	return w.snapshotBlock, w.snapshotReceipts
}

// updateSnapshot stores the material for the pending block/receipts RPC
// snapshot. The block itself is assembled lazily on first read (see
// pendingBlockAndReceipts): NewBlockFromReceipt computes the bloom, the tx
// root and the receipt root over the whole block — pure waste on the leader's
// critical path when no one queries pending state.
//
// The caller passes the COPY commit() already made (envCopy), whose lifetime
// ends when commit returns and which nothing mutates afterwards, so the
// snapshot can hold its slices directly.
func (w *worker) updateSnapshot(env *environment, rewards []*block.Reward) {
	w.snapshotMu.Lock()
	defer w.snapshotMu.Unlock()

	w.snapshotEnv = env
	w.snapshotRewards = rewards
	w.snapshotBlock = nil // invalidate; rebuilt on demand
	w.snapshotReceipts = env.receipts
}

func signalToErr(signal int32) error {
	switch signal {
	case commitInterruptNewHead:
		return errBlockInterruptedByNewHead
	case commitInterruptResubmit:
		return errBlockInterruptedByRecommit
	case commitInterruptTimeout:
		return errBlockInterruptedByTimeout
	default:
		return fmt.Errorf("undefined signal %d", signal)
	}
}

// parentExecutedResult returns this node's execution result of the parent
// of header: the record kept with a block this node sealed, else the
// stored result of an applied block (or the parent header's own fields
// before the deferred-execution fork). This ALWAYS means the literal
// parent's own result -- used by the speculative tree's own reload
// (NewMinerRootComputer) to recognise "this build continues my own last
// block," a concern unrelated to what a header being built CARRIES. For
// that, see headerStampResult.
func (w *worker) parentExecutedResult(tx kv.Getter, header *block.Header) (rawdb.ExecutedResult, error) {
	if header.Number.Uint64() == 0 {
		return rawdb.ExecutedResult{}, fmt.Errorf("block 0 has no parent")
	}
	return w.executedResultOfAncestor(tx, header.ParentHash, header.Number.Uint64()-1)
}

// headerStampResult returns the execution result that a header being built
// at header.Number/header.Time must carry: the parent's under depth-1
// (today, identical to parentExecutedResult), or the GRANDPARENT's once
// N42_DEFERRED_EXECUTION_DEPTH2_TIME activates depth-2 for this header (S55,
// docs/QS_BLOCK_TIME_BUDGET.md 6f7). Every node has the grandparent's result
// once the grandparent is imported -- no speculation needed even at a
// tenure hand-over, unlike parentExecutedResult's own sealedExec fast path,
// which exists only because a chained in-tenure build's own PARENT is often
// still unwritten.
func (w *worker) headerStampResult(tx kv.Getter, header *block.Header) (rawdb.ExecutedResult, error) {
	number := header.Number.Uint64()
	if number < 2 || w.chainConfig == nil || !w.chainConfig.IsDeferredExecutionDepth2(header.Time) {
		return w.parentExecutedResult(tx, header)
	}
	parentHeader := w.ancestorHeader(tx, header.ParentHash, number-1)
	if parentHeader == nil {
		return rawdb.ExecutedResult{}, fmt.Errorf("parent %x of block %d unknown", header.ParentHash[:8], number)
	}
	return w.executedResultOfAncestor(tx, parentHeader.ParentHash, number-2)
}

// ancestorHeader returns the header for (hash, number): this node's own
// sealed-but-maybe-unwritten block first (a chained in-tenure build), else
// the stored header.
func (w *worker) ancestorHeader(tx kv.Getter, hash types.Hash, number uint64) *block.Header {
	w.mu.RLock()
	ownBlk := w.sealedByHash[hash]
	w.mu.RUnlock()
	if ownBlk != nil {
		if h, ok := ownBlk.Header().(*block.Header); ok {
			return h
		}
	}
	return rawdb.ReadHeader(tx, hash, number)
}

// executedResultOfAncestor is parentExecutedResult's and headerStampResult's
// shared core: this node's own sealed-but-maybe-unwritten record for
// ancestorHash first, else the stored result of an applied block (or the
// ancestor header's own fields, pre-fork).
func (w *worker) executedResultOfAncestor(tx kv.Getter, ancestorHash types.Hash, ancestorNumber uint64) (rawdb.ExecutedResult, error) {
	w.mu.RLock()
	r, ok := w.sealedExec[ancestorHash]
	ownBlk := w.sealedByHash[ancestorHash]
	w.mu.RUnlock()
	if ok {
		return r, nil
	}
	var ah *block.Header
	if ownBlk != nil {
		ah, _ = ownBlk.Header().(*block.Header)
	}
	if ah == nil {
		ah = rawdb.ReadHeader(tx, ancestorHash, ancestorNumber)
	}
	if ah == nil {
		return rawdb.ExecutedResult{}, fmt.Errorf("ancestor %x (block %d) unknown", ancestorHash[:8], ancestorNumber)
	}
	return internal.ExecutedResultOfHeader(w.chainConfig, tx, ah)
}
