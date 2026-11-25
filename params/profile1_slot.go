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

// Profile1SlotMs returns the slot interval I in milliseconds (AM27a): any
// positive blockIntervalMs, or 0 when the chain is not profile 1 or carries
// none. I % 1000 == 0 selects the exact whole-second grid rules (B4).
func (c *ChainConfig) Profile1SlotMs() uint64 {
	if !c.IsProfile1() || c.HotStuff == nil {
		return 0
	}
	return c.HotStuff.BlockIntervalMs
}

// Profile1SlotStartMs is the leader's slot start in unix milliseconds
// (AM27a): slot s starts at G*1000 + s*I, with s = max(sP+1, ceil((now_ms -
// G*1000)/I)). For I % 1000 == 0 it is exactly Profile1SlotTime * 1000. For
// a non-whole-second I the parent's slot sP is derived conservatively from
// its stamp: the smallest slot whose floor(start/1000) >= parentTime, so the
// new slot is never earlier than the one after that. The header is stamped
// floor(start/1000). intervalMs must be positive.
func Profile1SlotStartMs(genesis, intervalMs, parentTime uint64, now time.Time) uint64 {
	if intervalMs == 0 {
		return parentTime * 1000
	}
	if intervalMs%1000 == 0 {
		return Profile1SlotTime(genesis, intervalMs/1000, parentTime, now) * 1000
	}
	var sP uint64
	if parentTime > genesis {
		sP = ((parentTime-genesis)*1000 + intervalMs - 1) / intervalMs // ceil
	}
	s := sP + 1
	gMs := int64(genesis) * 1000
	if d := now.UnixMilli() - gMs; d > 0 {
		if sNow := uint64((d + int64(intervalMs) - 1) / int64(intervalMs)); sNow > s {
			s = sNow
		}
	}
	return genesis*1000 + s*intervalMs
}
