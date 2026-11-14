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
// Worker support types: tasks, work requests, environment and notifier interfaces.

package miner

import (
	"errors"
	"sync/atomic"
	"time"

	mapset "github.com/deckarep/golang-set"
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state"
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

// blockPersistenceNotifier records execution evidence for a fresh local seal.
// Merely finding an existing body or a sibling is not evidence of applied state.
type blockPersistenceNotifier interface {
	NotifyBlockPersisted(hash, txHash, parentHash types.Hash)
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
	// lagged marks a block built without execution (deferred depth k > 1,
	// lag_build.go): no state, no receipts; it is pushed, proposed and
	// handed to the chain's lag executor instead of written.
	lagged bool
	// mobilePacket is what the mobile-verification packet is built from once
	// this block is sealed (nil when no packet sink is wired).
	mobilePacket *mobilePacketJob

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

	// Frame-referenced build (N42_FRAME_BLOCKS=1): the frames whose
	// transactions are txs, in order, and their encodings.
	frameHashes []types.Hash
	frameData   [][]byte

	// untracedReader is the build's state reader below the mobile read-log
	// recorder and the witness tracer. Reads the block does not make (the
	// stale-nonce trim) go through it; logged, they would sit in the read
	// log the verifiers consume by position and shift every read after them.
	untracedReader state.StateReader
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
	cpy.frameHashes = append([]types.Hash(nil), env.frameHashes...)
	cpy.frameData = append([][]byte(nil), env.frameData...)
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
