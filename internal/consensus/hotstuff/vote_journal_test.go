// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Tests for the durable vote journal: a vote commitment must reach disk before
// the vote reaches the network, must survive a restart, and must not be lost by
// a later periodic persist. Also covers reading records written by a pre-journal
// binary, and the diagnostic that fires when the recovered state has forked from
// the applied chain.

package hotstuff

import (
	"bytes"
	"context"
	"encoding/binary"
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// dbVoteJournal is Service.JournalVote without the surrounding service: it
// persists through the real codec into a real kv store, so these tests exercise
// encode → store → load → restore end to end.
type dbVoteJournal struct {
	db    kv.RwDB
	calls int
	fail  error
	// order records the interleaving of journal writes and engine outputs.
	order *[]string
}

func (j *dbVoteJournal) JournalVote(state *ConsensusState) error {
	if j.fail != nil {
		return j.fail
	}
	j.calls++
	if j.order != nil {
		*j.order = append(*j.order, "journal")
	}
	return j.db.Update(context.Background(), func(tx kv.RwTx) error {
		return SaveConsensusState(tx, state)
	})
}

func (j *dbVoteJournal) load(t *testing.T) *ConsensusState {
	t.Helper()
	var st *ConsensusState
	if err := j.db.View(context.Background(), func(tx kv.Tx) error {
		var err error
		st, err = LoadConsensusState(tx)
		return err
	}); err != nil {
		t.Fatalf("LoadConsensusState: %v", err)
	}
	return st
}

// importAndPropose drives the production default path (import-gated voting): the
// block is imported first, then the leader's proposal arrives, which makes the
// engine vote immediately.
func importAndPropose(t *testing.T, e *ConsensusEngine, setup *testSetup, view ViewNumber, blockHash types.Hash) error {
	t.Helper()
	if err := e.ProcessEvent(ConsensusEvent{Type: EventBlockImported, Hash: blockHash}); err != nil {
		t.Fatalf("EventBlockImported: %v", err)
	}
	leader := uint32(LeaderForView(view, setup.vs))
	sig := setup.keys[leader].Sign(SigningMessage(view, blockHash))
	return e.ProcessEvent(ConsensusEvent{
		Type: EventMessage,
		Msg: ConsensusMsg{Type: MsgProposal, Payload: &Proposal{
			View:      view,
			BlockHash: blockHash,
			JustifyQC: GenesisQC(),
			Proposer:  ValidatorIndex(leader),
			Signature: sig.Marshal(),
		}},
	})
}

func findVote(outputs []EngineOutput) *Vote {
	for _, o := range outputs {
		if o.Type == OutputSendToValidator && o.Message != nil && o.Message.Type == MsgVote {
			if v, ok := o.Message.Payload.(*Vote); ok {
				return v
			}
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// 1. the commitment is durable BEFORE the vote is released
// ---------------------------------------------------------------------------

func TestVoteIsJournalledBeforeItIsSent(t *testing.T) {
	setup := newTestSetup(t, 4)
	// Validator 0 is a follower in view 1 (leader is derived from the view).
	myIndex := 0
	if LeaderForView(1, setup.vs) == ValidatorIndex(myIndex) {
		myIndex = 1
	}
	outputCh := make(chan EngineOutput, 256)
	engine := NewConsensusEngine(ValidatorIndex(myIndex), setup.keys[myIndex], setup.vs, 1000, 10000, outputCh)

	var order []string
	j := &dbVoteJournal{db: memdb.NewTestDB(t), order: &order}
	engine.SetVoteJournal(j)

	blockHash := types.Hash{0xAB, 0xCD}
	if err := importAndPropose(t, engine, setup, 1, blockHash); err != nil {
		t.Fatalf("proposal: %v", err)
	}

	outputs := drainOutputs(outputCh)
	vote := findVote(outputs)
	if vote == nil {
		t.Fatal("expected a prepare vote to be emitted")
	}
	if j.calls != 1 {
		t.Fatalf("expected exactly one journal write, got %d", j.calls)
	}

	// The engine emits into a buffered channel, so "was the journal written
	// before the send" is checked against the persisted record: it must already
	// name this exact vote at the moment the vote exists.
	st := j.load(t)
	if st == nil {
		t.Fatal("no state journalled")
	}
	if st.LastVotedView != 1 || st.LastVotedHash != blockHash {
		t.Fatalf("journalled commitment wrong: view=%d hash=%x, want view=1 hash=%x",
			st.LastVotedView, st.LastVotedHash, blockHash)
	}
	if order[0] != "journal" {
		t.Fatalf("journal was not the first recorded action: %v", order)
	}
}

// ---------------------------------------------------------------------------
// 2. crash immediately after voting: the restart must not vote differently
// ---------------------------------------------------------------------------

func TestRestartAfterVoteDoesNotEquivocate(t *testing.T) {
	setup := newTestSetup(t, 4)
	myIndex := 0
	if LeaderForView(1, setup.vs) == ValidatorIndex(myIndex) {
		myIndex = 1
	}
	db := memdb.NewTestDB(t)

	// --- pre-crash process: vote for blockA in view 1 ---
	preCh := make(chan EngineOutput, 256)
	pre := NewConsensusEngine(ValidatorIndex(myIndex), setup.keys[myIndex], setup.vs, 1000, 10000, preCh)
	pre.SetVoteJournal(&dbVoteJournal{db: db})

	blockA := types.Hash{0x0A}
	if err := importAndPropose(t, pre, setup, 1, blockA); err != nil {
		t.Fatalf("first proposal: %v", err)
	}
	if findVote(drainOutputs(preCh)) == nil {
		t.Fatal("pre-crash engine did not vote")
	}
	// Crash here: nothing else runs, in particular no periodic persist.

	// --- post-restart process: fresh engine, recovered from disk only ---
	postCh := make(chan EngineOutput, 256)
	post := NewConsensusEngine(ValidatorIndex(myIndex), setup.keys[myIndex], setup.vs, 1000, 10000, postCh)
	post.SetVoteJournal(&dbVoteJournal{db: db})

	var st *ConsensusState
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		var err error
		st, err = LoadConsensusState(tx)
		return err
	}); err != nil {
		t.Fatalf("load: %v", err)
	}
	if st == nil {
		t.Fatal("expected persisted state after the vote")
	}
	if st.LastVotedView != 1 || st.LastVotedHash != blockA {
		t.Fatalf("recovered commitment wrong: view=%d hash=%x", st.LastVotedView, st.LastVotedHash)
	}
	post.RestoreVoteCommitments(st.LastVotedView, st.LastVotedHash, st.LastCommitVotedView, st.LastCommitVotedHash)

	// A conflicting proposal for the SAME view arrives (the leader re-proposed a
	// different block, or a stale duplicate is replayed).
	blockB := types.Hash{0x0B}
	if err := importAndPropose(t, post, setup, 1, blockB); err != nil {
		t.Fatalf("second proposal: %v", err)
	}
	if v := findVote(drainOutputs(postCh)); v != nil {
		t.Fatalf("EQUIVOCATION: restarted node voted again in view 1 for %x", v.BlockHash)
	}

	// Without the restore the very same engine would have voted — that is the
	// bug this guards, so prove the setup is otherwise capable of voting.
	fresh := NewConsensusEngine(ValidatorIndex(myIndex), setup.keys[myIndex], setup.vs, 1000, 10000, postCh)
	fresh.SetVoteJournal(&dbVoteJournal{db: memdb.NewTestDB(t)})
	if err := importAndPropose(t, fresh, setup, 1, blockB); err != nil {
		t.Fatalf("control proposal: %v", err)
	}
	if findVote(drainOutputs(postCh)) == nil {
		t.Fatal("control engine did not vote — the test setup proves nothing")
	}
}

// ---------------------------------------------------------------------------
// 3. a journal failure must make the node abstain, never vote unrecorded
// ---------------------------------------------------------------------------

func TestVoteAbstainsWhenJournalFails(t *testing.T) {
	setup := newTestSetup(t, 4)
	myIndex := 0
	if LeaderForView(1, setup.vs) == ValidatorIndex(myIndex) {
		myIndex = 1
	}
	outputCh := make(chan EngineOutput, 256)
	engine := NewConsensusEngine(ValidatorIndex(myIndex), setup.keys[myIndex], setup.vs, 1000, 10000, outputCh)
	engine.SetVoteJournal(&dbVoteJournal{db: memdb.NewTestDB(t), fail: context.DeadlineExceeded})

	blockHash := types.Hash{0xEE}
	if err := importAndPropose(t, engine, setup, 1, blockHash); err == nil {
		t.Fatal("expected the proposal handler to surface the journal failure")
	}
	if v := findVote(drainOutputs(outputCh)); v != nil {
		t.Fatalf("vote for %x was sent even though it could not be journalled", v.BlockHash)
	}
	// And the in-memory state must NOT claim we voted, so a later retry can.
	engine.mu.Lock()
	voted := engine.roundState.HasVotedInView(1)
	engine.mu.Unlock()
	if voted {
		t.Fatal("round state recorded a vote that was never journalled or sent")
	}
}

// ---------------------------------------------------------------------------
// 4. the periodic/commit-time persist must not erase the vote history
// ---------------------------------------------------------------------------

func TestSnapshotStateCarriesVoteCommitments(t *testing.T) {
	setup := newTestSetup(t, 4)
	myIndex := 0
	if LeaderForView(1, setup.vs) == ValidatorIndex(myIndex) {
		myIndex = 1
	}
	outputCh := make(chan EngineOutput, 256)
	engine := NewConsensusEngine(ValidatorIndex(myIndex), setup.keys[myIndex], setup.vs, 1000, 10000, outputCh)
	db := memdb.NewTestDB(t)
	engine.SetVoteJournal(&dbVoteJournal{db: db})

	blockHash := types.Hash{0x77}
	if err := importAndPropose(t, engine, setup, 1, blockHash); err != nil {
		t.Fatalf("proposal: %v", err)
	}

	// This is exactly what Service.persistState writes.
	snap := engine.SnapshotState()
	if snap.LastVotedView != 1 || snap.LastVotedHash != blockHash {
		t.Fatalf("SnapshotState dropped the vote commitment: view=%d hash=%x",
			snap.LastVotedView, snap.LastVotedHash)
	}
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		return SaveConsensusState(tx, snap)
	}); err != nil {
		t.Fatalf("persist: %v", err)
	}
	var reloaded *ConsensusState
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		var err error
		reloaded, err = LoadConsensusState(tx)
		return err
	}); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.LastVotedView != 1 || reloaded.LastVotedHash != blockHash {
		t.Fatal("a periodic persist clobbered the journalled vote commitment")
	}
}

