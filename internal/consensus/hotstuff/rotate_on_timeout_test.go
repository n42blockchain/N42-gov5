// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"math"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

// rotSim is a deterministic in-process network of HotStuff engines driven by
// a virtual clock. Messages are delivered instantly; a proposal costs
// rotSimBlockTime of virtual time; a view whose leader is silent ends when
// the (virtual) pacemaker deadlines of the live nodes fire. Deadlines follow
// the engine's own backoff (Pacemaker.TimeoutDuration with the node's
// consecutive-timeout counter), so stall lengths are the real protocol's.
type rotSim struct {
	t        *testing.T
	setup    *testSetup
	engines  []*ConsensusEngine
	chans    []chan EngineOutput
	alive    []bool
	now      time.Duration
	deadline []time.Duration
	seenView []ViewNumber
	proposed map[ViewNumber]bool
	commits  []rotCommit
	commitAt map[ViewNumber]bool
	queue    []rotDelivery
	tenure   uint64
	rotate   bool
}

type rotCommit struct {
	view   ViewNumber
	leader ValidatorIndex
	at     time.Duration
}

type rotDelivery struct {
	to int
	ev ConsensusEvent
}

const (
	rotSimBaseMs    = 1000
	rotSimMaxMs     = 10000
	rotSimBlockTime = 50 * time.Millisecond
	rotSimBase      = rotSimBaseMs * time.Millisecond
)

func newRotSim(t *testing.T, n int, tenure uint64, rotate bool) *rotSim {
	t.Helper()
	setup := newTestSetup(t, n)
	s := &rotSim{
		t: t, setup: setup, tenure: tenure, rotate: rotate,
		alive:    make([]bool, n),
		deadline: make([]time.Duration, n),
		seenView: make([]ViewNumber, n),
		proposed: make(map[ViewNumber]bool),
		commitAt: make(map[ViewNumber]bool),
	}
	for i := 0; i < n; i++ {
		e, ch := s.newEngine(i)
		s.engines = append(s.engines, e)
		s.chans = append(s.chans, ch)
		s.alive[i] = true
	}
	return s
}

func (s *rotSim) newEngine(i int) (*ConsensusEngine, chan EngineOutput) {
	ch := make(chan EngineOutput, 1<<14)
	e := NewConsensusEngine(ValidatorIndex(i), s.setup.keys[i], s.setup.vs, rotSimBaseMs, rotSimMaxMs, ch)
	e.leaderTenure = s.tenure
	e.SetRotateOnTimeout(s.rotate, 0)
	return e, ch
}

func (s *rotSim) leader(view ViewNumber) ValidatorIndex {
	return LeaderForViewWithTenure(view, s.setup.vs, s.tenure)
}

// pump routes every pending output until the network is quiescent.
func (s *rotSim) pump() {
	for {
		progressed := false
		for i, ch := range s.chans {
			for _, o := range drainOutputs(ch) {
				if !s.alive[i] {
					continue
				}
				progressed = true
				s.route(i, o)
			}
		}
		for len(s.queue) > 0 {
			d := s.queue[0]
			s.queue = s.queue[1:]
			progressed = true
			if s.alive[d.to] {
				_ = s.engines[d.to].ProcessEvent(d.ev)
			}
		}
		if !progressed {
			return
		}
	}
}

func (s *rotSim) route(from int, o EngineOutput) {
	switch o.Type {
	case OutputBroadcast:
		for j := range s.engines {
			if j != from {
				s.queue = append(s.queue, rotDelivery{to: j, ev: ConsensusEvent{Type: EventMessage, Msg: *o.Message}})
			}
		}
	case OutputSendToValidator:
		s.queue = append(s.queue, rotDelivery{to: int(o.Target), ev: ConsensusEvent{Type: EventMessage, Msg: *o.Message}})
	case OutputExecuteBlock:
		s.queue = append(s.queue, rotDelivery{to: from, ev: ConsensusEvent{Type: EventBlockImported, Hash: o.Hash}})
	case OutputBlockCommitted:
		if !s.commitAt[o.View] {
			s.commitAt[o.View] = true
			s.commits = append(s.commits, rotCommit{view: o.View, leader: s.leader(o.View), at: s.now})
		}
	}
}

