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
	"encoding/binary"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/parallel"
)

// The preparation of a queued child's parallel run (P13e).
//
// Under load a follower's child block is always queued before its parent
// publishes the snapshot it executes ahead on, and waits there; its ahead
// run then spent its first ~20 ms (affinityMs) on work that needs no state:
// every transaction's message and hash and its affinity key, and the first
// wave's partition. A block queued by its availability check starts that
// preparation in the background once its senders are settled (the block's
// sender job is done), and the ahead run adopts the result instead of
// computing it, when it was computed for the same transactions, signer,
// base fee, worker count and wave modes; otherwise the run computes it as
// before. The keys come from the same function (txAffinityKey) and the
// partition from the executor's own code, so an adopted preparation is the
// one the run would compute.
//
// The preparation only reads the transactions (their wire senders, the
// sender and hash memos are atomic): it starts after the sender job's
// background workers, and every import of the block claims it before its
// own sender recovery -- an ahead run in runParallelMode, any other import
// in lagPipeTake -- cancelling one not started yet and waiting for one
// running, so nothing writes a sender while it reads them.
// N42_AHEAD_PREP=0 disables it.

// aheadPrepOn is N42_AHEAD_PREP (default on; "0", "false" or "off"
// disables).
var aheadPrepOn = parseLagPipeline(os.Getenv("N42_AHEAD_PREP"))

// SetAheadPrepForTest switches the preparation and returns the restore.
func SetAheadPrepForTest(on bool) func() {
	prev := aheadPrepOn
	aheadPrepOn = on
	return func() { aheadPrepOn = prev }
}

// aheadPrepCap bounds the preparations held (each holds a message per
// transaction, ~40 MB at 163k): the lowest blocks are kept.
const aheadPrepCap = 2

// States of an aheadPrep.
const (
	prepPending int32 = iota
	prepRunning
	prepDone
	prepCancelled
)

// aheadPrep is one block's preparation.
type aheadPrep struct {
	hash   types.Hash
	number uint64
	state  atomic.Int32
	// done closes once a preparation that started running finished.
	done chan struct{}

	// Set before done closes; ok when the preparation is usable.
	ok       bool
	txs      []*transaction.Transaction
	signer   transaction.Signer
	baseFee  *uint256.Int
	engine   bool
	prepared []preparedTx
	aff      *parallel.AffinityPrep
	// base: the block's base prefill (ahead_base_prefill.go), started once
	// the preparation is computed; nil when off or not started.
	base *aheadBase
	// dropping: the block will not use the preparation; its base prefill
	// stops reading at the next key.
	dropping atomic.Bool
}

type aheadPreps struct {
	mu sync.Mutex
	m  map[types.Hash]*aheadPrep
}

// Ahead runs that adopted a preparation, and those that found none usable
// (process-wide, since start).
var aheadPrepHits, aheadPrepMisses atomic.Uint64

// aheadPrepWorkersForTest, when positive, is the goroutines a preparation
// computes the keys on.
var aheadPrepWorkersForTest int

func aheadPrepWorkers() int {
	if aheadPrepWorkersForTest > 0 {
		return aheadPrepWorkersForTest
	}
	n := senderRecoveryFanout() / 2
	if n < 1 {
		n = 1
	}
	return n
}

// aheadPrepUsable: blocks of this chain may be prepared (they can run on
// the ahead path).
func (bc *BlockChain) aheadPrepUsable() bool {
	return aheadPrepOn && bc.lagPipeUsable()
}

// newAheadPrep registers a pending preparation of b, or returns nil when
// there is one already or the cap keeps lower blocks.
func (bc *BlockChain) newAheadPrep(b *block.Block) *aheadPrep {
	if b == nil || !bc.aheadPrepUsable() {
		return nil
	}
	pr := &aheadPrep{hash: b.Hash(), number: b.Number64().Uint64(), done: make(chan struct{})}
	ps := &bc.aheadPreps
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if ps.m == nil {
		ps.m = make(map[types.Hash]*aheadPrep, aheadPrepCap)
	}
	if _, dup := ps.m[pr.hash]; dup {
		return nil
	}
	if len(ps.m) >= aheadPrepCap {
		var top *aheadPrep
		for _, o := range ps.m {
			if top == nil || o.number > top.number {
				top = o
			}
		}
		// Only a preparation not started yet is evicted: a running one
		// stays claimable, so an import of its block waits for it.
		if top.number <= pr.number || !top.state.CompareAndSwap(prepPending, prepCancelled) {
			return nil
		}
		delete(ps.m, top.hash)
	}
	ps.m[pr.hash] = pr
	return pr
}

// startAheadPrep starts recovering b's senders (the block's sender job) and,
// with the preparation on, prepares its parallel run once they are settled.
func (bc *BlockChain) startAheadPrep(b *block.Block) {
	pr := bc.newAheadPrep(b)
	if pr == nil {
		bc.startSenderJob(b, nil)
		return
	}
	bc.startSenderJob(b, func(ok bool) {
		if !ok {
			bc.dropAheadPrep(pr.hash)
			return
		}
		go bc.runAheadPrep(pr, b)
	})
}

