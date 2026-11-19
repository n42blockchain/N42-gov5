// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import "github.com/n42blockchain/N42/modules/state/commitment"

// setColdLogFields returns the import's SetCold split for the "blockimport
// phases" line (r97a stamps): setColdWaitUs is the wait for the QMDB tree's
// lock (the previous block's row capture holds it from the hand-off),
// setColdWorkUs the install, setColdHolder the writer that held the lock
// when the wait began ("none" under lookupWaitNote). With
// N42_IMPORT_SETCOLD_LEAN: setColdParked (the lock was held and the source
// was parked instead of waited for), setColdAdoptLagUs/setColdAdoptBy (when
// and by which section a parked source was installed; root = the block's
// own root), and the process counters coldLeanParks/coldLeanAdopts and, in
// check mode, coldLeanChecks/coldLeanMisses.
func (bc *BlockChain) setColdLogFields() []any {
	if !bc.qmdbEnabled || bc.qmdbRootComputer == nil {
		return nil
	}
	s := bc.qmdbRootComputer.LastSetCold()
	f := []any{"setColdWaitUs", s.Wait.Microseconds(), "setColdWorkUs", s.Work.Microseconds(),
		"setColdHolder", commitment.QMDBHolderName(s.Holder), "setColdParked", s.Parked,
		"setColdAdoptLagUs", s.AdoptLag.Microseconds(), "setColdAdoptBy", commitment.QMDBHolderName(s.AdoptBy)}
	if commitment.ImportSetColdLeanOn() {
		parks, adopts := commitment.ColdLeanCounts()
		checks, misses := commitment.ColdLeanCheckCounts()
		f = append(f, "coldLeanParks", parks, "coldLeanAdopts", adopts, "coldLeanChecks", checks, "coldLeanMisses", misses)
	}
	return f
}
