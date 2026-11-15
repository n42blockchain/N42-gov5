// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"sync"
	"sync/atomic"
	"testing"
)

// TestPublishedValidatorSetTracksCurrent: every path that sets the current
// set publishes the same pointer at the same step.
func TestPublishedValidatorSetTracksCurrent(t *testing.T) {
	vs := NewValidatorSet(makeTestValidators(4), 1)
	for name, em := range map[string]*EpochManager{
		"static":    NewEpochManager(vs),
		"length":    NewEpochManagerWithLength(vs, 10),
		"fromEpoch": NewEpochManagerFromEpoch(vs, 10, 3),
	} {
		check := func(step string) {
			t.Helper()
			if em.PublishedValidatorSet() != em.CurrentValidatorSet() {
				t.Fatalf("%s %s: published set differs from current", name, step)
			}
		}
		check("new")
		em.StageNextEpoch(makeTestValidators(7), 2)
		check("staged") // staging does not change the current set
		if !em.AdvanceEpoch(10) {
			t.Fatalf("%s: no advance", name)
		}
		check("advanced")
		if em.CurrentValidatorSet().Len() != 7 {
			t.Fatalf("%s: advance did not switch", name)
		}
		em.RestoreActiveSet(9, makeTestValidators(5), 1)
		check("restored")
	}
	if (&EpochManager{}).PublishedValidatorSet() != nil {
		t.Fatal("zero manager publishes a set")
	}
}

// TestPublishedValidatorSetConcurrent: lock-free readers racing the
// engine's locked epoch advances only ever see a set the locked read
// returned at some point, and see each change once the writer has made it.
func TestPublishedValidatorSetConcurrent(t *testing.T) {
	const epochs = 200
	sets := make([]*ValidatorSet, epochs+1)
	for i := range sets {
		sets[i] = NewValidatorSet(makeTestValidators(4), 1)
	}
	em := NewEpochManagerWithLength(sets[0], 1)
	var mu sync.Mutex // the engine's lock
	known := map[*ValidatorSet]bool{sets[0]: true}
	for _, s := range sets {
		known[s] = true
	}
	var stop atomic.Bool
	var wg sync.WaitGroup
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !stop.Load() {
				if vs := em.PublishedValidatorSet(); !known[vs] {
					t.Error("published an unknown set")
					return
				}
			}
		}()
	}
	for i := 1; i <= epochs; i++ {
		mu.Lock()
		em.nextSet = sets[i]
		em.AdvanceEpoch(uint64(i))
		mu.Unlock()
		// After the writer's critical section, a reader sees the change.
		if em.PublishedValidatorSet() != sets[i] {
			t.Fatalf("epoch %d not visible after the change", i)
		}
	}
	stop.Store(true)
	wg.Wait()
}