// refreshDeadlines arms a virtual deadline for every node that entered a new
// view since the last check.
func (s *rotSim) refreshDeadlines() {
	for i, e := range s.engines {
		if v := e.CurrentView(); v != s.seenView[i] {
			s.seenView[i] = v
			s.deadline[i] = s.now + e.Pacemaker().TimeoutDuration(e.roundState.ConsecutiveTimeouts())
		}
	}
}

// step performs one unit of progress: a live leader proposes, or, when no
// live node can propose, virtual time jumps to the earliest deadline and
// those nodes time out.
func (s *rotSim) step() {
	s.pump()
	s.refreshDeadlines()
	for i, e := range s.engines {
		v := e.CurrentView()
		if !s.alive[i] || s.leader(v) != ValidatorIndex(i) || s.proposed[v] {
			continue
		}
		s.proposed[v] = true
		s.now += rotSimBlockTime
		_ = e.ProcessEvent(ConsensusEvent{Type: EventBlockReady, Hash: rotHash(v)})
		s.pump()
		s.refreshDeadlines()
		return
	}
	next := time.Duration(math.MaxInt64)
	for i := range s.engines {
		if s.alive[i] && s.deadline[i] < next {
			next = s.deadline[i]
		}
	}
	if next > s.now {
		s.now = next
	}
	for i, e := range s.engines {
		if s.alive[i] && s.deadline[i] <= s.now {
			_ = timeoutNow(e)
			s.deadline[i] = s.now + e.Pacemaker().TimeoutDuration(e.roundState.ConsecutiveTimeouts())
		}
	}
	s.pump()
	s.refreshDeadlines()
}

// runUntil steps until cond holds or virtual time passes limit.
func (s *rotSim) runUntil(limit time.Duration, cond func() bool) bool {
	for s.now <= limit {
		if cond() {
			return true
		}
		s.step()
	}
	return cond()
}

func (s *rotSim) maxCommitView() ViewNumber {
	var m ViewNumber
	for _, c := range s.commits {
		if c.view > m {
			m = c.view
		}
	}
	return m
}

func (s *rotSim) commitsAfter(view ViewNumber) []rotCommit {
	var out []rotCommit
	for _, c := range s.commits {
		if c.view > view {
			out = append(out, c)
		}
	}
	return out
}

func rotHash(view ViewNumber) types.Hash {
	var h types.Hash
	for i := 0; i < 8; i++ {
		h[i] = byte(view >> (8 * i))
	}
	h[31] = 0x42
	return h
}

func (s *rotSim) lastCommitBefore(view ViewNumber) rotCommit {
	var last rotCommit
	for _, c := range s.commits {
		if c.view < view && c.view >= last.view {
			last = c
		}
	}
	return last
}

// TestViewAfterTimeoutVectors pins the schedule function; the same vectors
// are in the cross-client spec (N42-SYNC-SPEC "Leader rotation on timeout").
func TestViewAfterTimeoutVectors(t *testing.T) {
	for _, tc := range []struct {
		tcView, tenure, epoch uint64
		rotate                bool
		want                  uint64
	}{
		{8, 8, 0, false, 9},   // switch off: classic
		{8, 8, 0, true, 16},   // first view of the window -> next window
		{9, 8, 0, true, 16},   // mid-window
		{15, 8, 0, true, 16},  // last view of the window: same as classic
		{16, 8, 0, true, 24},  // the substitute's window timed out too
		{7, 8, 0, true, 8},    // leader 0's last view
		{0, 8, 0, true, 8},    // degenerate genesis TC
		{5, 1, 0, true, 6},    // tenure 1: unchanged
		{5, 0, 0, true, 6},    // tenure 0 = 1
		{8, 8, 10, true, 11},  // clamp to epoch boundary 11 ((11-1)%10==0)
		{10, 8, 10, true, 11}, // boundary right after
		{11, 8, 10, true, 16}, // boundary already behind: window wins
		{17, 8, 10, true, 21}, // next boundary 21 < 24
		{100, 32, 0, true, 128},
		{math.MaxUint64 - 1, 8, 0, true, math.MaxUint64}, // overflow guard keeps +1
		{math.MaxUint64 - 3, 8, 0, true, math.MaxUint64 - 2},
	} {
		if got := ViewAfterTimeout(tc.tcView, tc.tenure, tc.epoch, tc.rotate); got != tc.want {
			t.Errorf("ViewAfterTimeout(%d, tenure=%d, epoch=%d, rotate=%v) = %d, want %d",
				tc.tcView, tc.tenure, tc.epoch, tc.rotate, got, tc.want)
		}
	}
}