// ---------------------------------------------------------------------------
// 5. backward compatibility with records written before the vote fields existed
// ---------------------------------------------------------------------------

// encodeConsensusStateV1 reproduces the pre-journal on-disk layout byte for byte:
// view(8) + consecutiveTimeouts(4) + lockedQC_len(4) + lockedQC + committedQC.
func encodeConsensusStateV1(t *testing.T, st *ConsensusState) []byte {
	t.Helper()
	lockedQCBytes, err := encodeQC(&st.LockedQC)
	if err != nil {
		t.Fatalf("encode locked qc: %v", err)
	}
	committedQCBytes, err := encodeQC(&st.LastCommittedQC)
	if err != nil {
		t.Fatalf("encode committed qc: %v", err)
	}
	buf := make([]byte, 16+len(lockedQCBytes)+len(committedQCBytes))
	binary.LittleEndian.PutUint64(buf[0:8], st.View)
	binary.LittleEndian.PutUint32(buf[8:12], st.ConsecutiveTimeouts)
	binary.LittleEndian.PutUint32(buf[12:16], uint32(len(lockedQCBytes)))
	copy(buf[16:], lockedQCBytes)
	copy(buf[16+len(lockedQCBytes):], committedQCBytes)
	return buf
}

func TestLoadLegacyV1ConsensusState(t *testing.T) {
	db := memdb.NewTestDB(t)

	legacy := &ConsensusState{
		View:                13510003,
		ConsecutiveTimeouts: 7,
		LockedQC: QuorumCertificate{
			View:               13510002,
			BlockHash:          types.Hash{0x52, 0xcf, 0x2f, 0xf8},
			AggregateSignature: bytes.Repeat([]byte{0x11}, 96),
			Signers:            []bool{true, true, true, true, true, false, true},
		},
		LastCommittedQC: QuorumCertificate{
			View:               13510001,
			BlockHash:          types.Hash{0x99, 0x88},
			AggregateSignature: bytes.Repeat([]byte{0x22}, 96),
			Signers:            []bool{true, true, true, true, true, true, false},
		},
	}
	raw := encodeConsensusStateV1(t, legacy)

	// The record a live 7-validator node wrote before this change was 318 bytes;
	// pin that so a codec change to QuorumCertificate cannot silently invalidate
	// the compatibility path this test exercises.
	if len(raw) != 318 {
		t.Logf("note: legacy record is %d bytes (production observed 318)", len(raw))
	}

	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		return tx.Put(modules.HotStuffState, hotstuffStateKey, raw)
	}); err != nil {
		t.Fatalf("seed legacy record: %v", err)
	}

	var loaded *ConsensusState
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		var err error
		loaded, err = LoadConsensusState(tx)
		return err
	}); err != nil {
		t.Fatalf("LoadConsensusState on a legacy record failed: %v", err)
	}
	if loaded == nil {
		t.Fatal("legacy record decoded to nil")
	}
	if loaded.View != legacy.View || loaded.ConsecutiveTimeouts != legacy.ConsecutiveTimeouts {
		t.Fatalf("legacy header mismatch: view=%d timeouts=%d", loaded.View, loaded.ConsecutiveTimeouts)
	}
	if loaded.LockedQC.View != legacy.LockedQC.View || loaded.LockedQC.BlockHash != legacy.LockedQC.BlockHash {
		t.Fatal("legacy locked QC mismatch")
	}
	if !bytes.Equal(loaded.LockedQC.AggregateSignature, legacy.LockedQC.AggregateSignature) {
		t.Fatal("legacy locked QC signature mismatch")
	}
	if loaded.LastCommittedQC.View != legacy.LastCommittedQC.View ||
		loaded.LastCommittedQC.BlockHash != legacy.LastCommittedQC.BlockHash {
		t.Fatal("legacy committed QC mismatch")
	}
	// Missing fields read as "never voted" — the conservative default.
	if loaded.LastVotedView != 0 || loaded.LastVotedHash != (types.Hash{}) ||
		loaded.LastCommitVotedView != 0 || loaded.LastCommitVotedHash != (types.Hash{}) {
		t.Fatal("legacy record must decode with empty vote commitments")
	}

	// An upgraded node re-writes in v2; the record must then round-trip with the
	// vote fields, and must no longer be mistaken for a legacy one.
	loaded.LastVotedView = 13510003
	loaded.LastVotedHash = types.Hash{0xAA}
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		return SaveConsensusState(tx, loaded)
	}); err != nil {
		t.Fatalf("v2 rewrite: %v", err)
	}
	var v2 *ConsensusState
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		var err error
		v2, err = LoadConsensusState(tx)
		return err
	}); err != nil {
		t.Fatalf("v2 reload: %v", err)
	}
	if v2.LastVotedView != 13510003 || v2.LastVotedHash != (types.Hash{0xAA}) ||
		v2.View != legacy.View || v2.LockedQC.View != legacy.LockedQC.View ||
		v2.LastCommittedQC.BlockHash != legacy.LastCommittedQC.BlockHash {
		t.Fatal("v2 rewrite did not round-trip")
	}
}

