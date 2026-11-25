// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Profile-1 slot grid (mixed production standard A17/B4, AM27): slots are
// counted from the genesis timestamp G with interval I; slot s starts at
// G + s*I. There is no per-block drift.

package params

import "time"

// Profile1SlotSeconds returns the slot interval I in whole seconds, or 0 when
// the chain is not profile 1 or blockIntervalMs is not a positive multiple of
// one second (header timestamps are whole seconds, so the grid needs it).
func (c *ChainConfig) Profile1SlotSeconds() uint64 {
	if !c.IsProfile1() || c.HotStuff == nil {
		return 0
	}
	ms := c.HotStuff.BlockIntervalMs
	if ms == 0 || ms%1000 != 0 {
		return 0
	}
	return ms / 1000
}

// Profile1SlotTime is the timestamp a leader stamps (A17): the start of slot
// s = max(sP+1, ceil((now-G)/I)) where sP = (parentTime-G)/I, i.e. the first
// slot boundary after the parent's slot that is not in the past. It returns
// G + s*I. interval is in seconds and must be positive.
func Profile1SlotTime(genesis, interval, parentTime uint64, now time.Time) uint64 {
	if interval == 0 {
		return parentTime
	}
	var sP uint64
	if parentTime > genesis {
		sP = (parentTime - genesis) / interval
	}
	s := sP + 1
	gNs := int64(genesis) * int64(time.Second)
	if d := now.UnixNano() - gNs; d > 0 {
		iNs := int64(interval) * int64(time.Second)
		sNow := uint64((d + iNs - 1) / iNs) // ceil
		if sNow > s {
			s = sNow
		}
	}
	return genesis + s*interval
}

// Profile1OnGrid reports whether t is a slot start: (t - G) mod I == 0 and
// t >= G (B4).
func Profile1OnGrid(genesis, interval, t uint64) bool {
	return interval != 0 && t >= genesis && (t-genesis)%interval == 0
}