func TestViewAfterTCActivationView(t *testing.T) {
	setup := newTestSetup(t, 7)
	e, _ := newTestEngine(t, setup, 0)
	e.leaderTenure = 8
	if got := e.viewAfterTC(8); got != 9 {
		t.Fatalf("default off: got %d", got)
	}
	e.SetRotateOnTimeout(true, 20)
	if got := e.viewAfterTC(8); got != 9 {
		t.Fatalf("before activation: got %d", got)
	}
	if got := e.viewAfterTC(20); got != 24 {
		t.Fatalf("at activation: got %d", got)
	}
}

func TestRotateOnTimeoutSettingResolution(t *testing.T) {
	ten := uint64(10)
	t.Setenv("N42_HOTSTUFF_ROTATE_ON_TIMEOUT", "")
	if on, _ := rotateOnTimeoutSetting(&params.HotStuffConfig{}); on {
		t.Fatal("absent field must be off")
	}
	if on, from := rotateOnTimeoutSetting(&params.HotStuffConfig{RotateOnTimeoutView: &ten}); !on || from != 10 {
		t.Fatalf("chainspec: on=%v from=%d", on, from)
	}
	t.Setenv("N42_HOTSTUFF_ROTATE_ON_TIMEOUT", "1")
	if on, from := rotateOnTimeoutSetting(&params.HotStuffConfig{}); !on || from != 0 {
		t.Fatalf("env on: on=%v from=%d", on, from)
	}
	t.Setenv("N42_HOTSTUFF_ROTATE_ON_TIMEOUT", "0")
	if on, _ := rotateOnTimeoutSetting(&params.HotStuffConfig{RotateOnTimeoutView: &ten}); on {
		t.Fatal("env 0 must override the chainspec")
	}
}

// TestRotateOnTimeoutDeadTenureLeader: 7 validators, tenure 8, the validator
// whose tenure starts next (index 1, views 8..15) is silent. With rotation a
// block by a different leader commits within ~2x the base timeout of the
// last good block and the chain keeps a steady pace; without it every view of
// the dead tenure times out with doubling backoff.
func TestRotateOnTimeoutDeadTenureLeader(t *testing.T) {
	for _, rotate := range []bool{true, false} {
		s := newRotSim(t, 7, 8, rotate)
		s.alive[1] = false
		if !s.runUntil(10*time.Minute, func() bool { return s.maxCommitView() >= 40 }) {
			t.Fatalf("rotate=%v: chain did not reach view 40 (max %d)", rotate, s.maxCommitView())
		}
		last := s.lastCommitBefore(8)
		if last.view != 7 {
			t.Fatalf("rotate=%v: healthy tenure did not commit through view 7 (last %d)", rotate, last.view)
		}
		after := s.commitsAfter(7)
		first := after[0]
		gap := first.at - last.at
		for _, c := range s.commits {
			if c.leader == 1 {
				t.Fatalf("rotate=%v: dead validator committed view %d", rotate, c.view)
			}
		}
		if rotate {
			if first.view != 16 || first.leader != 2 {
				t.Fatalf("first post-stall commit view=%d leader=%d, want view 16 leader 2", first.view, first.leader)
			}
			if gap > 2*rotSimBase {
				t.Fatalf("stall %v exceeds 2x base timeout", gap)
			}
			// Steady afterwards: views 16..40 commit back to back.
			for i := 1; i < len(after) && after[i].view <= 40; i++ {
				if after[i].view != after[i-1].view+1 || after[i].at-after[i-1].at > 2*rotSimBlockTime {
					t.Fatalf("no steady progress after rotation: %+v -> %+v", after[i-1], after[i])
				}
			}
		} else {
			if first.view != 16 {
				t.Fatalf("off: first post-stall commit view %d, want 16", first.view)
			}
			// 8 timeouts with doubling: 1+2+4+8+10*4 = 55 s at base 1 s / max 10 s.
			if gap < 40*rotSimBase {
				t.Fatalf("off: stall %v, expected the old per-view backoff stall", gap)
			}
		}
	}
}