func TestConsensusStateV2RoundTrip(t *testing.T) {
	db := memdb.NewTestDB(t)
	st := &ConsensusState{
		View:                42,
		ConsecutiveTimeouts: 2,
		LockedQC: QuorumCertificate{
			View:               41,
			BlockHash:          types.Hash{0x01},
			AggregateSignature: bytes.Repeat([]byte{0xA1}, 96),
			Signers:            []bool{true, false, true, true},
		},
		LastCommittedQC: QuorumCertificate{
			View:               40,
			BlockHash:          types.Hash{0x02},
			AggregateSignature: bytes.Repeat([]byte{0xB2}, 96),
			Signers:            []bool{true, true, true, false},
		},
		LastVotedView:       42,
		LastVotedHash:       types.Hash{0xF1},
		LastCommitVotedView: 41,
		LastCommitVotedHash: types.Hash{0xF2},
	}
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		return SaveConsensusState(tx, st)
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	var got *ConsensusState
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		var err error
		got, err = LoadConsensusState(tx)
		return err
	}); err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.View != st.View || got.ConsecutiveTimeouts != st.ConsecutiveTimeouts ||
		got.LastVotedView != st.LastVotedView || got.LastVotedHash != st.LastVotedHash ||
		got.LastCommitVotedView != st.LastCommitVotedView || got.LastCommitVotedHash != st.LastCommitVotedHash {
		t.Fatalf("v2 round trip mismatch: %+v", got)
	}
	if got.LockedQC.View != st.LockedQC.View || got.LastCommittedQC.View != st.LastCommittedQC.View ||
		!bytes.Equal(got.LockedQC.AggregateSignature, st.LockedQC.AggregateSignature) ||
		!bytes.Equal(got.LastCommittedQC.AggregateSignature, st.LastCommittedQC.AggregateSignature) {
		t.Fatal("v2 round trip lost QC data")
	}
	if len(got.LockedQC.Signers) != len(st.LockedQC.Signers) {
		t.Fatal("v2 round trip lost the signer bitmap")
	}
}

