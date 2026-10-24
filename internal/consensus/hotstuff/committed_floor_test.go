// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"context"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// Round S131 (7 nodes, deferred depth 2, vote on availability): f343
// (h=14131247) committed on nodes 4-6 only; every datadir then had its
// HotStuff state deleted by hotstuff-reset. After the restart node 0, whose
// committed head was 14131246, proposed sibling 88e236 at 14131247 in view
// 2 with the genesis JustifyQC, and nodes 4-6 header-voted for it: the
// extends rule failed open on the empty justify and nothing compared the
// proposal with the chain they had committed. These tests replay that node
// 4-6 shape: committed f343, empty consensus state.

type fakeFloorHeader struct {
	number uint64
	parent types.Hash
}

type fakeCommittedChain struct {
	headNum   uint64
	head      types.Hash
	headers   map[types.Hash]fakeFloorHeader
	canonical map[uint64]types.Hash
}

func (c *fakeCommittedChain) CommittedHead() (uint64, types.Hash, bool, error) {
	return c.headNum, c.head, c.head != (types.Hash{}), nil
}

func (c *fakeCommittedChain) HeaderInfo(h types.Hash) (uint64, types.Hash, bool) {
	hdr, ok := c.headers[h]
	return hdr.number, hdr.parent, ok
}

func (c *fakeCommittedChain) CanonicalHash(n uint64) (types.Hash, bool) {
	h, ok := c.canonical[n]
	return h, ok
}

var (
	s131Grand     = types.Hash{0x14, 0x45} // 14131245, canonical
	s131Parent    = types.Hash{0x14, 0x46} // 14131246, canonical (nodes 0-3 committed head)
	s131Committed = types.Hash{0xf3, 0x43} // f343, 14131247, committed on nodes 4-6
	s131Sibling   = types.Hash{0x88, 0xe2} // 88e236, sibling of f343 proposed after the reset
	s131Child     = types.Hash{0xc1}       // a proper child of f343
)

// s131Chain is node 4-6's committed chain at the restart.
func s131Chain() *fakeCommittedChain {
	return &fakeCommittedChain{
		headNum: 14131247,
		head:    s131Committed,
		headers: map[types.Hash]fakeFloorHeader{
			s131Grand:     {14131245, types.Hash{0x14, 0x44}},
			s131Parent:    {14131246, s131Grand},
			s131Committed: {14131247, s131Parent},
		},
		canonical: map[uint64]types.Hash{
			14131245: s131Grand,
			14131246: s131Parent,
			14131247: s131Committed,
		},
	}
}

// s131Follower is a reset node 4-6: fresh engine (view 1, genesis lock),
// two-phase voting on availability, committed chain at f343.
func s131Follower(t *testing.T) (*ConsensusEngine, chan EngineOutput, *testSetup) {
	t.Helper()
	follower, outputCh, setup := twoPhaseFollower(t)
	follower.SetVoteOnAvailability(true)
	follower.SetCommittedChain(s131Chain())
	return follower, outputCh, setup
}

func s131Propose(t *testing.T, e *ConsensusEngine, setup *testSetup, blockHash types.Hash) {
	t.Helper()
	view := e.CurrentView()
	if err := e.ProcessEvent(ConsensusEvent{Type: EventMessage,
		Msg: ConsensusMsg{Type: MsgProposal, Payload: signedProposal(setup, view, blockHash, GenesisQC())}}); err != nil {
		t.Fatalf("proposal: %v", err)
	}
}

func TestCommittedFloorRefusesSiblingOfCommittedAfterReset(t *testing.T) {
	follower, outputCh, setup := s131Follower(t)
	before := metricVoteCommittedFloor.Get()

	s131Propose(t, follower, setup, s131Sibling)
	// Every vote trigger the vote-on-availability path has: header known,
	// availability checked, imported.
	for _, ev := range []ConsensusEvent{
		{Type: EventBlockHeaderKnown, Hash: s131Sibling, ParentHash: s131Parent, Number: 14131247},
		{Type: EventBlockChecked, Hash: s131Sibling, ParentHash: s131Parent},
		{Type: EventBlockImported, Hash: s131Sibling, ParentHash: s131Parent},
	} {
		if err := follower.ProcessEvent(ev); err != nil {
			t.Fatalf("event %d: %v", ev.Type, err)
		}
		if votes := countOutputs(drainOutputs(outputCh), OutputSendToValidator, MsgVote); votes != 0 {
			t.Fatalf("event %d: voted for a sibling of the committed block: %d votes", ev.Type, votes)
		}
	}
	if got := metricVoteCommittedFloor.Get(); got <= before {
		t.Fatalf("floor refusal not counted: metric %d -> %d", before, got)
	}
	if _, voted := follower.roundState.VotedHashInView(follower.CurrentView()); voted {
		t.Fatal("a vote commitment was recorded for the sibling")
	}
}

