// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// schedule.go — the DATC epoch schedule, a build/verify shared contract.
//
// Per-level epoch length E_d = clamp(α·16^d / C̄, 1, 2^22): every node sees ~α
// changes per its own epoch, equalizing the change rate across depths. build
// writes records keyed by epochOf(d, block); verify resolves them with the
// same schedule loaded from DatcMeta. Keep this the single definition so the
// two sides never drift.

package main

// epochSchedule holds per-depth epoch lengths.
type epochSchedule struct{ e [maxChgDepth + 1]uint64 }

func newSchedule(alpha, cbar float64) epochSchedule {
	var s epochSchedule
	for d := 0; d <= maxChgDepth; d++ {
		e := alpha * pow16(d) / cbar
		if e < 1 {
			e = 1
		}
		if e > 1<<22 {
			e = 1 << 22
		}
		s.e[d] = uint64(e)
	}
	return s
}

func pow16(d int) float64 {
	v := 1.0
	for i := 0; i < d; i++ {
		v *= 16
	}
	return v
}

func (s epochSchedule) epochOf(d int, block uint64) uint64 { return block / s.e[d] }
