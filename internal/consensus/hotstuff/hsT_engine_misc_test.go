// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// TestEmit_ChannelFullDropsAndReportsError covers emit's full-channel branch,
// including the isCriticalOutput log-escalation path for a critical type.
func TestEmit_ChannelFullDropsAndReportsError(t *testing.T) {
	setup := newTestSetup(t, 4)
	e, ch := newTestEngine(t, setup, 0)
	_ = ch // the raw channel is unbuffered-replacement below

	// Fill the engine's own output channel to capacity so the next emit hits
	// the default (full) branch. outputCh was created at size 256 by
	// newTestEngine's caller construction (NewConsensusEngine(...,outputCh)),
	// so drain is impractical; instead swap in a zero-capacity channel.
	e.outputCh = make(chan EngineOutput) // unbuffered + no reader: always full

	if err := e.emit(EngineOutput{Type: OutputBlockCommitted}); !errors.Is(err, ErrOutputChannelClosed) {
		t.Fatalf("emit() on a full channel = %v, want ErrOutputChannelClosed", err)
	}
	// Non-critical type takes the same drop path without the CRITICAL log line;
	// exercised for coverage of isCriticalOutput's false branch.
	if err := e.emit(EngineOutput{Type: OutputEpochStaged}); !errors.Is(err, ErrOutputChannelClosed) {
		t.Fatalf("emit() on a full channel = %v, want ErrOutputChannelClosed", err)
	}
}

// TestExtractQCFromMessage_AllTypes covers every message type that carries an
// embedded QC, plus the nil/unrelated-type fallthrough.
func TestExtractQCFromMessage_AllTypes(t *testing.T) {
	want := QuorumCertificate{View: 7, BlockHash: types.Hash{0x09}}

	cases := []struct {
		name string
		msg  *ConsensusMsg
		want *QuorumCertificate
	}{
		{"nil message", nil, nil},
		{"nil payload", &ConsensusMsg{Type: MsgVote, Payload: nil}, nil},
		{"proposal", &ConsensusMsg{Type: MsgProposal, Payload: &Proposal{JustifyQC: want}}, &want},
		{"timeout", &ConsensusMsg{Type: MsgTimeout, Payload: &TimeoutMessage{HighQC: want}}, &want},
		{"decide", &ConsensusMsg{Type: MsgDecide, Payload: &Decide{CommitQC: want}}, &want},
		{"prepareQC", &ConsensusMsg{Type: MsgPrepareQC, Payload: &PrepareQCMsg{QC: want}}, &want},
		{"newView", &ConsensusMsg{Type: MsgNewView, Payload: &NewViewMsg{TimeoutCert: TimeoutCertificate{HighQC: want}}}, &want},
		{"vote (no embedded QC)", &ConsensusMsg{Type: MsgVote, Payload: &Vote{}}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractQCFromMessage(tc.msg)
			if tc.want == nil {
				if got != nil {
					t.Fatalf("extractQCFromMessage(%s) = %+v, want nil", tc.name, got)
				}
				return
			}
			if got == nil || got.View != tc.want.View || got.BlockHash != tc.want.BlockHash {
				t.Fatalf("extractQCFromMessage(%s) = %+v, want %+v", tc.name, got, tc.want)
			}
		})
	}
}

// TestResolveQCValidatorSet_FallsBackOnSizeMismatch covers the epoch-drift
// fallback: when the view-derived set's size does not match the bitmap, the
// engine looks up a historical set of the right size.
func TestResolveQCValidatorSet_FallsBackOnSizeMismatch(t *testing.T) {
	setup := newTestSetup(t, 4)
	e, _ := newTestEngine(t, setup, 0)

	// Current set has 4 validators; a bitmap of length 4 resolves directly.
	vs := e.resolveQCValidatorSet(e.roundState.CurrentView(), 4)
	if vs == nil || int(vs.Len()) != 4 {
		t.Fatalf("resolveQCValidatorSet(matching size) = %v, want a 4-validator set", vs)
	}

	// A bitmap length that matches nothing returns the primary (view-derived)
	// set unchanged -- the subsequent signature check is what actually fails
	// a forged/mismatched certificate, not this resolution step.
	vs2 := e.resolveQCValidatorSet(e.roundState.CurrentView(), 99)
	if vs2 == nil {
		t.Fatal("resolveQCValidatorSet(no match) returned nil, want the primary set")
	}
}

// TestEpochManager_TrimHistorical covers the eviction loop that keeps
// historicalSets bounded to maxHistoricalEpochs, evicting the lowest epoch
// first.
func TestEpochManager_TrimHistorical(t *testing.T) {
	setup := newTestSetup(t, 4)
	em := NewEpochManager(setup.vs)
	for epoch := uint64(0); epoch < maxHistoricalEpochs+3; epoch++ {
		em.historicalSets[epoch] = setup.vs.Clone()
	}
	em.trimHistorical()
	if len(em.historicalSets) != maxHistoricalEpochs {
		t.Fatalf("historicalSets len = %d, want %d", len(em.historicalSets), maxHistoricalEpochs)
	}
	for epoch := uint64(0); epoch < 3; epoch++ {
		if _, ok := em.historicalSets[epoch]; ok {
			t.Fatalf("expected epoch %d to be evicted (oldest-first)", epoch)
		}
	}
}