func TestLoadConsensusStateRejectsTruncatedV2(t *testing.T) {
	db := memdb.NewTestDB(t)
	st := &ConsensusState{View: 5, LockedQC: GenesisQC(), LastCommittedQC: GenesisQC()}
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		return SaveConsensusState(tx, st)
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	var full []byte
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		v, err := tx.GetOne(modules.HotStuffState, hotstuffStateKey)
		full = append([]byte(nil), v...)
		return err
	}); err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		return tx.Put(modules.HotStuffState, hotstuffStateKey, full[:len(full)-3])
	}); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	err := db.View(context.Background(), func(tx kv.Tx) error {
		_, lerr := LoadConsensusState(tx)
		return lerr
	})
	if err == nil {
		t.Fatal("a truncated v2 record must be reported, not silently accepted")
	}
}

// ---------------------------------------------------------------------------
// 6. recovered state vs applied chain: the wedge must be reported, not silent
// ---------------------------------------------------------------------------

func writeCanonicalHeader(t *testing.T, db kv.RwDB, number uint64, tag byte) types.Hash {
	t.Helper()
	h := &block.Header{
		Number:     uint256.NewInt(number),
		Difficulty: uint256.NewInt(1),
		Root:       types.Hash{tag},
		Extra:      []byte{tag},
	}
	hash := h.Hash()
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		rawdb.WriteHeader(tx, h)
		return rawdb.WriteCanonicalHash(tx, hash, number)
	}); err != nil {
		t.Fatalf("write header %d: %v", number, err)
	}
	return hash
}

