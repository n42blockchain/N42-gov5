// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package miner

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

// A17 + B4: on Profile 1 a leader proposes no earlier than blockIntervalMs
// after it first held the parent and stamps max(parent.time, floor(now)) at
// that point. The header time must therefore be taken after the wait, not at
// the build trigger (mixed round 3: gov5 stamped the trigger second and
// published one interval later, 0 s after a foreign parent).
func TestCommitWorkProfile1StampsAfterPacing(t *testing.T) {
	f := mnTNewChainFixture(t)
	w := mnTBareWorker(t, f, &mnTStubTxsPool{})
	w.coinbase = f.Coinbase
	w.resubmitAdjustCh = make(chan *intervalAdjust, 10)
	atomic.StoreInt32(&w.running, 1)

	// The fixture chain cannot start as Profile 1; the build reads the
	// profile from the worker's own copy of the config.
	cfg := *f.Config
	cfg.N42Profile = params.N42Profile1
	w.chainConfig = &cfg
	if !w.chainConfig.IsProfile1() {
		t.Fatal("worker config is not Profile 1")
	}
	const interval = 2000 * time.Millisecond
	w.minerConf.BlockIntervalMs = uint64(interval / time.Millisecond)

	// The zero parent hash builds on the local head (the genesis here).
	parent := f.Chain.CurrentBlock()
	trigger := time.Now()
	w.parentHeld.note(parent.Hash(), trigger)

	if err := w.commitWork(nil, false, trigger.Unix(), types.Hash{}, false, trigger); err != nil {
		t.Fatalf("commitWork: %v", err)
	}
	handed := time.Now()
	var tk *task
	select {
	case tk = <-w.taskCh:
	default:
		t.Fatal("commitWork did not send a task")
	}
	if handed.Sub(trigger) < interval {
		t.Fatalf("handed to the sealer %v after the parent was held, want >= %v", handed.Sub(trigger), interval)
	}
	got := tk.block.Time()
	want := max(parent.Time(), uint64(trigger.Add(interval).Unix()))
	if got < want {
		t.Fatalf("header time %d stamped before the pacing wait; want >= %d (floor(trigger+interval))", got, want)
	}
	if got > uint64(handed.Unix()) {
		t.Fatalf("header time %d is after the hand-off second %d", got, handed.Unix())
	}
}

// A speculative build is stamped when it runs, during the previous view,
// before the A17 wait. On Profile 1 a parked block whose second has passed by
// the time the wait ends must not be handed to the sealer; one stamped in the
// current second still is.
func TestSpeculativeHitProfile1DropsStaleTimestamp(t *testing.T) {
	f := mnTNewChainFixture(t)
	parentHash := types.Hash{0x5e}
	setup := func(blockTime uint64) (*worker, block.IBlock) {
		w := mnTBareWorker(t, f, &mnTStubTxsPool{})
		w.coinbase = f.Coinbase
		w.resubmitAdjustCh = make(chan *intervalAdjust, 10)
		atomic.StoreInt32(&w.running, 1)
		cfg := *f.Config
		cfg.N42Profile = params.N42Profile1
		w.chainConfig = &cfg
		w.minerConf.BlockIntervalMs = 1000
		// Held long enough ago that the A17 wait is already over.
		w.parentHeld.note(parentHash, time.Now().Add(-5*time.Second))
		blk := block.NewBlock(&block.Header{ParentHash: parentHash, Number: uint256.NewInt(1), Time: blockTime}, nil)
		w.specMu.Lock()
		w.specTask, w.specParent = &task{block: blk}, parentHash
		w.specMu.Unlock()
		return w, blk
	}

	// Fresh: stamped a second ahead of now, so still current after the wait.
	w, blk := setup(uint64(time.Now().Unix()) + 1)
	if err := w.commitWork(nil, false, time.Now().Unix(), parentHash, false, time.Now()); err != nil {
		t.Fatalf("fresh speculative hit: %v", err)
	}
	select {
	case tk := <-w.taskCh:
		if tk.block != blk {
			t.Fatal("fresh speculative hit handed over a different block")
		}
	default:
		t.Fatal("fresh speculative block was not handed to the sealer")
	}

	// Stale: stamped seconds before the wait ended. The rebuild that follows
	// cannot find the made-up parent and fails; what matters is that the
	// stale block never reaches the sealer.
	w, blk = setup(uint64(time.Now().Unix()) - 3)
	_ = w.commitWork(nil, false, time.Now().Unix(), parentHash, false, time.Now())
	select {
	case tk := <-w.taskCh:
		if tk.block == blk {
			t.Fatal("speculative block stamped before the pacing wait was handed to the sealer")
		}
	default:
	}
}
