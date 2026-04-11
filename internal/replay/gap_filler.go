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
func CalcGapBlockTimes(prevTime, currTime, period, tolerance uint64) []uint64 {
	if prevTime == 0 || currTime <= prevTime {
		return nil
	}
	delta := currTime - prevTime
	if delta <= tolerance {
		return nil
	}
	var times []uint64
	for t := prevTime + period; t < currTime; t += period {
		times = append(times, t)
	}
	return times
}
