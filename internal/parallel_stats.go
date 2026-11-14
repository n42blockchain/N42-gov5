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
// Parallel execution counters, wave meters and finalize-span notes.

package internal

import (
	"sync/atomic"
	"time"

	"github.com/n42blockchain/N42/modules/state"
)

// noteFinalizeSpan fills the Finalize span's split (FinishParallel, t0 its
// start) from the root computation ibs just ran: the engine's part before
// the root (rewards), the root's preamble steps up to the post-state hook,
// the hook, the root computer and whatever follows it.
func (ph *ProcessPhases) noteFinalizeSpan(t0 time.Time, ibs *state.IntraBlockState) {
	rp := ibs.LastRootPrepPhases()
	if rp.Enter.IsZero() || rp.Enter.Before(ph.finEngineStart) {
		return
	}
	ph.FinEngine = rp.Enter.Sub(ph.finEngineStart)
	ph.RootPrep = rp
	ph.FinPreHook = rp.Enter.Sub(t0) + rp.Pending + rp.Detach + rp.Mat + rp.Merge + rp.Sort + rp.Loop + rp.Tail
	ph.FinAfter = ph.Finalize - ph.FinPreHook - rp.Hook - rp.Compute
}

// parallelFeeReruns counts Block-STM runs executed a second time because a
// recipient carried code while fee credits were deferred.
var parallelFeeReruns atomic.Int64

// waveWorkNs sums, over every parallel run of the process, the wall time
// the workers spent inside transaction bodies and their store writes
// (benchmarks: the exec wave's work without the scheduling gaps).
var waveWorkNs atomic.Int64

// waveCPUMeter, when set (benchmarks), reads the process CPU; every
// parallel run then adds its executor run's CPU to waveCPUNs.
var (
	waveCPUMeter func() time.Duration
	waveCPUNs    atomic.Int64
)
