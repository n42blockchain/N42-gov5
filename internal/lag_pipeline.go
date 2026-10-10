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

package internal

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/internal/consensus/misc"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// Execution off the consensus path (docs/QS_MULTI_MILLION_DESIGN.md P2a).
//
// Available blocks: frame blocks this node holds every frame of -- checked
// by the availability vote, or sealed here -- but has not executed yet.
// Their headers are findable (GetHeaderByHash) so a leader can build on a
// parent it has not executed and the vote check can walk to the block whose
// result a header carries.
//
// Lag executor: available blocks are imported (executed, root, written)
// in parent order on one background goroutine, never on the vote path. A
// block is imported once its parent is this node's applied head; a block
// whose parent is stored on another branch is imported with branch-switch
// authority after a short grace, as the future queue does. Every import
// tells consensus (the imported hook), which retries deferred commits and
// resumes a leader view waiting on the lag gate.

// maxAvailableBlocks bounds the available-block cache. Each entry holds a
// decoded body (~0.3 GB at 310k transactions), and what legitimately waits
// here is small: at depth k the leader gate keeps the canonical chain at
// most k+1 blocks ahead of execution, plus the losing siblings of a few
// views; imported and forgotten blocks leave at once (forgetAvailable).
// The bound only caps a stalled executor.
const maxAvailableBlocks = 32

// lagAuthorizedGrace is how long a queued block whose parent is stored on a
// branch that is not applied waits before it is imported with switch
// authority (the future queue retries the same way every 2 s).
const lagAuthorizedGrace = time.Second

// lagRetryInterval is the executor's poll while blocks are queued.
const lagRetryInterval = 50 * time.Millisecond

type availableState struct {
	mu   sync.Mutex
	m    map[types.Hash]*block.Block
	fifo []types.Hash
}

type lagQueued struct {
	blk      *block.Block
	since    time.Time
	lastAuth time.Time // last import attempt with switch authority
	stuckLog bool
}

type lagExecutor struct {
	mu      sync.Mutex
	queued  map[types.Hash]*lagQueued
	kick    chan struct{}
	started atomic.Bool
	hook    atomic.Pointer[func(hash, txHash types.Hash)]

	imported atomic.Uint64
	lastNum  atomic.Uint64
}

// RememberAvailable records a frame block this node holds every frame of
// and has not executed (an availability-checked proposal, or a block sealed
// here without execution). Its header becomes findable by GetHeaderByHash.
func (bc *BlockChain) RememberAvailable(b *block.Block) {
	if b == nil {
		return
	}
	a := &bc.avail
	a.mu.Lock()
	if a.m == nil {
		a.m = make(map[types.Hash]*block.Block)
	}
	h := b.Hash()
	if _, ok := a.m[h]; !ok {
		a.m[h] = b
		a.fifo = append(a.fifo, h)
		for len(a.fifo) > maxAvailableBlocks {
			delete(a.m, a.fifo[0])
			a.fifo = a.fifo[1:]
		}
	}
	a.mu.Unlock()
	if hdr, ok := b.Header().(*block.Header); ok {
		bc.RememberSealedHeader(hdr)
	}
}

// AvailableBlock returns an available (unexecuted) block by hash, or nil.
func (bc *BlockChain) AvailableBlock(hash types.Hash) *block.Block {
	a := &bc.avail
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.m[hash]
}

// forgetAvailable drops hash from the available blocks: it is written (its
// header and body are in the store now) or it can never import here. An
// available block holds its decoded body -- every transaction with its
// sender, ~0.9 KB each -- so keeping imported blocks until the FIFO bound
// pushed them out held 256 blocks of them (S93/S94: +0.2-0.35 GB of live
// heap per imported block).
func (bc *BlockChain) forgetAvailable(hash types.Hash) {
	a := &bc.avail
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.m[hash]; !ok {
		return
	}
	delete(a.m, hash)
	for i, h := range a.fifo {
		if h == hash {
			a.fifo = append(a.fifo[:i], a.fifo[i+1:]...)
			break
		}
	}
}

// HasAvailableBlock reports whether hash is available here (held, checked
// or sealed, possibly not executed yet) or already stored.
func (bc *BlockChain) HasAvailableBlock(hash types.Hash) bool {
	return bc.AvailableBlock(hash) != nil
}

