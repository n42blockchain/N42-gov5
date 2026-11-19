// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import "github.com/n42blockchain/N42/modules/state/commitment"

// setColdLogFields returns the import's SetCold split for the "blockimport
// phases" line (r97a stamps): setColdWaitUs is the wait for the QMDB tree's
// lock (the previous block's row capture holds it from the hand-off),
// setColdWorkUs the install, setColdHolder the writer that held the lock
// when the wait began ("none" under lookupWaitNote).
func (bc *BlockChain) setColdLogFields() []any {
	if !bc.qmdbEnabled || bc.qmdbRootComputer == nil {
		return nil
	}
	s := bc.qmdbRootComputer.LastSetCold()
	return []any{"setColdWaitUs", s.Wait.Microseconds(), "setColdWorkUs", s.Work.Microseconds(),
		"setColdHolder", commitment.QMDBHolderName(s.Holder)}
}
