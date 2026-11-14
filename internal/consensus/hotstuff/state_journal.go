// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"os"
	"strings"

	"github.com/n42blockchain/N42/lib/kv"
)

// hsStateJournalOn is N42_HS_STATE_JOURNAL (default off, r89a).
//
// The service persists its consensus-state snapshot (view, lock, last
// committed QC and the vote commitments) in the chain database: inside the
// canonical commit's transaction when that commit succeeds, otherwise in a
// write transaction of its own -- after every commit-to-canonical that was
// deferred (the committed block not executed here yet, which under vote on
// availability is most commits on the executor: 8 a second in E1N26 run 68
// B4) and every persistInterval views. Each of those took the chain
// database's single writer (held 2.7 ms, waited 11 ms) in front of the
// ordered block write.
//
// With the switch on and the separate vote journal in use
// (N42_VOTE_JOURNAL_SEPARATE, <datadir>/hotstuff-journal) those two
// steady-state persists go to the journal store instead. The journal
// already holds this record, written by every vote before the vote is
// released, and recovery merges it into the chain database's record with
// the monotonic rules of SaveConsensusState (mergeVoteJournal), field by
// field at the highest view: a snapshot written to either store restores
// the same state. Nothing a vote or a proposal waits for changes -- the
// vote journal write is untouched -- and the snapshot is durable when the
// call returns, as before. The canonical commit's in-transaction persist,
// the shutdown persist and the anchor stay on the chain database, so the
// chain record still advances at every successful commit and the reset
// tools (which delete it, discarding the journal) keep working. A snapshot
// carrying a staged validator set still goes to the chain database: the
// staged epoch rows live there and must commit with the state that refers
// to them.
var hsStateJournalOn = parseHSOptIn(os.Getenv("N42_HS_STATE_JOURNAL"))

func parseHSOptIn(v string) bool {
	switch strings.TrimSpace(v) {
	case "1", "true", "TRUE", "on", "ON", "yes":
		return true
	}
	return false
}

// SetHSStateJournalForTest switches N42_HS_STATE_JOURNAL and returns the
// restore.
func SetHSStateJournalForTest(on bool) func() {
	prev := hsStateJournalOn
	hsStateJournalOn = on
	return func() { hsStateJournalOn = prev }
}

// steadyStateDB is the store of a steady-state persist (persistState): the
// vote journal under N42_HS_STATE_JOURNAL when the snapshot carries no
// staged epoch, else the chain database.
func (s *Service) steadyStateDB(h *stateHook) kv.RwDB {
	if hsStateJournalOn && s.journalDB != nil && s.voteDB == nil && h != nil && !h.staged {
		return s.journalDB
	}
	return s.db
}