// headerAnywhere reads a header from tx, else from the available blocks.
func (bc *BlockChain) headerAnywhere(tx kv.Getter, hash types.Hash, number uint64) *block.Header {
	if tx != nil {
		if h := rawdb.ReadHeader(tx, hash, number); h != nil {
			return h
		}
	}
	if b := bc.AvailableBlock(hash); b != nil && b.Number64().Uint64() == number {
		if h, ok := b.Header().(*block.Header); ok {
			return h
		}
	}
	return nil
}

// deferredTarget returns the header whose executed result header must
// carry: the ancestor DeferredDepthAt(header) levels up (the parent at depth
// 1), or genesis when the chain is shorter. parent is header's parent.
// lookup resolves the ancestors above parent.
func deferredTarget(header, parent *block.Header, lookup func(hash types.Hash, number uint64) *block.Header) (*block.Header, error) {
	k := block.DeferredDepthAt(header.Number.Uint64())
	t := parent
	for i := 1; i < k; i++ {
		if t.Number.Uint64() == 0 {
			return t, nil
		}
		next := lookup(t.ParentHash, t.Number.Uint64()-1)
		if next == nil {
			return nil, fmt.Errorf("%w: ancestor %x of block %d not known (depth %d)", ErrDeferredResultUnknown, t.ParentHash[:8], header.Number.Uint64(), k)
		}
		t = next
	}
	return t, nil
}

// LaggedTarget returns the header of the block whose executed result a
// header built on header.ParentHash must carry: the parent at depth 1, the
// block DeferredDepthAt-1 levels above the parent at depth k. own resolves
// headers this node sealed and has not written (nil = none).
func (bc *BlockChain) LaggedTarget(tx kv.Getter, header *block.Header, own func(types.Hash) *block.Header) (*block.Header, error) {
	lookup := func(hash types.Hash, number uint64) *block.Header {
		if own != nil {
			if h := own(hash); h != nil {
				return h
			}
		}
		return bc.headerAnywhere(tx, hash, number)
	}
	if header.Number.Uint64() == 0 {
		return nil, errors.New("deferred execution: genesis has no parent")
	}
	parent := lookup(header.ParentHash, header.Number.Uint64()-1)
	if parent == nil {
		return nil, fmt.Errorf("%w: parent %x of block %d not known", ErrDeferredResultUnknown, header.ParentHash[:8], header.Number.Uint64())
	}
	return deferredTarget(header, parent, lookup)
}

// LaggedExecutedResult returns this node's executed result that header must
// carry (see LaggedTarget) and the header it belongs to.
func (bc *BlockChain) LaggedExecutedResult(tx kv.Getter, header *block.Header, own func(types.Hash) *block.Header) (rawdb.ExecutedResult, *block.Header, error) {
	target, err := bc.LaggedTarget(tx, header, own)
	if err != nil {
		return rawdb.ExecutedResult{}, nil, err
	}
	r, err := executedOrSeededResult(bc.chainConfig, tx, target)
	return r, target, err
}

// LagReady is the leader gate under deferred execution depth k > 1: a node
// may propose the child of parent (height N) once it has executed N-k.
// waitFor names the block whose execution it waits for (the parent itself
// when even its header is unknown). A zero parent is the consensus
// service's "no LockedQC block yet" (the genesis QC: the first views after
// a reseed or a consensus-state reset), which the builder resolves to the
// canonical head; so does the gate (S91: it waited on the zero hash for
// ever and the fleet built nothing).
func (bc *BlockChain) LagReady(parent types.Hash) (ready bool, waitFor types.Hash, err error) {
	if bc.ChainDB == nil {
		return true, types.Hash{}, nil
	}
	if parent == (types.Hash{}) {
		cur := bc.currentBlock.Load()
		if cur == nil {
			return true, types.Hash{}, nil
		}
		parent = cur.Hash()
	}
	err = bc.ChainDB.View(bc.ctx, func(tx kv.Tx) error {
		var ph *block.Header
		if n := rawdb.ReadHeaderNumber(tx, parent); n != nil {
			ph = rawdb.ReadHeader(tx, parent, *n)
		}
		if ph == nil {
			if b := bc.AvailableBlock(parent); b != nil {
				ph, _ = b.Header().(*block.Header)
			}
		}
		if ph == nil {
			waitFor = parent
			return nil
		}
		if bc.chainConfig == nil || !bc.chainConfig.IsDeferredExecution(ph.Time) {
			ready = true
			return nil
		}
		child := &block.Header{Number: ph.Number.Clone(), ParentHash: parent}
		child.Number.AddUint64(child.Number, 1)
		target, terr := deferredTarget(child, ph, func(h types.Hash, n uint64) *block.Header { return bc.headerAnywhere(tx, h, n) })
		if terr != nil {
			waitFor = parent
			return nil
		}
		if _, rerr := executedOrSeededResult(bc.chainConfig, tx, target); rerr != nil {
			waitFor = target.Hash()
			return nil
		}
		ready = true
		return nil
	})
	return ready, waitFor, err
}