// TestRotateOnTimeoutTwoConsecutiveDeadLeaders: validators 1 and 2 (windows
// 8..15 and 16..23) are both silent; rotation skips both windows.
func TestRotateOnTimeoutTwoConsecutiveDeadLeaders(t *testing.T) {
	s := newRotSim(t, 7, 8, true)
	s.alive[1], s.alive[2] = false, false
	if !s.runUntil(5*time.Minute, func() bool { return s.maxCommitView() >= 30 }) {
		t.Fatalf("chain did not reach view 30 (max %d)", s.maxCommitView())
	}
	last := s.lastCommitBefore(8)
	first := s.commitsAfter(7)[0]
	if first.view != 24 || first.leader != 3 {
		t.Fatalf("first post-stall commit view=%d leader=%d, want view 24 leader 3", first.view, first.leader)
	}
	// One base timeout, then one doubled timeout: 1 s + 2 s.
	if gap := first.at - last.at; gap > 4*rotSimBase {
		t.Fatalf("stall %v exceeds 4x base timeout", gap)
	}
}

// TestRotateOnTimeoutDeadLeaderComesBack: validator 1 is silent through its
// first tenure, then returns; it catches up and leads its next window
// (views 64..71) normally.
func TestRotateOnTimeoutDeadLeaderComesBack(t *testing.T) {
	s := newRotSim(t, 7, 8, true)
	s.alive[1] = false
	if !s.runUntil(5*time.Minute, func() bool { return s.maxCommitView() >= 20 }) {
		t.Fatalf("chain did not reach view 20 (max %d)", s.maxCommitView())
	}
	s.alive[1] = true
	s.refreshDeadlines()
	if !s.runUntil(10*time.Minute, func() bool { return s.maxCommitView() >= 75 }) {
		t.Fatalf("chain did not reach view 75 after the return (max %d)", s.maxCommitView())
	}
	led := 0
	for _, c := range s.commits {
		if c.leader == 1 {
			led++
			if c.view < 64 || c.view > 71 {
				t.Fatalf("returned validator led unexpected view %d", c.view)
			}
		}
	}
	if led != 8 {
		t.Fatalf("returned validator committed %d blocks of its window, want 8", led)
	}
	if got := s.engines[1].CurrentView(); got != s.engines[0].CurrentView() {
		t.Fatalf("returned validator at view %d, network at %d", got, s.engines[0].CurrentView())
	}
}

// TestRotateOnTimeoutRestartMidRotation: a follower restarted from its
// persisted state after the rotation recomputes the same leader schedule and
// keeps voting.
func TestRotateOnTimeoutRestartMidRotation(t *testing.T) {
	s := newRotSim(t, 7, 8, true)
	s.alive[1] = false
	if !s.runUntil(5*time.Minute, func() bool { return s.maxCommitView() >= 18 }) {
		t.Fatalf("chain did not reach view 18 (max %d)", s.maxCommitView())
	}
	const node = 4
	st := s.engines[node].SnapshotState()
	fresh, ch := s.newEngine(node)
	fresh.RestoreState(st.View, st.LockedQC, st.LastCommittedQC, st.ConsecutiveTimeouts)
	fresh.RestoreVoteCommitments(st.LastVotedView, st.LastVotedHash, st.LastCommitVotedView, st.LastCommitVotedHash)
	s.engines[node], s.chans[node] = fresh, ch
	if fresh.CurrentLeaderIndex() != s.engines[0].CurrentLeaderIndex() {
		t.Fatalf("restarted node computes leader %d, network %d", fresh.CurrentLeaderIndex(), s.engines[0].CurrentLeaderIndex())
	}
	// Silence one more node so the restarted one is needed for every quorum
	// (5 of 7 = quorum): progress proves it accepts the rotated leaders.
	s.alive[5] = false
	if !s.runUntil(10*time.Minute, func() bool { return s.maxCommitView() >= 30 }) {
		t.Fatalf("chain stalled after restart (max %d)", s.maxCommitView())
	}
}

