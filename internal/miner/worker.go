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
	"errors"
	"fmt"
	"runtime/debug"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	mapset "github.com/deckarep/golang-set"
	"github.com/holiman/uint256"
	"golang.org/x/sync/errgroup"

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
	vm2 "github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/internal/zkprover"
	"github.com/n42blockchain/N42/lib/kv/layered"
	"github.com/n42blockchain/N42/log"
	event "github.com/n42blockchain/N42/modules/event/v2"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state"
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

type task struct {
	receipts  []*block.Receipt
	state     *state.IntraBlockState
	block     block.IBlock
	createdAt time.Time
	nopay     map[types.Address]*uint256.Int
}

type newWorkReq struct {
	interrupt *atomic.Int32
	noempty   bool
	timestamp int64
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

	snapshotMu       sync.RWMutex
	snapshotBlock    block.IBlock
	snapshotReceipts block.Receipts
}

func newWorker(ctx context.Context, group *errgroup.Group, chainConfig *params.ChainConfig, engine consensus.Engine, bc common.IBlockChain, txsPool common.ITxsPool, isLocalBlock func(header *block.Header) bool, init bool, minerConf conf.MinerConfig) *worker {
	c, cancel := context.WithCancel(ctx)
	worker := &worker{
		engine:           engine,
		chain:            bc,
		txsPool:          txsPool,
		chainConfig:      chainConfig,
		startCh:          make(chan struct{}, 1),
		group:            group,
		isLocalBlock:     isLocalBlock,
		ctx:              c,
		cancel:           cancel,
		taskCh:           make(chan *task),
		newWorkCh:        make(chan *newWorkReq),
		resultCh:         make(chan block.IBlock),
		pendingTasks:     make(map[types.Hash]*task),
		minerConf:        minerConf,
		resubmitAdjustCh: make(chan *intervalAdjust, resubmitAdjustChanSize),
		bundlePool:       builder.NewBundlePool(),
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
			err := w.commitWork(req.interrupt, req.noempty, req.timestamp)
			if err != nil {
				log.Error("runLoop error", "err", err)
			}
		}
	}
}

func (w *worker) resultLoop() error {
	defer w.cancel()
	defer w.stop()

	for {
		select {
		case <-w.ctx.Done():
			return w.ctx.Err()
		case blk := <-w.resultCh:
			if blk == nil {
				continue
			}
			blockNumber, err := requireBlockNumber(blk, "block number unavailable")
			if err != nil {
				log.Error("Ignoring sealed block", "err", err, "hash", blk.Hash())
				continue
			}

			// Short circuit when receiving duplicate result caused by resubmitting.
			if w.chain.HasBlock(blk.Hash(), blockNumber.Uint64()) {
				continue
			}

			var (
				sealhash = w.engine.SealHash(blk.Header())
				hash     = blk.Hash()
			)
			w.mu.RLock()
			task, exist := w.pendingTasks[sealhash]
			w.mu.RUnlock()
			if !exist {
				log.Error("Block found but no relative pending task", "number", blockNumber.Uint64(), "sealhash", sealhash, "hash", hash)
				continue
			}

			// Deep copy receipts and set block location fields to prevent write-write conflicts
			// when different blocks share the same sealhash.
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

			err = w.chain.WriteBlockWithState(blk, receipts, task.state, task.nopay)
			if err != nil {
				log.Error("Failed writing block to chain", "err", err)
				miningErrorsCounter.Inc()
				continue
			}
			w.mu.Lock()
			delete(w.pendingTasks, sealhash)
			w.mu.Unlock()
			blocksMinedCounter.Inc()
			blockMiningTimer.UpdateDuration(task.createdAt)

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
				"number", blockNumber.Uint64(),
				"used gas", blk.GasUsed(),
				"diff", blk.Difficulty().Uint64(),
				"headerTime", time.Unix(int64(blk.Time()), 0).Format(time.RFC3339),
				"verifierCount", verifierCount,
				"rewardCount", rewardCount,
				"elapsed", common.PrettyDuration(time.Since(task.createdAt)),
				"txs", len(blk.Transactions()))

			if err = w.chain.SealedBlock(blk); err != nil {
				log.Error("Failed Broadcast block to p2p network", "err", err)
				continue
			}
			if concrete, ok := blk.(*block.Block); ok {
				event.GlobalEvent.Send(common.ChainHighestBlock{Block: *concrete, Inserted: true})
			}
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
			if sealHash == prev {
				continue
			}
			interrupt()
			stopCh, prev = make(chan struct{}), sealHash
			w.mu.Lock()
			w.pendingTasks[sealHash] = task
			w.mu.Unlock()

			hash := task.block.Hash()
			stateRoot := task.block.StateRoot()
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
				log.Debug("send task", "sealHash", sealHash, "hash", hash, "stateRoot", stateRoot)
			}
		}
	}
}