// checkAvailability is the availability vote (N42_VOTE_ON_AVAILABILITY=1)
// for a frame block: the header's static fields against its parent, the
// body's frame hash list against the header, every frame held (pulled if
// missing), the gas sum within the limit, and -- when this node has already
// executed the block whose result the header carries -- that result. Nothing
// is executed. A passing block is remembered as available and queued for
// the lag executor. retry asks the caller to run the check again (the parent
// is not known yet, or frames could not be pulled).
func (bc *BlockChain) checkAvailability(blk block.IBlock, hdr *block.Header, body *block.Body) (retry bool, err error) {
	number := hdr.Number.Uint64()
	tx, err := bc.ChainDB.BeginRo(context.Background())
	if err != nil {
		return true, err
	}
	defer tx.Rollback()
	lookup := func(h types.Hash, n uint64) *block.Header { return bc.headerAnywhere(tx, h, n) }
	parent := lookup(hdr.ParentHash, number-1)
	if parent == nil {
		return true, fmt.Errorf("%w: parent %x of block %d not known", ErrDeferredResultUnknown, hdr.ParentHash[:8], number)
	}
	if err := checkStaticHeader(bc, hdr, parent); err != nil {
		return false, err
	}
	if got, want := hdr.TxHash, block.BodyTxRoot(body, hdr.Time); got != want {
		return false, fmt.Errorf("availability: block %d transactions root %x is not its body's %x", number, got[:8], want[:8])
	}
	if err := bc.ResolveFrames(blk); err != nil {
		return true, err
	}
	var gas uint64
	for _, t := range body.Txs {
		gas += t.Gas()
		if gas > hdr.GasLimit {
			return false, fmt.Errorf("availability: frame block %d exceeds its gas limit", number)
		}
	}
	// The lagged result: checked now when this node has it (an honest
	// follower one block behind usually has), otherwise at import, where a
	// mismatch is the equivocation the import reports.
	if target, terr := deferredTarget(hdr, parent, lookup); terr == nil {
		if want, rerr := executedOrSeededResult(bc.chainConfig, tx, target); rerr == nil {
			if err := compareDeferredResult(hdr, want); err != nil {
				reportLaggedEquivocation(hdr, target, err)
				return false, err
			}
		}
	}
	if b, ok := blk.(*block.Block); ok {
		bc.RememberAvailable(b)
		bc.EnqueueLagImport(b)
	}
	return false, nil
}

// checkStaticHeader checks what a header must satisfy against its parent
// without executing anything: height, time, gas limit, gas used against
// the limit of the block it reports, and the base fee rule.
func checkStaticHeader(bc *BlockChain, hdr, parent *block.Header) error {
	number := hdr.Number.Uint64()
	if parent.Number.Uint64()+1 != number {
		return fmt.Errorf("availability: block %d on parent at height %d", number, parent.Number.Uint64())
	}
	if hdr.Time <= parent.Time {
		return fmt.Errorf("availability: block %d time %d not after its parent's %d", number, hdr.Time, parent.Time)
	}
	if hdr.GasLimit == 0 {
		return fmt.Errorf("availability: block %d has no gas limit", number)
	}
	if bc.chainConfig != nil && bc.chainConfig.IsLondon(number) && hdr.BaseFee != nil && bc.chainConfig.IsLondon(parent.Number.Uint64()) {
		want := misc.CalcBaseFee(bc.chainConfig, parent)
		if want != nil && hdr.BaseFee.ToBig().Cmp(want) != 0 {
			return fmt.Errorf("availability: block %d base fee %s, parent implies %s", number, hdr.BaseFee, want)
		}
	}
	return nil
}

