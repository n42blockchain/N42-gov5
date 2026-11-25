// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package miner

import (
	"sync/atomic"
	"testing"
	"time"

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