func TestCommittedFloorRefusesLocalCommitOfSibling(t *testing.T) {
	follower, outputCh, setup := s131Follower(t)
	view := follower.CurrentView()
	qc := hsTBuildCommitQC(t, setup, view, s131Sibling)
	follower.importedParents[s131Sibling] = s131Parent

	if err := follower.processDecide(&Decide{View: view, BlockHash: s131Sibling, CommitQC: *qc}); err != nil {
		t.Fatalf("processDecide: %v", err)
	}
	if n := countOutputs(drainOutputs(outputCh), OutputBlockCommitted, 0); n != 0 {
		t.Fatalf("committed a sibling of the committed block locally: %d commits", n)
	}
	if got := follower.roundState.LastCommittedQC().BlockHash; got == s131Sibling {
		t.Fatal("LastCommittedQC moved to the sibling")
	}
	if follower.CurrentView() != view+1 {
		t.Fatalf("view did not follow the refused Decide: %d", follower.CurrentView())
	}
}

func TestCommittedFloorVotesForChildOfCommitted(t *testing.T) {
	follower, outputCh, setup := s131Follower(t)

	s131Propose(t, follower, setup, s131Child)
	if err := follower.ProcessEvent(ConsensusEvent{Type: EventBlockHeaderKnown,
		Hash: s131Child, ParentHash: s131Committed, Number: 14131248}); err != nil {
		t.Fatal(err)
	}
	if votes := countOutputs(drainOutputs(outputCh), OutputSendToValidator, MsgVote); votes != 1 {
		t.Fatalf("expected one vote for a child of the committed block, got %d", votes)
	}
}

func TestCommittedFloorWaitsForUnknownAncestry(t *testing.T) {
	follower, outputCh, setup := s131Follower(t)
	middle := types.Hash{0xd0} // 14131248, header not seen yet
	tip := types.Hash{0xd1}    // 14131249, the proposal

	s131Propose(t, follower, setup, tip)
	if err := follower.ProcessEvent(ConsensusEvent{Type: EventBlockHeaderKnown,
		Hash: tip, ParentHash: middle, Number: 14131249}); err != nil {
		t.Fatal(err)
	}
	if votes := countOutputs(drainOutputs(outputCh), OutputSendToValidator, MsgVote); votes != 0 {
		t.Fatalf("voted with an unresolved ancestry: %d votes", votes)
	}
	// The missing ancestor's header arrives: the deferred vote is cast.
	if err := follower.ProcessEvent(ConsensusEvent{Type: EventBlockHeaderKnown,
		Hash: middle, ParentHash: s131Committed, Number: 14131248}); err != nil {
		t.Fatal(err)
	}
	if votes := countOutputs(drainOutputs(outputCh), OutputSendToValidator, MsgVote); votes != 1 {
		t.Fatalf("expected the vote once the ancestry resolved, got %d", votes)
	}
}

