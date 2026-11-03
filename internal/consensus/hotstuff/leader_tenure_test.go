package hotstuff

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

func TestLeaderTenureSchedule(t *testing.T) {
	vs := NewValidatorSet(make([]ValidatorInfo, 7), 2)
	for _, tc := range []struct {
		view, tenure uint64
		want         ValidatorIndex
	}{
		{0, 16, 0}, {1, 16, 0}, {15, 16, 0}, {16, 16, 1}, {31, 16, 1}, {32, 16, 2},
		{111, 16, 6}, {112, 16, 0}, {math.MaxUint64, 16, 0},
		{math.MaxUint64, math.MaxUint64, 1}, {math.MaxUint64 - 1, math.MaxUint64, 0},
		{8, 0, 1}, {8, 1, 1},
	} {
		if got := LeaderForViewWithTenure(tc.view, vs, tc.tenure); got != tc.want {
			t.Fatalf("view=%d tenure=%d got=%d want=%d", tc.view, tc.tenure, got, tc.want)
		}
	}
	if LeaderForViewWithTenure(9, NewValidatorSet(nil, 0), 16) != 0 {
		t.Fatal("empty set")
	}
}

func TestPersistedSealEnablesBuildAhead(t *testing.T) {
	setup := newTestSetup(t, 7)
	for _, persisted := range []bool{false, true} {
		e, ch := newTestEngine(t, setup, 0)
		e.leaderTenure = 16
		e.roundState.AdvanceView(1)
		h := &HotStuff{engine: e}
		hash, parent := types.Hash{42}, types.Hash{41}
		if persisted {
			h.NotifyBlockPersisted(hash, types.Hash{43}, parent)
		}
		h.NotifyBlockSealed(hash, types.Hash{43})
		proposals, hints := 0, 0
		for _, out := range drainOutputs(ch) {
			if out.Type == OutputBroadcast && out.Message.Type == MsgProposal {
				proposals++
			}
			if out.Type == OutputSpeculativeBuild {
				hints++
				if out.Hash != hash || out.View != 2 {
					t.Fatal("build-ahead did not extend the persisted seal")
				}
			}
		}
		if proposals != 1 || (hints == 1) != persisted || hints > 1 {
			t.Fatalf("persisted=%v proposals=%d hints=%d", persisted, proposals, hints)
		}
		if persisted && e.importedParents[hash] != parent {
			t.Fatal("persisted parent evidence missing")
		}
	}
}

func TestPersistedSealRejectsMovedJustify(t *testing.T) {
	setup := newTestSetup(t, 7)
	e, ch := newTestEngine(t, setup, 0)
	e.leaderTenure = 16
	e.roundState.AdvanceView(2)
	e.roundState.UpdateLockedQC(&QuorumCertificate{View: 1, BlockHash: types.Hash{40}})
	h := &HotStuff{engine: e}
	h.NotifyBlockPersisted(types.Hash{42}, types.Hash{43}, types.Hash{41})
	h.NotifyBlockSealed(types.Hash{42}, types.Hash{43})
	if len(drainOutputs(ch)) != 0 || e.roundState.HasVotedInView(2) {
		t.Fatal("seal with a stale parent emitted a proposal or vote")
	}
}

func TestLeaderTenureAdapterRoundTrip(t *testing.T) {
	setup := newTestSetup(t, 7)
	cfg := params.HotStuffConfig{LeaderTenure: 16, BaseTimeout: 1000, MaxTimeout: 10000}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var decoded params.HotStuffConfig
	if err = json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, c := range []*params.HotStuffConfig{&cfg, &decoded} {
		h := New(c, nil)
		h.Authorize(setup.validators[0].Address, setup.keys[0])
		if err := h.InitEngine(setup.validators, setup.f); err != nil {
			t.Fatal(err)
		}
		ce := h.Engine()
		if ce.CurrentLeaderIndex() != 0 || !ce.IsCurrentLeader() || ce.LeaderForView(16, setup.vs) != 1 {
			t.Fatal("adapter lost the chain schedule")
		}
		// Validator-set changes alter the modulus, not the immutable tenure.
		if ce.LeaderForView(64, NewValidatorSet(setup.validators[:4], 1)) != 0 {
			t.Fatal("epoch set changed tenure")
		}
	}
}