func (w *worker) commitWork(interrupt *atomic.Int32, noempty bool, timestamp int64) error {
	start := time.Now()
	w.mu.RLock()
	coinbase := w.coinbase
	w.mu.RUnlock()
	if w.isRunning() && coinbase == (types.Address{}) {
		return errors.New("coinbase is empty")
	}

	current, err := w.prepareWork(&generateParams{timestamp: uint64(timestamp), coinbase: coinbase})
	if err != nil {
		log.Error("cannot prepare work", "err", err)
		return err
	}

	tx, err := w.chain.DB().BeginRo(w.ctx)
	if err != nil {
		log.Error("work.commitWork failed", err)
		return err
	}
	defer tx.Rollback()

	var stateReader state.StateReader = state.NewPlainStateReader(tx)
	if cache := layered.ExtractCache(w.chain.DB()); cache != nil {
		stateReader = state.NewCachedStateReader(stateReader, cache)
	}

	// Wrap state reader with TracingReader when JMT is enabled to record
	// all state accesses for witness generation.
	var tracingReader *witness.TracingReader
	if bc, ok := w.chain.(*internal.BlockChain); ok && bc.IsJMTEnabled() {
		tracingReader = witness.NewTracingReader(stateReader)
		stateReader = tracingReader
	}

	stateWriter := state.NewNoopWriter()
	ibs := state.New(stateReader)
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

	err = w.fillTransactions(interrupt, current, ibs, getHeader)
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
	}

	if err = w.commit(current, stateWriter, ibs, start, headers, tracingReader); err != nil {
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
			timestamp = time.Now().Unix()
			commit(false, commitInterruptNewHead)

		case blockEvent := <-newBlockCh:
			clearPending(blockEvent.Block.Number64())
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

func (w *worker) fillTransactions(interrupt *atomic.Int32, env *environment, ibs *state.IntraBlockState, getHeader func(hash types.Hash, number uint64) *block.Header) error {
	// Pre-allocate with estimated max tx count to avoid append-growth
	// in the hot commit loop. 21000 is the minimum gas per tx.
	estCap := int(env.gasPool.Gas() / 21000)
	if estCap > 1024 {
		estCap = 1024 // cap to avoid over-allocation on high gas limits
	}
	env.txs = make([]*transaction.Transaction, 0, estCap)
	env.receipts = make(block.Receipts, 0, estCap)

	header := env.header
	noop := state.NewNoopWriter()
	vmConfig := vm2.Config{}

	commitTx := func(txn *transaction.Transaction) error {
		ibs.Prepare(txn.Hash(), types.Hash{}, env.tcount)
		gasSnap := env.gasPool.Gas()
		snap := ibs.Snapshot()
		log.Debug("addTransactionsToMiningBlock", "txn hash", txn.Hash())

		coinbase := env.coinbase
		receipt, _, err := internal.ApplyTransaction(w.chainConfig, internal.GetHashFn(header, getHeader), w.engine, &coinbase, env.gasPool, ibs, noop, env.header, txn, &header.GasUsed, vmConfig)
		if err != nil {
			ibs.RevertToSnapshot(snap)
			env.gasPool = new(common.GasPool).AddGas(gasSnap)
			return err
		}

		env.txs = append(env.txs, txn)
		env.receipts = append(env.receipts, receipt)
		return nil
	}

	headerNumber, err := requireHeaderNumber(header, "mining header number unavailable")
	if err != nil {
		return err
	}
	blockNumber := headerNumber.Uint64()

	// Phase 1: Execute MEV bundles at block top (highest-paying first).
	if w.bundlePool != nil {
		bundles := w.bundlePool.GetBundles(blockNumber, header.Time)
		for _, bundle := range bundles {
			if env.gasPool.Gas() < params.TxGas {
				break
			}
			// Try to commit entire bundle atomically.
			snap := ibs.Snapshot()
			gasSnap := env.gasPool.Gas()
			startTxCount := len(env.txs)
			startReceiptCount := len(env.receipts)
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

			if !bundleOk {
				// Revert entire bundle.
				ibs.RevertToSnapshot(snap)
				env.gasPool = new(common.GasPool).AddGas(gasSnap)
				env.txs = env.txs[:startTxCount]
				env.receipts = env.receipts[:startReceiptCount]
			}
		}
	}

	// Phase 2: Fill remaining space with regular transactions sorted by effective tip.
	pending := w.txsPool.Pending(false)
	if len(pending) == 0 {
		return nil
	}

	txSet := builder.NewTxByPriceAndNonce(pending, header.BaseFee)
	log.Tracef("fillTransactions pending accounts:%d", len(pending))

	for {
		if interrupt != nil {
			if signal := interrupt.Load(); signal != commitInterruptNone {
				return signalToErr(signal)
			}
		}
		if env.gasPool.Gas() < params.TxGas {
			log.Trace("Not enough gas for further transactions", "have", env.gasPool, "want", params.TxGas)
			break
		}

		tx := txSet.Peek()
		if tx == nil {
			break
		}

		err := commitTx(tx)
		switch {
		case err == nil:
			env.tcount++
			txSet.Shift() // Move to next tx from same account
		case errors.Is(err, internal.ErrGasLimitReached):
			txSet.Pop() // Skip this account entirely
		case errors.Is(err, internal.ErrNonceTooHigh):
			txSet.Pop() // Nonce gap, skip account
		case errors.Is(err, internal.ErrNonceTooLow):
			txSet.Shift() // Try next nonce from same account
		default:
			log.Error("miningCommitTx failed", "error", err)
			txSet.Shift()
		}
	}

	// Prune old bundles.
	if w.bundlePool != nil {
		w.bundlePool.PruneBundles(blockNumber)
	}

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
		b, _ := w.chain.GetBlockByHash(param.parentHash)
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
		gasPool:   new(common.GasPool).AddGas(header.GasLimit),
	}

	for _, ancestor := range w.chain.GetBlocksFromHash(parent.ParentHash, 3) {
		env.family.Add(ancestor.Hash())
		env.ancestors.Add(ancestor.Hash())
	}

	return env
}