func TestCheckCommittedFloorShapes(t *testing.T) {
	chain := s131Chain()
	// A stored descendant chain two blocks above the committed head.
	d1, d2 := types.Hash{0xe1}, types.Hash{0xe2}
	chain.headers[d1] = fakeFloorHeader{14131248, s131Committed}
	chain.headers[d2] = fakeFloorHeader{14131249, d1}
	// A stored branch off 14131246 two blocks deep.
	x1, x2 := types.Hash{0xa1}, types.Hash{0xa2}
	chain.headers[x1] = fakeFloorHeader{14131247, s131Parent}
	chain.headers[x2] = fakeFloorHeader{14131248, x1}

	cases := []struct {
		name    string
		block   types.Hash
		parents map[types.Hash]types.Hash
		want    floorVerdict
	}{
		{"committed block itself", s131Committed, nil, floorAllow},
		{"canonical ancestor (late Decide)", s131Parent, nil, floorAllow},
		{"canonical ancestor via parent maps", s131Parent, map[types.Hash]types.Hash{s131Parent: s131Grand}, floorAllow},
		{"child via parent map", s131Child, map[types.Hash]types.Hash{s131Child: s131Committed}, floorAllow},
		{"stored grandchild", d2, nil, floorAllow},
		{"unstored block above stored grandchild", types.Hash{0xe3}, map[types.Hash]types.Hash{{0xe3}: d2}, floorAllow},
		{"sibling via parent map", s131Sibling, map[types.Hash]types.Hash{s131Sibling: s131Parent}, floorRefuse},
		{"stored sibling", x1, nil, floorRefuse},
		{"stored child of sibling", x2, nil, floorRefuse},
		{"unstored child of sibling", types.Hash{0xa3}, map[types.Hash]types.Hash{{0xa3}: x2}, floorRefuse},
		{"sibling chain wholly in parent maps", types.Hash{0xb2}, map[types.Hash]types.Hash{
			{0xb2}: {0xb1}, {0xb1}: s131Parent, s131Parent: s131Grand}, floorRefuse},
		{"unknown block", types.Hash{0x99}, nil, floorWait},
		{"unknown parent", types.Hash{0x98}, map[types.Hash]types.Hash{{0x98}: {0x97}}, floorWait},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setup := newTestSetup(t, 4)
			e, _ := newTestEngine(t, setup, 0)
			e.SetCommittedChain(chain)
			for h, p := range tc.parents {
				e.importedParents[h] = p
			}
			if got := e.checkCommittedFloor(tc.block); got != tc.want {
				t.Fatalf("checkCommittedFloor = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestCheckCommittedFloorDisabledOrNothingCommitted(t *testing.T) {
	setup := newTestSetup(t, 4)
	e, _ := newTestEngine(t, setup, 0)
	if got := e.checkCommittedFloor(s131Sibling); got != floorAllow {
		t.Fatalf("no reader: %s, want allow", got)
	}
	e.SetCommittedChain(&fakeCommittedChain{})
	if got := e.checkCommittedFloor(s131Sibling); got != floorAllow {
		t.Fatalf("nothing committed: %s, want allow", got)
	}
}

// TestDBCommittedChainReadsTheQCMarker pins the production reader to the
// QC-backed HotStuffCommittedHead marker, not HeadBlockHash.
func TestDBCommittedChainReadsTheQCMarker(t *testing.T) {
	db := memdb.NewTestDB(t)
	parent := &block.Header{Number: uint256.NewInt(10), Difficulty: uint256.NewInt(0)}
	committed := &block.Header{ParentHash: parent.Hash(), Number: uint256.NewInt(11), Difficulty: uint256.NewInt(0)}
	sibling := &block.Header{ParentHash: parent.Hash(), Number: uint256.NewInt(11), Difficulty: uint256.NewInt(1)}
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		for _, h := range []*block.Header{parent, committed, sibling} {
			rawdb.WriteHeader(tx, h)
		}
		if err := rawdb.WriteCanonicalHash(tx, parent.Hash(), 10); err != nil {
			return err
		}
		if err := rawdb.WriteCanonicalHash(tx, committed.Hash(), 11); err != nil {
			return err
		}
		rawdb.WriteHeadBlockHash(tx, sibling.Hash()) // a polluted legacy head must not matter
		return rawdb.WriteHotStuffCommittedHead(tx, committed.Hash())
	}); err != nil {
		t.Fatal(err)
	}
	c := NewDBCommittedChain(context.Background(), db)
	n, h, ok, err := c.CommittedHead()
	if err != nil || !ok || n != 11 || h != committed.Hash() {
		t.Fatalf("CommittedHead = %d %x %v %v", n, h, ok, err)
	}
	if n, p, ok := c.HeaderInfo(sibling.Hash()); !ok || n != 11 || p != parent.Hash() {
		t.Fatalf("HeaderInfo(sibling) = %d %x %v", n, p, ok)
	}
	if ch, ok := c.CanonicalHash(10); !ok || ch != parent.Hash() {
		t.Fatalf("CanonicalHash(10) = %x %v", ch, ok)
	}

	setup := newTestSetup(t, 4)
	e, _ := newTestEngine(t, setup, 0)
	e.SetCommittedChain(c)
	if got := e.checkCommittedFloor(sibling.Hash()); got != floorRefuse {
		t.Fatalf("stored sibling: %s, want refuse", got)
	}
	if got := e.checkCommittedFloor(committed.Hash()); got != floorAllow {
		t.Fatalf("committed block: %s, want allow", got)
	}
}
