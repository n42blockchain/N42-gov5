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