func TestLeaderTenureRejectsWrongLeaderProposal(t *testing.T) {
	setup := newTestSetup(t, 7)
	for _, view := range []uint64{1, 16, 32, 112} {
		follower, _ := newTestEngine(t, setup, 6)
		follower.leaderTenure = 16
		follower.roundState.AdvanceView(view)
		expected := LeaderForViewWithTenure(view, setup.vs, 16)
		wrong := (expected + 1) % 7
		hash := types.Hash{byte(view), 0x42}
		proposal := &Proposal{View: view, BlockHash: hash, JustifyQC: GenesisQC(), Proposer: wrong, Signature: setup.keys[wrong].Sign(SigningMessage(view, hash)).Marshal()}
		err := follower.ProcessEvent(ConsensusEvent{Type: EventMessage, Msg: ConsensusMsg{Type: MsgProposal, Payload: proposal}})
		if _, ok := err.(*InvalidProposerError); !ok {
			t.Fatalf("view %d: expected wrong-leader rejection, got %v", view, err)
		}
		proposal.Proposer = expected
		proposal.Signature = setup.keys[expected].Sign(SigningMessage(view, hash)).Marshal()
		if err := follower.ProcessEvent(ConsensusEvent{Type: EventMessage, Msg: ConsensusMsg{Type: MsgProposal, Payload: proposal}}); err != nil {
			t.Fatalf("view %d: valid tenure leader rejected: %v", view, err)
		}
	}
}

func TestLeaderTenureSevenNodeCommitAndTimeout(t *testing.T) {
	for _, v4 := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "H2-v4"}[v4], func(t *testing.T) {
			testLeaderTenureSevenNodeCommitAndTimeout(t, v4)
		})
	}
}

func testLeaderTenureSevenNodeCommitAndTimeout(t *testing.T, v4 bool) {
	h := newChaosHarness(t, 7)
	for _, e := range h.engines {
		e.leaderTenure = 16
		if v4 {
			e.EnableH2V4(H2V4ChainIdentity{ChainID: 1143, GenesisHash: types.Hash{0x42}})
		}
	}
	for view := uint64(1); view <= 113; view++ {
		// Exercise timeout inside a tenure and across its final view.
		if view == 5 || view == 15 {
			h.runTimeoutViewChange(view)
		} else {
			hash := blockHashForView(view)
			h.markBlockImported(hash)
			h.runConsensusRound(view, hash)
		}
		for i, e := range h.engines {
			if got := e.CurrentView(); got != view+1 {
				t.Fatalf("node %d after view %d at %d", i, view, got)
			}
		}
	}
	if len(h.committed) != 111 {
		t.Fatalf("commits=%d want 111", len(h.committed))
	}
}

func TestLeaderTenureSelfVoteBuildHint(t *testing.T) {
	setup := newTestSetup(t, 7)
	for _, tc := range []struct {
		view     uint64
		imported bool
		want     bool
	}{
		{1, true, true}, {1, false, false}, {15, true, false}, {16, true, true},
	} {
		leader := LeaderForViewWithTenure(tc.view, setup.vs, 16)
		e, ch := newTestEngine(t, setup, int(leader))
		e.leaderTenure = 16
		e.roundState.AdvanceView(tc.view)
		hash := blockHashForView(tc.view)
		if tc.imported {
			if err := e.ProcessEvent(ConsensusEvent{Type: EventBlockImported, Hash: hash}); err != nil {
				t.Fatal(err)
			}
		}
		drainOutputs(ch)
		if err := e.ProcessEvent(ConsensusEvent{Type: EventBlockReady, Hash: hash}); err != nil {
			t.Fatal(err)
		}
		hints := 0
		for _, o := range drainOutputs(ch) {
			if o.Type == OutputSpeculativeBuild {
				hints++
				if o.View != tc.view+1 || o.Hash != hash {
					t.Fatal("hint has wrong parent/view")
				}
			}
		}
		if (hints == 1) != tc.want || hints > 1 {
			t.Fatalf("view=%d imported=%v hints=%d want=%v", tc.view, tc.imported, hints, tc.want)
		}
	}
}