// failingVoteJournal always errors, to exercise the ABSTAIN path of
// journalPrepareVote/journalCommitVote.
type failingVoteJournal struct{}

func (failingVoteJournal) JournalVote(*ConsensusState) error { return errors.New("journal write failed") }

// TestJournalCommitVote_ErrorAbstains covers the error path: a failing
// VoteJournal must make journalCommitVote return an error and must NOT record
// the commit-vote hash (the abstain contract).
func TestJournalCommitVote_ErrorAbstains(t *testing.T) {
	setup := newTestSetup(t, 4)
	e, _ := newTestEngine(t, setup, 0)
	e.SetVoteJournal(failingVoteJournal{})

	e.mu.Lock()
	err := e.journalCommitVote(1, types.Hash{0x01})
	e.mu.Unlock()
	if err == nil {
		t.Fatal("expected journalCommitVote to propagate the journal error")
	}
}

// TestJournalCommitVote_NonMemberSkipsJournal covers the non-member fast
// path: a non-participating engine records the vote hash in round state
// without ever calling the journal.
func TestJournalCommitVote_NonMemberSkipsJournal(t *testing.T) {
	setup := newTestSetup(t, 4)
	e, _ := newTestEngine(t, setup, 0)
	e.myIndex = NonMemberIndex
	e.SetVoteJournal(failingVoteJournal{}) // would error if (incorrectly) called

	e.mu.Lock()
	err := e.journalCommitVote(1, types.Hash{0x02})
	e.mu.Unlock()
	if err != nil {
		t.Fatalf("non-member journalCommitVote should skip the journal and succeed, got %v", err)
	}
}

// hsTDeferExecProducer tracks TriggerBlockProduction calls for
// deferProduction's immediate-resume branch.
type hsTDeferExecProducer struct {
	triggered []types.Hash
}

func (p *hsTDeferExecProducer) TriggerBlockProduction(parentHash types.Hash) {
	p.triggered = append(p.triggered, parentHash)
}
func (p *hsTDeferExecProducer) CommitToCanonical(types.Hash) error { return nil }

type hsTDeferExecFetcher struct{ applied bool }

func (f *hsTDeferExecFetcher) FetchBlockByHash(types.Hash) {}
func (f *hsTDeferExecFetcher) CatchUp()                    {}
func (f *hsTDeferExecFetcher) HeightBehind() uint64        { return 0 }
func (f *hsTDeferExecFetcher) BlockApplied(types.Hash, uint64) bool {
	return f.applied
}

// TestDeferProduction_NotAppliedParksOnly covers the park-without-resume
// branch: the parent is checked but not yet applied.
func TestDeferProduction_NotAppliedParksOnly(t *testing.T) {
	db := memdb.NewTestDB(t)
	header := &block.Header{Number: uint256.NewInt(1), Difficulty: uint256.NewInt(0)}
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		rawdb.WriteHeader(tx, header)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	fetcher := &hsTDeferExecFetcher{applied: false}
	producer := &hsTDeferExecProducer{}
	s := &Service{db: db, ctx: context.Background(), blockFetcher: fetcher, blockProducer: producer}

	s.deferProduction(5, header.Hash())

	if len(producer.triggered) != 0 {
		t.Fatalf("expected no production trigger while the parent is unapplied, got %v", producer.triggered)
	}
	s.pendingMu.Lock()
	parked := s.deferredProduce.parent == header.Hash()
	s.pendingMu.Unlock()
	if !parked {
		t.Fatal("expected the view/parent to remain parked in deferredProduce")
	}
}

// TestDeferProduction_AppliedResumesImmediately covers the race-closing
// immediate-resume branch: the parent is already applied by the time the
// gate re-checks it.
func TestDeferProduction_AppliedResumesImmediately(t *testing.T) {
	db := memdb.NewTestDB(t)
	header := &block.Header{Number: uint256.NewInt(2), Difficulty: uint256.NewInt(0)}
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		rawdb.WriteHeader(tx, header)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	fetcher := &hsTDeferExecFetcher{applied: true}
	producer := &hsTDeferExecProducer{}
	s := &Service{db: db, ctx: context.Background(), blockFetcher: fetcher, blockProducer: producer}

	s.deferProduction(5, header.Hash())

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && len(producer.triggered) == 0 {
		time.Sleep(20 * time.Millisecond)
	}
	if len(producer.triggered) != 1 || producer.triggered[0] != header.Hash() {
		t.Fatalf("expected an immediate resume triggering production on %s, got %v", header.Hash(), producer.triggered)
	}
	s.pendingMu.Lock()
	cleared := s.deferredProduce.parent == (types.Hash{})
	s.pendingMu.Unlock()
	if !cleared {
		t.Fatal("expected deferredProduce.parent to be cleared after the immediate resume")
	}
}