// metricLaggedEquivocation counts headers whose lagged execution fields
// differ from this node's own result of the block they report.
var laggedEquivocations atomic.Uint64

// LaggedEquivocations returns how many headers carried a lagged result that
// differs from this node's execution (since start).
func LaggedEquivocations() uint64 { return laggedEquivocations.Load() }

// reportLaggedEquivocation logs a header whose lagged execution fields
// differ from this node's result: the proposer (or a quorum, if it was
// committed) disagrees with this node's execution of target. The block is
// refused (no vote, bad block on import) through the deferred-execution
// mismatch path.
func reportLaggedEquivocation(hdr, target *block.Header, err error) {
	laggedEquivocations.Add(1)
	log.Error("deferred execution: lagged result equivocation", "number", hdr.Number.Uint64(), "hash", hdr.Hash(),
		"coinbase", hdr.Coinbase, "reports", target.Number.Uint64(), "reportsHash", target.Hash(), "err", err)
}

// SetLagImportedHook attaches the callback the lag executor runs after each
// block it imported (consensus: retry deferred commits, resume a waiting
// leader view).
func (bc *BlockChain) SetLagImportedHook(fn func(hash, txHash types.Hash)) {
	if fn == nil {
		bc.lagExec.hook.Store(nil)
		return
	}
	bc.lagExec.hook.Store(&fn)
}

// LagImportQueued reports whether hash waits in the lag executor (or is
// being imported by it): the push path leaves its import to the executor.
func (bc *BlockChain) LagImportQueued(hash types.Hash) bool {
	le := &bc.lagExec
	le.mu.Lock()
	defer le.mu.Unlock()
	_, ok := le.queued[hash]
	return ok
}

// LagStats returns the blocks imported by the lag executor, the height of
// the last one and the blocks waiting.
func (bc *BlockChain) LagStats() (imported, lastNumber uint64, queued int) {
	le := &bc.lagExec
	le.mu.Lock()
	queued = len(le.queued)
	le.mu.Unlock()
	return le.imported.Load(), le.lastNum.Load(), queued
}

// EnqueueLagImport hands an available block to the lag executor.
func (bc *BlockChain) EnqueueLagImport(b *block.Block) {
	if b == nil {
		return
	}
	le := &bc.lagExec
	le.mu.Lock()
	if le.queued == nil {
		le.queued = make(map[types.Hash]*lagQueued)
		le.kick = make(chan struct{}, 1)
	}
	if _, ok := le.queued[b.Hash()]; !ok {
		le.queued[b.Hash()] = &lagQueued{blk: b, since: time.Now()}
	}
	kick := le.kick
	le.mu.Unlock()
	if le.started.CompareAndSwap(false, true) {
		go bc.lagLoop()
	}
	select {
	case kick <- struct{}{}:
	default:
	}
	// A child queued while its parent is being rooted starts ahead.
	bc.lagPipeLaunch()
}

func (bc *BlockChain) lagLoop() {
	le := &bc.lagExec
	t := time.NewTicker(lagRetryInterval)
	defer t.Stop()
	ctx := bc.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-le.kick:
		case <-t.C:
		}
		for bc.lagStep() {
		}
		// Idle: stop until the next enqueue, which restarts the loop. The
		// re-check after clearing the flag closes the window where an
		// enqueue saw the loop still running.
		le.mu.Lock()
		idle := len(le.queued) == 0
		le.mu.Unlock()
		if idle {
			le.started.Store(false)
			le.mu.Lock()
			again := len(le.queued) > 0
			le.mu.Unlock()
			if !again || !le.started.CompareAndSwap(false, true) {
				return
			}
		}
	}
}

