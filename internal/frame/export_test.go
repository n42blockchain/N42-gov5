// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package frame

import (
	"context"

	"github.com/n42blockchain/N42/common/types"
)

// AttachForTest attaches r to its store without starting workers, so a
// test steps the recovery itself with StepForTest.
func (r *SenderRecoverer) AttachForTest() { r.st.SetRecoverer(r) }

// StepForTest takes the next job the way a worker does, recovers it and
// returns its frame hash (false when nothing is queued).
func (r *SenderRecoverer) StepForTest() (types.Hash, bool) {
	ctx, cancel := context.WithCancel(context.Background())
	if len(r.prio) == 0 && len(r.jobs) == 0 {
		cancel()
		return types.Hash{}, false
	}
	defer cancel()
	j, ok := r.next(ctx)
	if !ok {
		return types.Hash{}, false
	}
	r.recoverOne(j)
	r.pending.Add(-1)
	return j.h, true
}

// Outcomes of a LocalAger step (StepForTest).
const (
	OutcomeNone     = outcomeNone
	OutcomeAttested = outcomeAttested
	OutcomeLocal    = outcomeLocal
)

// StepForTest recovers the next frame the ager queued, on the caller's
// goroutine, and returns its hash and outcome (false when none is queued).
func (a *LocalAger) StepForTest() (types.Hash, int, bool) {
	select {
	case j := <-a.jobs:
		return j.h, a.run(j), true
	default:
		return types.Hash{}, 0, false
	}
}

// SetAfterRecoverForTest runs fn between a hedge job's local recovery and
// its caching.
func (h *HeadHedge) SetAfterRecoverForTest(fn func(types.Hash)) { h.afterRecover = fn }