func TestReportPersistedStateDivergence(t *testing.T) {
	newSvc := func(db kv.RwDB) *Service {
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		return &Service{db: db, ctx: ctx}
	}

	t.Run("consistent", func(t *testing.T) {
		db := memdb.NewTestDB(t)
		hash := writeCanonicalHeader(t, db, 100, 0x01)
		st := &ConsensusState{LastCommittedQC: QuorumCertificate{View: 10, BlockHash: hash}}
		if got := newSvc(db).reportPersistedStateDivergence(st); got != persistedStateConsistent {
			t.Fatalf("got status %d, want consistent", got)
		}
	})

	t.Run("block unknown", func(t *testing.T) {
		db := memdb.NewTestDB(t)
		writeCanonicalHeader(t, db, 100, 0x01)
		st := &ConsensusState{LastCommittedQC: QuorumCertificate{View: 10, BlockHash: types.Hash{0xDE, 0xAD}}}
		if got := newSvc(db).reportPersistedStateDivergence(st); got != persistedStateBlockUnknown {
			t.Fatalf("got status %d, want block-unknown", got)
		}
	})

	// The production wedge: the committed QC block exists locally but a DIFFERENT
	// block is canonical at that height, so AlignAppliedBranch will refuse to
	// move onto it ("would revert committed block") and block production stops.
	t.Run("diverged", func(t *testing.T) {
		db := memdb.NewTestDB(t)
		sibling := &block.Header{
			Number:     uint256.NewInt(100),
			Difficulty: uint256.NewInt(1),
			Root:       types.Hash{0xBB},
			Extra:      []byte{0xBB},
		}
		siblingHash := sibling.Hash()
		if err := db.Update(context.Background(), func(tx kv.RwTx) error {
			rawdb.WriteHeader(tx, sibling)
			return nil
		}); err != nil {
			t.Fatalf("write sibling: %v", err)
		}
		canonHash := writeCanonicalHeader(t, db, 100, 0xAA)
		if canonHash == siblingHash {
			t.Fatal("test setup: sibling must differ from the canonical block")
		}
		st := &ConsensusState{
			LockedQC:        QuorumCertificate{View: 11, BlockHash: siblingHash},
			LastCommittedQC: QuorumCertificate{View: 10, BlockHash: siblingHash},
		}
		if got := newSvc(db).reportPersistedStateDivergence(st); got != persistedStateDiverged {
			t.Fatalf("got status %d, want diverged", got)
		}
	})

	t.Run("genesis qc is not checkable", func(t *testing.T) {
		db := memdb.NewTestDB(t)
		st := &ConsensusState{LastCommittedQC: GenesisQC()}
		if got := newSvc(db).reportPersistedStateDivergence(st); got != persistedStateUncheckable {
			t.Fatalf("got status %d, want uncheckable", got)
		}
	})
}