// lagStep imports the lowest ready queued block and reports whether it did
// anything (the loop runs again at once).
func (bc *BlockChain) lagStep() bool {
	le := &bc.lagExec
	le.mu.Lock()
	if len(le.queued) == 0 {
		le.mu.Unlock()
		return false
	}
	list := make([]*lagQueued, 0, len(le.queued))
	for _, q := range le.queued {
		list = append(list, q)
	}
	le.mu.Unlock()
	sort.Slice(list, func(i, j int) bool { return list[i].blk.Number64().Uint64() < list[j].blk.Number64().Uint64() })

	var committedNum uint64
	if committed := bc.currentBlock.Load(); committed != nil && committed.Number64() != nil {
		committedNum = committed.Number64().Uint64()
	}
	for _, q := range list {
		b := q.blk
		n := b.Number64().Uint64()
		if bc.blockStored(b.Hash(), n) {
			bc.lagDone(b, false)
			return true
		}
		if n <= committedNum {
			// A height already decided without this block: a losing fork.
			bc.lagForget(b.Hash())
			return true
		}
		ready := bc.lagParentReady(b.ParentHash(), n-1)
		authorized := false
		if !ready {
			if !bc.blockStored(b.ParentHash(), n-1) || time.Since(q.since) < lagAuthorizedGrace ||
				time.Since(q.lastAuth) < lagAuthorizedGrace {
				continue
			}
			authorized = true
			q.lastAuth = time.Now()
		}
		var err error
		bc.lagPipeArm(b.Hash())
		if authorized {
			_, err = bc.InsertChainAuthorized([]block.IBlock{b})
		} else {
			_, err = bc.InsertChain([]block.IBlock{b})
		}
		written := err == nil && bc.blockStored(b.Hash(), n)
		bc.lagPipeAfterImport(b, written)
		if written {
			bc.lagDone(b, true)
			return true
		}
		if err != nil && errors.Is(err, consensus.ErrExecutionInvalid) {
			// Deterministically invalid (the lagged-result mismatch among
			// others): it can never import here.
			log.Warn("lag executor: block invalid, dropped", "number", n, "hash", b.Hash().Hex()[:12], "err", err)
			bc.lagForget(b.Hash())
			return true
		}
		if !q.stuckLog && time.Since(q.since) > 5*time.Second {
			q.stuckLog = true
			log.Warn("lag executor: block not importable yet", "number", n, "hash", b.Hash().Hex()[:12], "authorized", authorized, "err", err)
		}
	}
	return false
}

// lagParentReady: the parent is the state a non-authorized import runs on.
func (bc *BlockChain) lagParentReady(parent types.Hash, number uint64) bool {
	if bc.qmdbEnabled {
		return bc.AppliedHeadIsExactly(parent, number)
	}
	return bc.blockStored(parent, number)
}

// blockStored reports whether the header of hash at number is written.
func (bc *BlockChain) blockStored(hash types.Hash, number uint64) bool {
	if bc.blockCache != nil && bc.blockCache.Contains(hash) {
		return true
	}
	if bc.ChainDB == nil {
		return false
	}
	ok := false
	_ = bc.ChainDB.View(context.Background(), func(tx kv.Tx) error {
		ok = rawdb.HasHeader(tx, hash, number)
		return nil
	})
	return ok
}

func (bc *BlockChain) lagForget(hash types.Hash) {
	bc.lagUnqueue(hash)
	bc.lagPipeForget(hash)
}

// lagUnqueue drops hash from the executor's queue and the available blocks.
func (bc *BlockChain) lagUnqueue(hash types.Hash) {
	le := &bc.lagExec
	le.mu.Lock()
	delete(le.queued, hash)
	le.mu.Unlock()
	bc.forgetAvailable(hash)
}

func (bc *BlockChain) lagDone(b *block.Block, imported bool) {
	if !imported {
		bc.lagForget(b.Hash())
		return
	}
	// Written by this executor: its child's ahead execution stays usable
	// (lagPipeAfterImport marked it).
	bc.lagUnqueue(b.Hash())
	le := &bc.lagExec
	le.imported.Add(1)
	le.lastNum.Store(b.Number64().Uint64())
	if fn := le.hook.Load(); fn != nil {
		(*fn)(b.Hash(), b.TxHash())
	}
}
