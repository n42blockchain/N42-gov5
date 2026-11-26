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

// A17 + B4 (AM27): on Profile 1 the leader stamps the start of its slot on
// the genesis grid, G + s*I with s = max(sP+1, ceil((now-G)/I)), chosen when
// the build is triggered, and hands the block to the sealer no earlier than
// that second.
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
	const iv = uint64(2)
	w.minerConf.BlockIntervalMs = iv * 1000

	// The zero parent hash builds on the local head (the genesis here).
	parent := f.Chain.CurrentBlock()
	g := f.Chain.GetHeaderByNumber(uint256.NewInt(0)).(*block.Header).Time
	trigger := time.Now()
	w.parentHeld.note(parent.Hash(), trigger)
	lo := params.Profile1SlotTime(g, iv, parent.Time(), trigger)

	if err := w.commitWork(nil, false, trigger.Unix(), types.Hash{}, false, trigger); err != nil {
		t.Fatalf("commitWork: %v", err)
	}
	handed := time.Now()
	hi := params.Profile1SlotTime(g, iv, parent.Time(), handed)
	var tk *task
	select {
	case tk = <-w.taskCh:
	default:
		t.Fatal("commitWork did not send a task")
	}
	got := tk.block.Time()
	if !params.Profile1OnGrid(g, iv, got) {
		t.Fatalf("header time %d is off the grid (G %d, I %d)", got, g, iv)
	}
	if got < lo || got > hi || got < parent.Time()+iv {
		t.Fatalf("header time %d, want the trigger's slot start %d (<= %d)", got, lo, hi)
	}
	if uint64(handed.Unix()) < got {
		t.Fatalf("handed to the sealer at %v, before the stamped slot start %d", handed, got)
	}
}

// A speculative build stamps the slot it targets. On Profile 1 a parked block
// is handed over at that slot's start only if it is still the proposal's
// slot; one stamped for an earlier slot (the view was entered late) is
// rebuilt.
func TestSpeculativeHitProfile1DropsStaleTimestamp(t *testing.T) {
	f := mnTNewChainFixture(t)
	parent := f.Chain.CurrentBlock()
	parentHash := parent.Hash()
	g := f.Chain.GetHeaderByNumber(uint256.NewInt(0)).(*block.Header).Time
	const iv = uint64(1)
	setup := func(blockTime uint64) (*worker, block.IBlock) {
		w := mnTBareWorker(t, f, &mnTStubTxsPool{})
		w.coinbase = f.Coinbase
		w.resubmitAdjustCh = make(chan *intervalAdjust, 10)
		atomic.StoreInt32(&w.running, 1)
		cfg := *f.Config
		cfg.N42Profile = params.N42Profile1
		w.chainConfig = &cfg
		w.minerConf.BlockIntervalMs = iv * 1000
		blk := block.NewBlock(&block.Header{ParentHash: parentHash, Number: uint256.NewInt(1), Time: blockTime}, nil)
		w.specMu.Lock()
		w.specTask, w.specParent = &task{block: blk, createdAt: time.UnixMilli(int64(blockTime) * 1000)}, parentHash
		w.specMu.Unlock()
		return w, blk
	}
	// Retry across a second boundary between the expectation and the build.
	for attempt := 0; ; attempt++ {
		target := params.Profile1SlotTime(g, iv, parent.Time(), time.Now())
		w, blk := setup(target)
		if err := w.commitWork(nil, false, time.Now().Unix(), parentHash, false, time.Now()); err != nil {
			t.Fatalf("fresh speculative hit: %v", err)
		}
		var tk *task
		select {
		case tk = <-w.taskCh:
		default:
			t.Fatal("commitWork did not send a task")
		}
		if tk.block == blk {
			if uint64(time.Now().Unix()) < target {
				t.Fatalf("handed over before the slot start %d", target)
			}
			break
		}
		if attempt == 3 {
			t.Fatal("speculative block stamped with the proposal's slot was not handed to the sealer")
		}
	}

	// Stamped for an earlier slot: never handed to the sealer.
	target := params.Profile1SlotTime(g, iv, parent.Time(), time.Now())
	w, blk := setup(target - iv)
	_ = w.commitWork(nil, false, time.Now().Unix(), parentHash, false, time.Now())
	select {
	case tk := <-w.taskCh:
		if tk.block == blk {
			t.Fatal("speculative block stamped for an earlier slot was handed to the sealer")
		}
	default:
	}
}