func (w *worker) commit(env *environment, writer state.WriterWithChangeSets, ibs *state.IntraBlockState, start time.Time, needHeaders []*block.Header, tracingReader *witness.TracingReader) error {
	if !w.isRunning() {
		return nil
	}

	envCopy := env.copy()
	iblock, rewards, unpay, err := w.engine.FinalizeAndAssemble(w.chain, envCopy.header, ibs, envCopy.txs, nil, envCopy.receipts)
	if err != nil {
		return err
	}

	// Generate witness after FinalizeAndAssemble when JMT + TracingReader are active.
	if tracingReader != nil {
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
	}

	envHeaderNumber, err := requireHeaderNumber(envCopy.header, "mining header number unavailable")
	if err != nil {
		return err
	}
	if w.chainConfig.IsBeijing(envHeaderNumber.Uint64()) {
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

	w.updateSnapshot(envCopy, rewards)

	commitRewardCount := 0
	if b := iblock.Body(); b != nil {
		commitRewardCount = len(b.Reward())
	}

	select {
	case w.taskCh <- &task{receipts: envCopy.receipts, block: iblock, createdAt: time.Now(), state: ibs, nopay: unpay}:
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
	w.snapshotMu.RLock()
	defer w.snapshotMu.RUnlock()
	return w.snapshotBlock, w.snapshotReceipts
}

func (w *worker) updateSnapshot(env *environment, rewards []*block.Reward) {
	w.snapshotMu.Lock()
	defer w.snapshotMu.Unlock()

	w.snapshotBlock = block.NewBlockFromReceipt(
		env.header,
		env.txs,
		nil,
		env.receipts,
		rewards,
	)
	w.snapshotReceipts = copyReceipts(env.receipts)
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