// TestRotateOnTimeoutRejectsWrongLeader: after a TC for view 8 a follower is
// in view 16; a proposal from the timed-out tenure owner is rejected, a
// NewView for the classic successor (9) is rejected, and the rotated
// leader's NewView is accepted.
func TestRotateOnTimeoutRejectsWrongLeader(t *testing.T) {
	setup := newTestSetup(t, 7)
	follower, _ := newTestEngine(t, setup, 6)
	follower.leaderTenure = 8
	follower.SetRotateOnTimeout(true, 0)
	follower.roundState.AdvanceView(8)
	tc := buildTestTC(t, setup, 8, 0)

	nv := func(view ViewNumber, leader ValidatorIndex) ConsensusEvent {
		return ConsensusEvent{Type: EventMessage, Msg: ConsensusMsg{Type: MsgNewView, Payload: &NewViewMsg{
			View: view, TimeoutCert: *tc, Leader: leader,
			Signature: setup.keys[leader].Sign(NewViewSigningMessage(view)).Marshal(),
		}}}
	}
	if err := follower.ProcessEvent(nv(9, 1)); err == nil || follower.CurrentView() != 8 {
		t.Fatalf("classic-successor NewView accepted: err=%v view=%d", err, follower.CurrentView())
	}
	if err := follower.ProcessEvent(nv(16, 2)); err != nil || follower.CurrentView() != 16 {
		t.Fatalf("rotated NewView rejected: err=%v view=%d", err, follower.CurrentView())
	}
	for _, proposer := range []ValidatorIndex{1, 3} {
		hash := types.Hash{0x16, byte(proposer)}
		p := &Proposal{View: 16, BlockHash: hash, JustifyQC: GenesisQC(), Proposer: proposer,
			Signature: setup.keys[proposer].Sign(SigningMessage(16, hash)).Marshal()}
		err := follower.ProcessEvent(ConsensusEvent{Type: EventMessage, Msg: ConsensusMsg{Type: MsgProposal, Payload: p}})
		if _, ok := err.(*InvalidProposerError); !ok {
			t.Fatalf("proposer %d at view 16: expected InvalidProposerError, got %v", proposer, err)
		}
	}
	hash := types.Hash{0x16, 2}
	p := &Proposal{View: 16, BlockHash: hash, JustifyQC: GenesisQC(), Proposer: 2,
		Signature: setup.keys[2].Sign(SigningMessage(16, hash)).Marshal()}
	if err := follower.ProcessEvent(ConsensusEvent{Type: EventMessage, Msg: ConsensusMsg{Type: MsgProposal, Payload: p}}); err != nil {
		t.Fatalf("rotated leader's proposal rejected: %v", err)
	}
}

// TestRotateOnTimeoutPiggybackedTCJumpsToWindow: a lagging node adopting a
// SyncInfo TC lands on the rotated view, not tc.View+1.
func TestRotateOnTimeoutPiggybackedTCJumpsToWindow(t *testing.T) {
	setup := newTestSetup(t, 7)
	e, _ := newTestEngine(t, setup, 0)
	e.leaderTenure = 8
	e.SetRotateOnTimeout(true, 0)
	tc := buildTestTC(t, setup, 8, 0)
	tm := &TimeoutMessage{View: 16, HighQC: GenesisQC(), Sender: 3,
		Signature: setup.keys[3].Sign(TimeoutSigningMessage(16)).Marshal(), HighTC: tc}
	_ = e.ProcessEvent(ConsensusEvent{Type: EventMessage, Msg: ConsensusMsg{Type: MsgTimeout, Payload: tm}})
	if got := e.CurrentView(); got != 16 {
		t.Fatalf("lagging node at view %d, want 16", got)
	}
}

// TestRotateOnTimeoutTenureOneUnchanged: with tenure 1 the switch changes
// nothing; the commit sequence and timing match the switch-off run.
func TestRotateOnTimeoutTenureOneUnchanged(t *testing.T) {
	var runs [2][]rotCommit
	for k, rotate := range []bool{false, true} {
		s := newRotSim(t, 7, 1, rotate)
		s.alive[3] = false
		if !s.runUntil(10*time.Minute, func() bool { return s.maxCommitView() >= 30 }) {
			t.Fatalf("rotate=%v: chain did not reach view 30", rotate)
		}
		runs[k] = s.commits
	}
	if len(runs[0]) != len(runs[1]) {
		t.Fatalf("commit counts differ: %d vs %d", len(runs[0]), len(runs[1]))
	}
	for i := range runs[0] {
		if runs[0][i] != runs[1][i] {
			t.Fatalf("commit %d differs: %+v vs %+v", i, runs[0][i], runs[1][i])
		}
	}
}