// Gate 6c: a gov5 leader that follows a gov5 leader parks its speculative
// build the moment the parent arrives, a whole slot before its own slot
// starts. The parked block holds the pool as it was then -- the parent's own
// transactions, trimmed as stale, and little else -- and was handed over at
// the slot start: 10 transactions over 19 proposals. A block parked that far
// ahead is rebuilt at the slot start from the pool as it is then.
func TestSpeculativeHitProfile1RebuildsStalePool(t *testing.T) {
	const iv = uint64(1)
	for attempt := 0; ; attempt++ {
		f := leadNewQMDBFixture(t)
		parent := f.Chain.CurrentBlock()
		parentHash := parent.Hash()
		g := f.Chain.GetHeaderByNumber(uint256.NewInt(0)).(*block.Header).Time
		pool := &mnTStubTxsPool{}
		w := leadWorker(t, f, pool)
		cfg := *w.chainConfig
		cfg.N42Profile = params.N42Profile1
		w.chainConfig = &cfg
		w.minerConf.BlockIntervalMs = iv * 1000

		// The speculative build runs the moment the parent arrives, against a
		// pool holding nothing it can still include. HotStuff's Prepare
		// stamps it with the slot it targets; the stub engine keeps the
		// miner's timestamp, so pass that slot.
		now := time.Now()
		slot := params.Profile1SlotTime(g, iv, parent.Time(), now)
		if err := w.commitWork(new(atomic.Int32), false, int64(slot), parentHash, true, now); err != nil {
			t.Fatalf("speculative build: %v", err)
		}
		w.specMu.Lock()
		parked := w.specTask
		w.specMu.Unlock()
		if parked == nil {
			t.Fatal("speculative build was not parked")
		}
		if n := len(parked.block.Transactions()); n != 0 {
			t.Fatalf("parked block holds %d transactions, want 0", n)
		}
		// It was parked a whole slot ahead of its start; the pool filled
		// meanwhile.
		parked.createdAt = parked.createdAt.Add(-8 * time.Second)
		pool.pending = mnTByAddr(f.Senders[0], f.mnTSignedTransfer(t, 0, f.Senders[1], uint256.NewInt(1_000)))

		if parked.block.Time() != params.Profile1SlotTime(g, iv, parent.Time(), time.Now()) {
			// A second boundary fell in between; the parked block would be
			// dropped for its stamp, not its pool. Try again.
			if attempt == 3 {
				t.Fatal("could not line the parked stamp up with the slot")
			}
			continue
		}
		if err := w.commitWork(nil, false, time.Now().Unix(), parentHash, false, time.Now()); err != nil {
			t.Fatalf("slot-start build: %v", err)
		}
		var tk *task
		select {
		case tk = <-w.taskCh:
		default:
			t.Fatal("commitWork did not send a task")
		}
		if tk.block == parked.block {
			t.Fatal("speculative block parked a slot ahead was handed to the sealer")
		}
		if got := len(tk.block.Transactions()); got != 1 {
			t.Fatalf("slot-start block holds %d transactions, want 1", got)
		}
		if uint64(time.Now().Unix()) < tk.block.Time() {
			t.Fatalf("handed over before the slot start %d", tk.block.Time())
		}
		return
	}
}

func TestProfile1SpecPoolStale(t *testing.T) {
	start := time.UnixMilli(1_000_000)
	for _, c := range []struct {
		parked time.Time
		want   bool
	}{
		{time.Time{}, true},
		{start, false},
		{start.Add(-profile1SpecMaxLeadMs * time.Millisecond), false},
		{start.Add(-(profile1SpecMaxLeadMs + 1) * time.Millisecond), true},
		{start.Add(-8 * time.Second), true},
		{start.Add(time.Second), false},
	} {
		if got := profile1SpecPoolStale(c.parked, uint64(start.UnixMilli())); got != c.want {
			t.Errorf("parked %v: stale %v, want %v", c.parked, got, c.want)
		}
	}
}