// runAheadPrep computes pr unless an import claimed or it was dropped first.
func (bc *BlockChain) runAheadPrep(pr *aheadPrep, b *block.Block) {
	if !pr.state.CompareAndSwap(prepPending, prepRunning) {
		return
	}
	defer func() {
		// A panic leaves the preparation unusable; the run computes its own.
		if recover() != nil {
			pr.ok = false
		}
		pr.state.Store(prepDone)
		close(pr.done)
	}()
	sp, ok := bc.process.(*StateProcessor)
	if !ok || bc.ResolveFrames(b) != nil {
		return
	}
	hdr, ok := b.Header().(*block.Header)
	if !ok || hdr.Number == nil {
		return
	}
	if !sp.ParallelApplicable(b) {
		// The block runs serially.
		return
	}
	txs := b.Transactions()
	signer := transaction.MakeSignerWithTimestamp(sp.config, hdr.Number.ToBig(), hdr.Time)
	prepared, aff := sp.prepareParallelRun(txs, signer, hdr.BaseFee, parallelWorkers(), aheadPrepWorkers())
	if aff == nil {
		return
	}
	pr.txs, pr.signer, pr.baseFee, pr.engine = txs, signer, hdr.BaseFee, sp.engine != nil
	pr.prepared, pr.aff, pr.ok = prepared, aff, true
	if fn := aheadPrepDoneForTest; fn != nil {
		fn(pr)
	}
	if bc.aheadBaseUsable() {
		pr.base = bc.startAheadBase(hdr, txs, prepared, sp, pr.dropping.Load)
	}
}

// aheadPrepDoneForTest runs on a computed preparation before it is
// published (tests: make it mismatch its run).
var aheadPrepDoneForTest func(pr *aheadPrep)

// claimAheadPrep removes h's preparation for its import: one not started is
// cancelled (nil), one running is waited for, and so is its base prefill
// (one still waiting for the tree is ended). The wait is returned.
func (bc *BlockChain) claimAheadPrep(h types.Hash) (*aheadPrep, time.Duration) {
	if bc == nil {
		return nil, 0
	}
	ps := &bc.aheadPreps
	ps.mu.Lock()
	pr := ps.m[h]
	delete(ps.m, h)
	ps.mu.Unlock()
	if pr == nil || pr.state.CompareAndSwap(prepPending, prepCancelled) || pr.state.Load() == prepCancelled {
		return nil, 0
	}
	t0 := time.Now()
	<-pr.done
	pr.base.halt()
	wait := time.Since(t0)
	if !pr.ok {
		return nil, wait
	}
	return pr, wait
}

// dropAheadPrep forgets h's preparation (the block left the queue or will
// not use it), waiting for one running: once it returns nothing reads the
// block's senders for it.
func (bc *BlockChain) dropAheadPrep(h types.Hash) {
	if bc == nil {
		return
	}
	bc.aheadPreps.mu.Lock()
	if pr := bc.aheadPreps.m[h]; pr != nil {
		pr.dropping.Store(true)
	}
	bc.aheadPreps.mu.Unlock()
	bc.claimAheadPrep(h)
}

// AheadPrepStats returns the ahead runs that adopted a preparation and
// those that found none usable (since start).
func AheadPrepStats() (hits, misses uint64) {
	return aheadPrepHits.Load(), aheadPrepMisses.Load()
}

// matches reports whether pr was computed for this run.
func (pr *aheadPrep) matches(txs []*transaction.Transaction, signer transaction.Signer, baseFee *uint256.Int, engine bool) bool {
	if pr == nil || !pr.ok || len(pr.txs) != len(txs) || len(txs) == 0 || pr.txs[0] != txs[0] ||
		pr.txs[len(txs)-1] != txs[len(txs)-1] || signer == nil || pr.signer == nil || !pr.signer.Equal(signer) ||
		pr.engine != engine || len(pr.prepared) != len(txs) {
		return false
	}
	switch {
	case pr.baseFee == nil && baseFee == nil:
		return true
	case pr.baseFee == nil || baseFee == nil:
		return false
	}
	return pr.baseFee.Eq(baseFee)
}

// prepareParallelRun computes what a parallel run's affinity pass computes
// (PrepareAffinity with txAffinityKey) for txs on an executor of workers:
// every transaction's message and hash and the first wave's keys and
// partition, keys on goroutines goroutines. aff is nil when the pass would
// compute nothing.
func (p *StateProcessor) prepareParallelRun(txs []*transaction.Transaction, signer transaction.Signer, baseFee *uint256.Int, workers, goroutines int) ([]preparedTx, *parallel.AffinityPrep) {
	prepared := make([]preparedTx, len(txs))
	aff := parallel.PrepareAffinityFor(len(txs), workers, func(i int) uint64 {
		return p.txAffinityKey(txs, prepared, i, signer, baseFee, false)
	}, goroutines)
	return prepared, aff
}

// txAffinityKey is a parallel run's affinity key of txs[i]: its sender's
// first 8 bytes, else its index. It first builds the transaction's message
// and hash into prepared[i] (preparedTx) unless they are there.
func (p *StateProcessor) txAffinityKey(txs []*transaction.Transaction, prepared []preparedTx, i int, signer transaction.Signer, baseFee *uint256.Int, stateless bool) uint64 {
	tx := txs[i]
	if pm := &prepared[i]; !pm.ok && signer != nil {
		if tx.AsMessageInto(&pm.msg, signer, baseFee) == nil {
			NormalizeExecutionMessage(&pm.msg, stateless, p.engine != nil)
			pm.hash = tx.Hash()
			pm.ok = true
		}
	}
	if from := tx.From(); from != nil {
		return binary.LittleEndian.Uint64(from[:8])
	}
	if signer != nil {
		if from, err := transaction.Sender(signer, tx); err == nil {
			return binary.LittleEndian.Uint64(from[:8])
		}
	}
	return uint64(i)
}
