// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"os"
	"strings"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules"
)

// Separate vote journal.
//
// Every vote (and the leader's Proposal, which is a vote commitment) is made
// durable by JournalVote before it is released, under the engine mutex. In
// the chain database that is a write transaction, and MDBX has one writer:
// the journal queued behind whatever block write held it. On the S99 fleet
// at 163k-tx blocks the lag writer holds it 90-107 ms per block, and a
// follower's prepare vote went out a median 20-31 ms (p90 87-126 ms) after
// the Proposal reached it, against ~4 ms at 22.5k-tx blocks; the leader's
// own Proposal waited behind a 221 ms write in the worst views.
//
// With the journal in a small store of its own (<datadir>/hotstuff-journal)
// the vote never waits for a block write. The chain database's record stays
// what it was (written at every commit and every persistInterval views); the
// journal holds the same record, kept at least as far forward, and recovery
// merges the two with the monotonic rules of SaveConsensusState.
//
// The journal is an extension of the chain database's record, never a
// replacement: when the chain database holds no record (a fresh datadir, or
// one cleared by cmd/hotstuff-reset or cmd/qs-hsreset) the journal is
// discarded, so the reset tools keep working without knowing it exists, and
// Start writes the chain record before any vote so a journalled vote always
// has one to extend.

// SeparateVoteJournalEnabled reports whether votes are journalled in their
// own store: N42_VOTE_JOURNAL_SEPARATE=1/0 forces it, unset follows vote on
// availability (the mode whose lag writer holds the chain writer longest).
func SeparateVoteJournalEnabled() bool {
	switch strings.TrimSpace(os.Getenv("N42_VOTE_JOURNAL_SEPARATE")) {
	case "1", "true", "on":
		return true
	case "0", "false", "off":
		return false
	}
	return block.VoteOnAvailability()
}

// VoteJournalTables is the table set of the separate journal store.
func VoteJournalTables(kv.TableCfg) kv.TableCfg {
	return kv.TableCfg{modules.HotStuffState: {}}
}

// SetVoteJournalDB routes JournalVote to db. Must be called before Start.
func (s *Service) SetVoteJournalDB(db kv.RwDB) {
	s.journalDB = db
}

// mergeVoteJournal folds the separate journal's record into the chain
// database's record (nil when the chain database holds none). With no chain
// record the journal is stale by definition (fresh or reset datadir) and is
// cleared.
func (s *Service) mergeVoteJournal(chain *ConsensusState) (*ConsensusState, error) {
	if s.journalDB == nil {
		return chain, nil
	}
	if chain == nil {
		return nil, s.journalDB.Update(s.ctx, func(tx kv.RwTx) error {
			if v, err := tx.GetOne(modules.HotStuffState, hotstuffStateKey); err != nil || v == nil {
				return err
			}
			log.Warn("hotstuff: discarding the vote journal, the chain database holds no consensus state (fresh or reset datadir)")
			return tx.Delete(modules.HotStuffState, hotstuffStateKey)
		})
	}
	var journal *ConsensusState
	if err := s.journalDB.View(s.ctx, func(tx kv.Tx) error {
		var err error
		journal, err = LoadConsensusState(tx)
		return err
	}); err != nil {
		return nil, err
	}
	if journal == nil {
		return chain, nil
	}
	merged, err := mergeConsensusStates(chain, journal)
	if err != nil {
		return nil, err
	}
	log.Info("hotstuff: merged the vote journal into the persisted state",
		"chainVoted", chain.LastVotedView, "journalVoted", journal.LastVotedView,
		"chainCommitVoted", chain.LastCommitVotedView, "journalCommitVoted", journal.LastCommitVotedView)
	return merged, nil
}

// anchorVoteJournal writes the chain database's consensus record when a
// separate journal is in use and the chain database has none yet, so every
// journalled vote extends a chain record (mergeVoteJournal discards a journal
// without one).
func (s *Service) anchorVoteJournal() error {
	if s.journalDB == nil || s.db == nil {
		return nil
	}
	var have bool
	if err := s.db.View(s.ctx, func(tx kv.Tx) error {
		v, err := tx.GetOne(modules.HotStuffState, hotstuffStateKey)
		have = v != nil
		return err
	}); err != nil {
		return err
	}
	if have {
		return nil
	}
	ce := s.engine.Engine()
	if ce == nil {
		return nil
	}
	st := ce.SnapshotState()
	return s.db.Update(s.ctx, func(tx kv.RwTx) error {
		return SaveConsensusState(tx, st)
	})
}
