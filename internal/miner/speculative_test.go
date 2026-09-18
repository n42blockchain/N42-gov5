// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package miner

import (
	"sync/atomic"
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

// TestTakeSpecTaskSemantics pins the parking-spot contract: a matching parent
// returns the task exactly once; any probe — hit or miss — clears the spot, so
// a stale guess can never be proposed later.
func TestTakeSpecTaskSemantics(t *testing.T) {
	w := &worker{}
	parent := types.Hash{0xaa}
	other := types.Hash{0xbb}
	parked := &task{}

	// Miss on empty.
	if got := w.takeSpecTask(parent); got != nil {
		t.Fatalf("empty spot returned a task")
	}

	// Hit clears.
	w.specMu.Lock()
	w.specTask, w.specParent = parked, parent
	w.specMu.Unlock()
	if got := w.takeSpecTask(parent); got != parked {
		t.Fatalf("matching parent did not return the parked task")
	}
	if got := w.takeSpecTask(parent); got != nil {
		t.Fatalf("second take returned a task; the spot must clear on hit")
	}

	// Mismatch clears too: the stale guess must not survive to a later view.
	w.specMu.Lock()
	w.specTask, w.specParent = parked, parent
	w.specMu.Unlock()
	if got := w.takeSpecTask(other); got != nil {
		t.Fatalf("mismatched parent returned the parked task")
	}
	if got := w.takeSpecTask(parent); got != nil {
		t.Fatalf("stale guess survived a mismatched probe")
	}
}

// TestTriggerEvictsSpeculativeOnly pins the queue policy: a real trigger must
// never be dropped because a speculative request holds the slot, and must
// never evict another real request.
func TestTriggerEvictsSpeculativeOnly(t *testing.T) {
	w := &worker{newWorkCh: make(chan *newWorkReq, 1), running: 1}
	m := &Miner{worker: w}

	// Slot held by a speculative request: the real trigger evicts it.
	w.newWorkCh <- &newWorkReq{speculative: true, interrupt: new(atomic.Int32)}
	m.TriggerBlockProduction(types.Hash{0x01})
	select {
	case got := <-w.newWorkCh:
		if got.speculative {
			t.Fatalf("speculative request survived a real trigger")
		}
		if got.parentHash != (types.Hash{0x01}) {
			t.Fatalf("queued request has wrong parent")
		}
	default:
		t.Fatalf("no request queued after eviction")
	}

	// Slot held by a REAL request: the new trigger is dropped, the old kept.
	held := &newWorkReq{speculative: false, parentHash: types.Hash{0x02}, interrupt: new(atomic.Int32)}
	w.newWorkCh <- held
	m.TriggerBlockProduction(types.Hash{0x03})
	select {
	case got := <-w.newWorkCh:
		if got.parentHash != (types.Hash{0x02}) {
			t.Fatalf("real request was evicted by a later trigger: got parent %x", got.parentHash)
		}
	default:
		t.Fatalf("queued real request vanished")
	}
}

// TestSpeculativeInterruptSignalled pins the pre-emption path: a real trigger
// must signal the interrupt of a speculative build in flight.
func TestSpeculativeInterruptSignalled(t *testing.T) {
	w := &worker{newWorkCh: make(chan *newWorkReq, 1), running: 1}
	m := &Miner{worker: w}

	specInt := new(atomic.Int32)
	w.activeSpecInterrupt.Store(specInt)
	m.TriggerBlockProduction(types.Hash{0x04})
	if specInt.Load() != commitInterruptNewHead {
		t.Fatalf("active speculative build was not interrupted: %d", specInt.Load())
	}
	// Drain for cleanliness.
	<-w.newWorkCh
}

// TestSpeculativeForSameParentIsNotInterrupted: the guess in flight IS the
// block the trigger wants. Interrupting it throws away a nearly finished build
// and starts over -- which is where round 35zzq's 406 ms between the QC and the
// next seal went, once deferred execution made the vote path shorter than a
// full build.
func TestSpeculativeForSameParentIsNotInterrupted(t *testing.T) {
	w := &worker{newWorkCh: make(chan *newWorkReq, 1), running: 1}
	m := &Miner{worker: w}

	parent := types.Hash{0x07}
	specInt := new(atomic.Int32)
	w.activeSpecInterrupt.Store(specInt)
	p := parent
	w.activeSpecParent.Store(&p)

	m.TriggerBlockProduction(parent)

	if got := specInt.Load(); got != 0 {
		t.Fatalf("the in-flight guess for this parent was interrupted: %d", got)
	}
	select {
	case req := <-w.newWorkCh:
		if req.parentHash != parent || req.speculative {
			t.Fatalf("queued request = parent %x speculative %v", req.parentHash, req.speculative)
		}
	default:
		t.Fatal("the real trigger was not queued")
	}
}

// A guess for another parent is still interrupted: it is building the wrong
// block and the worker is needed now.
func TestSpeculativeForAnotherParentIsInterrupted(t *testing.T) {
	w := &worker{newWorkCh: make(chan *newWorkReq, 1), running: 1}
	m := &Miner{worker: w}

	specInt := new(atomic.Int32)
	w.activeSpecInterrupt.Store(specInt)
	other := types.Hash{0x08}
	w.activeSpecParent.Store(&other)

	m.TriggerBlockProduction(types.Hash{0x09})

	if got := specInt.Load(); got != commitInterruptNewHead {
		t.Fatalf("a guess for another parent was not interrupted: %d", got)
	}
	<-w.newWorkCh
}
