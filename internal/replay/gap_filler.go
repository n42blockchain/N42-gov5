// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Timestamp gap-filling helper for replay. CalcGapBlockTimes returns
// the synthetic timestamps for empty blocks inserted between two real
// blocks whose delta exceeds the configured tolerance, keeping block
// production continuous across source-chain outages.

package replay

// CalcGapBlockTimes returns timestamps for empty blocks to insert between
// prevTime and currTime to maintain continuous block production.
// Returns nil if the gap is within tolerance.
//
// maxBlocks caps the number of synthetic blocks for a single gap (0 = unlimited).
// A real multi-day outage (or a startup gap) would otherwise synthesize hundreds
// of thousands of empty blocks into ONE batch transaction and OOM the process;
// the cap keeps the fill bounded — beyond the cap the next real block simply jumps
// forward in time. Use CalcGapBlockTimesCapped; the 4-arg form is the uncapped
// legacy behavior kept for callers/tests.
func CalcGapBlockTimes(prevTime, currTime, period, tolerance uint64) []uint64 {
	return CalcGapBlockTimesCapped(prevTime, currTime, period, tolerance, 0)
}

// CalcGapBlockTimesCapped is CalcGapBlockTimes with a hard cap on the number of
// synthetic blocks per gap.
func CalcGapBlockTimesCapped(prevTime, currTime, period, tolerance, maxBlocks uint64) []uint64 {
	if prevTime == 0 || currTime <= prevTime || period == 0 {
		return nil
	}
	delta := currTime - prevTime
	if delta <= tolerance {
		return nil
	}
	var times []uint64
	for t := prevTime + period; t < currTime; t += period {
		times = append(times, t)
		if maxBlocks > 0 && uint64(len(times)) >= maxBlocks {
			break
		}
	}
	return times
}
