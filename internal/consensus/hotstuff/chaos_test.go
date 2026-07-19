// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package hotstuff

import (
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
)

// chaosHarness manages a cluster of 7 HotStuff-2 consensus engines for
// integration testing. It orchestrates message routing between nodes and
// tracks committed blocks.
type chaosHarness struct {
	t         *testing.T
	engines   []*ConsensusEngine
	channels  []chan EngineOutput
	setup     *testSetup
	committed []struct {
		view ViewNumber
		hash types.Hash
	}
}

// newChaosHarness creates n consensus engines using the existing test helpers.
func newChaosHarness(t *testing.T, n int) *chaosHarness {
	t.Helper()
	setup := newTestSetup(t, n)
	engines := make([]*ConsensusEngine, n)
	channels := make([]chan EngineOutput, n)
	for i := 0; i < n; i++ {
		engines[i], channels[i] = newTestEngine(t, setup, i)
	}
	return &chaosHarness{
		t:        t,
		engines:  engines,
		channels: channels,
		setup:    setup,
	}
}

// drainAll drains all output channels, returning outputs indexed by engine.
func (h *chaosHarness) drainAll() [][]EngineOutput {
	all := make([][]EngineOutput, len(h.channels))
	for i, ch := range h.channels {
		all[i] = drainOutputs(ch)
	}
	return all
}

// markBlockImported models successful execution-layer propagation independently
// from consensus-message routing. Import-gated validators may vote only after
// receiving this evidence.
func (h *chaosHarness) markBlockImported(blockHash types.Hash) {
	h.t.Helper()
	for i, engine := range h.engines {
		if err := engine.ProcessEvent(ConsensusEvent{Type: EventBlockImported, Hash: blockHash}); err != nil {
			h.t.Fatalf("node %d import block %s: %v", i, blockHash, err)
		}
	}
}

// blockHashForView generates a deterministic block hash for a given view.
func blockHashForView(view ViewNumber) types.Hash {
	var hash types.Hash
	hash[0] = byte(view & 0xff)
	hash[1] = byte((view >> 8) & 0xff)
	hash[2] = byte((view >> 16) & 0xff)
	hash[3] = 0xBE
	hash[4] = 0xEF
	return hash
}

// runConsensusRound orchestrates one full HotStuff-2 round across all engines.
//
// The protocol flow is:
//  1. Leader (view % n) proposes a block via EventBlockReady
//  2. All non-leader nodes receive the Proposal via ProcessEvent
//  3. Non-leaders emit Vote outputs (SendToValidator to leader); route to leader
//  4. Leader forms PrepareQC and broadcasts PrepareQCMsg
//  5. All non-leaders receive PrepareQCMsg and emit CommitVote outputs; route to leader
//  6. Leader forms CommitQC and broadcasts Decide
//  7. All non-leaders receive Decide, commit the block
//  8. Track committed blocks
func (h *chaosHarness) runConsensusRound(view ViewNumber, blockHash types.Hash) {
	h.t.Helper()
	n := len(h.engines)
	leaderIdx := int(view % uint64(n))

	// Step 1: Leader proposes.
	err := h.engines[leaderIdx].ProcessEvent(ConsensusEvent{
		Type: EventBlockReady,
		Hash: blockHash,
	})
	if err != nil {
		h.t.Fatalf("view %d: leader %d onBlockReady failed: %v", view, leaderIdx, err)
	}

	leaderOutputs := drainOutputs(h.channels[leaderIdx])

	// Find the Proposal broadcast from leader outputs.
	var proposal *Proposal
	for _, o := range leaderOutputs {
		if o.Type == OutputBroadcast && o.Message != nil && o.Message.Type == MsgProposal {
			proposal = o.Message.Payload.(*Proposal)
		}
	}
	if proposal == nil {
		h.t.Fatalf("view %d: leader %d did not broadcast Proposal", view, leaderIdx)
	}

	// Step 2: Route Proposal to all non-leader validators.
	for i := 0; i < n; i++ {
		if i == leaderIdx {
			continue
		}
		err := h.engines[i].ProcessEvent(ConsensusEvent{
			Type: EventMessage,
			Msg:  ConsensusMsg{Type: MsgProposal, Payload: proposal},
		})
		if err != nil {
			h.t.Fatalf("view %d: node %d processProposal failed: %v", view, i, err)
		}
	}

	// Step 3: Collect Vote outputs from non-leaders, route to leader.
	for i := 0; i < n; i++ {
		if i == leaderIdx {
			continue
		}
		outputs := drainOutputs(h.channels[i])
		for _, o := range outputs {
			if o.Type == OutputSendToValidator && o.Message != nil && o.Message.Type == MsgVote {
				err := h.engines[leaderIdx].ProcessEvent(ConsensusEvent{
					Type: EventMessage,
					Msg:  *o.Message,
				})
				if err != nil {
					h.t.Fatalf("view %d: leader processVote from %d failed: %v", view, i, err)
				}
			}
		}
	}

	// Step 4: Leader should have formed PrepareQC. Drain leader and find PrepareQCMsg.
	leaderOutputs = drainOutputs(h.channels[leaderIdx])
	var prepareQCMsg *PrepareQCMsg
	for _, o := range leaderOutputs {
		if o.Type == OutputBroadcast && o.Message != nil && o.Message.Type == MsgPrepareQC {
			prepareQCMsg = o.Message.Payload.(*PrepareQCMsg)
		}
	}
	if prepareQCMsg == nil {
		h.t.Fatalf("view %d: leader did not broadcast PrepareQC", view)
	}

	// Step 5: Route PrepareQCMsg to all non-leaders; collect CommitVote outputs.
	for i := 0; i < n; i++ {
		if i == leaderIdx {
			continue
		}
		err := h.engines[i].ProcessEvent(ConsensusEvent{
			Type: EventMessage,
			Msg:  ConsensusMsg{Type: MsgPrepareQC, Payload: prepareQCMsg},
		})
		if err != nil {
			h.t.Fatalf("view %d: node %d processPrepareQC failed: %v", view, i, err)
		}
	}

	for i := 0; i < n; i++ {
		if i == leaderIdx {
			continue
		}
		outputs := drainOutputs(h.channels[i])
		for _, o := range outputs {
			if o.Type == OutputSendToValidator && o.Message != nil && o.Message.Type == MsgCommitVote {
				err := h.engines[leaderIdx].ProcessEvent(ConsensusEvent{
					Type: EventMessage,
					Msg:  *o.Message,
				})
				if err != nil {
					h.t.Fatalf("view %d: leader processCommitVote from %d failed: %v", view, i, err)
				}
			}
		}
	}

	// Step 6: Leader should have formed CommitQC and broadcast Decide.
	leaderOutputs = drainOutputs(h.channels[leaderIdx])
	var decide *Decide
	for _, o := range leaderOutputs {
		if o.Type == OutputBroadcast && o.Message != nil && o.Message.Type == MsgDecide {
			decide = o.Message.Payload.(*Decide)
		}
	}
	if decide == nil {
		h.t.Fatalf("view %d: leader did not broadcast Decide", view)
	}

	// Track leader commit.
	for _, o := range leaderOutputs {
		if o.Type == OutputBlockCommitted {
			h.committed = append(h.committed, struct {
				view ViewNumber
				hash types.Hash
			}{view: o.View, hash: o.Hash})
		}
	}

	// Step 7: Route Decide to all non-leaders.
	for i := 0; i < n; i++ {
		if i == leaderIdx {
			continue
		}
		err := h.engines[i].ProcessEvent(ConsensusEvent{
			Type: EventMessage,
			Msg:  ConsensusMsg{Type: MsgDecide, Payload: decide},
		})
		if err != nil {
			h.t.Fatalf("view %d: node %d processDecide failed: %v", view, i, err)
		}
	}

	// Drain remaining outputs from all nodes.
	h.drainAll()
}

// runTimeoutViewChange triggers timeout on all engines for the given view,
// routes TimeoutMessages between engines, and advances to the next view
// via the NewView protocol.
func (h *chaosHarness) runTimeoutViewChange(view ViewNumber) {
	h.t.Helper()
	n := len(h.engines)

	// All engines call OnTimeout.
	for i := 0; i < n; i++ {
		err := timeoutNow(h.engines[i])
		if err != nil {
			h.t.Fatalf("view %d: node %d OnTimeout failed: %v", view, i, err)
		}
	}

	// Collect TimeoutMessages from all engines.
	allOutputs := h.drainAll()
	var timeoutMsgs []*TimeoutMessage
	for _, outputs := range allOutputs {
		for _, o := range outputs {
			if o.Type == OutputBroadcast && o.Message != nil && o.Message.Type == MsgTimeout {
				tm := o.Message.Payload.(*TimeoutMessage)
				timeoutMsgs = append(timeoutMsgs, tm)
			}
		}
	}

	// Route all timeout messages to all engines.
	for _, tm := range timeoutMsgs {
		for i := 0; i < n; i++ {
			// Skip sending to the sender (they already processed their own).
			if ValidatorIndex(i) == tm.Sender {
				continue
			}
			_ = h.engines[i].ProcessEvent(ConsensusEvent{
				Type: EventMessage,
				Msg:  ConsensusMsg{Type: MsgTimeout, Payload: tm},
			})
		}
	}

	// The next view leader should form TC and broadcast NewView.
	// Drain and find NewView messages.
	allOutputs = h.drainAll()
	var newViewMsgs []*NewViewMsg
	for _, outputs := range allOutputs {
		for _, o := range outputs {
			if o.Type == OutputBroadcast && o.Message != nil && o.Message.Type == MsgNewView {
				nv := o.Message.Payload.(*NewViewMsg)
				newViewMsgs = append(newViewMsgs, nv)
			}
		}
	}

	// Route NewView messages to all non-sender engines.
	for _, nv := range newViewMsgs {
		for i := 0; i < n; i++ {
			if ValidatorIndex(i) == nv.Leader {
				continue
			}
			_ = h.engines[i].ProcessEvent(ConsensusEvent{
				Type: EventMessage,
				Msg:  ConsensusMsg{Type: MsgNewView, Payload: nv},
			})
		}
	}

	// Drain any remaining outputs.
	h.drainAll()
}

// TestChaos7Node_NormalConsensus runs 10 normal consensus rounds with 7
// validators and verifies all 10 blocks are committed and all engines
// advance to view 11.
func TestChaos7Node_NormalConsensus(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping long chaos test in -short mode")
	}

	h := newChaosHarness(t, 7)

	// Run 10 rounds (views 1..10).
	for view := ViewNumber(1); view <= 10; view++ {
		blockHash := blockHashForView(view)
		h.markBlockImported(blockHash)
		h.runConsensusRound(view, blockHash)
	}

	// Verify all 10 blocks committed.
	if len(h.committed) < 10 {
		t.Fatalf("expected at least 10 committed blocks, got %d", len(h.committed))
	}

	// Verify each view 1..10 was committed.
	committedViews := make(map[ViewNumber]bool)
	for _, c := range h.committed {
		committedViews[c.view] = true
	}
	for view := ViewNumber(1); view <= 10; view++ {
		if !committedViews[view] {
			t.Errorf("view %d was not committed", view)
		}
	}

	// All engines should be at view 11.
	for i, e := range h.engines {
		cv := e.CurrentView()
		if cv != 11 {
			t.Errorf("engine %d: expected view 11, got %d", i, cv)
		}
	}
}

// TestChaos7Node_TimeoutRecovery tests that the cluster recovers from a
// timeout when the leader goes silent. Three normal rounds complete,
// then view 4 is skipped (leader silent), all nodes timeout, and view 5
// resumes normally.
func TestChaos7Node_TimeoutRecovery(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping long chaos test in -short mode")
	}

	h := newChaosHarness(t, 7)

	// Run 3 normal rounds (views 1..3).
	for view := ViewNumber(1); view <= 3; view++ {
		blockHash := blockHashForView(view)
		h.markBlockImported(blockHash)
		h.runConsensusRound(view, blockHash)
	}

	// Verify 3 commits.
	if len(h.committed) < 3 {
		t.Fatalf("expected at least 3 committed blocks, got %d", len(h.committed))
	}

	// View 4: leader is silent. All engines timeout.
	h.runTimeoutViewChange(4)

	// View 4 should NOT have a commit.
	for _, c := range h.committed {
		if c.view == 4 {
			t.Fatal("view 4 should not have been committed (leader was silent)")
		}
	}

	// After timeout, engines should be at view 5.
	for i, e := range h.engines {
		cv := e.CurrentView()
		if cv != 5 {
			t.Errorf("engine %d: expected view 5, got %d", i, cv)
		}
	}

	// View 5: resume normal consensus.
	blockHash5 := blockHashForView(5)
	h.markBlockImported(blockHash5)
	h.runConsensusRound(5, blockHash5)

	// Verify view 5 committed.
	found := false
	for _, c := range h.committed {
		if c.view == 5 {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("view 5 should have been committed after timeout recovery")
	}

	// All engines at view 6.
	for i, e := range h.engines {
		cv := e.CurrentView()
		if cv != 6 {
			t.Errorf("engine %d: expected view 6, got %d", i, cv)
		}
	}
}

// TestChaos7Node_PacketDrop runs consensus with 20% random packet drop to
// simulate an unreliable network. It runs 3 clean rounds, then 20 rounds
// with packet drop. Rounds that fail to commit trigger a timeout and view
// change. The test verifies at least 5 commits and no equivocations.
func TestChaos7Node_PacketDrop(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping long chaos test in -short mode")
	}

	h := newChaosHarness(t, 7)
	rng := rand.New(rand.NewSource(42))
	n := len(h.engines)

	// Run 3 clean rounds.
	for view := ViewNumber(1); view <= 3; view++ {
		blockHash := blockHashForView(view)
		h.markBlockImported(blockHash)
		h.runConsensusRound(view, blockHash)
	}

	initialCommits := len(h.committed)
	if initialCommits < 3 {
		t.Fatalf("expected at least 3 initial commits, got %d", initialCommits)
	}

	// Track equivocation detections.
	equivocations := 0

	// Run 20 rounds with 20% packet drop.
	for round := 0; round < 20; round++ {
		// Determine the current view from the leader perspective.
		// All engines should be at roughly the same view.
		currentView := h.engines[0].CurrentView()

		blockHash := blockHashForView(currentView)
		leaderIdx := int(currentView % uint64(n))
		// Block propagation is modeled independently from the 20% consensus
		// message drop; validators that receive the Proposal have executed it.
		h.markBlockImported(blockHash)

		// Step 1: Leader proposes.
		err := h.engines[leaderIdx].ProcessEvent(ConsensusEvent{
			Type: EventBlockReady,
			Hash: blockHash,
		})
		if err != nil {
			// Leader might be at wrong view; trigger timeout.
			h.runTimeoutViewChange(currentView)
			continue
		}

		leaderOutputs := drainOutputs(h.channels[leaderIdx])

		// Find the Proposal.
		var proposal *Proposal
		for _, o := range leaderOutputs {
			if o.Type == OutputBroadcast && o.Message != nil && o.Message.Type == MsgProposal {
				proposal = o.Message.Payload.(*Proposal)
			}
		}
		if proposal == nil {
			h.runTimeoutViewChange(currentView)
			continue
		}

		// Route Proposal with packet drop.
		for i := 0; i < n; i++ {
			if i == leaderIdx {
				continue
			}
			if rng.Float64() < 0.2 {
				continue // Drop this packet.
			}
			_ = h.engines[i].ProcessEvent(ConsensusEvent{
				Type: EventMessage,
				Msg:  ConsensusMsg{Type: MsgProposal, Payload: proposal},
			})
		}

		// Collect and route votes with packet drop.
		for i := 0; i < n; i++ {
			if i == leaderIdx {
				continue
			}
			outputs := drainOutputs(h.channels[i])
			for _, o := range outputs {
				if o.Type == OutputSendToValidator && o.Message != nil && o.Message.Type == MsgVote {
					if rng.Float64() < 0.2 {
						continue // Drop vote.
					}
					_ = h.engines[leaderIdx].ProcessEvent(ConsensusEvent{
						Type: EventMessage,
						Msg:  *o.Message,
					})
				}
			}
		}

		// Check if PrepareQC formed.
		leaderOutputs = drainOutputs(h.channels[leaderIdx])
		var prepareQCMsg *PrepareQCMsg
		for _, o := range leaderOutputs {
			if o.Type == OutputBroadcast && o.Message != nil && o.Message.Type == MsgPrepareQC {
				prepareQCMsg = o.Message.Payload.(*PrepareQCMsg)
			}
		}

		if prepareQCMsg == nil {
			// Not enough votes. Timeout and continue.
			h.runTimeoutViewChange(currentView)
			continue
		}

		// Route PrepareQCMsg with packet drop.
		for i := 0; i < n; i++ {
			if i == leaderIdx {
				continue
			}
			if rng.Float64() < 0.2 {
				continue
			}
			_ = h.engines[i].ProcessEvent(ConsensusEvent{
				Type: EventMessage,
				Msg:  ConsensusMsg{Type: MsgPrepareQC, Payload: prepareQCMsg},
			})
		}

		// Collect and route CommitVotes with packet drop.
		for i := 0; i < n; i++ {
			if i == leaderIdx {
				continue
			}
			outputs := drainOutputs(h.channels[i])
			for _, o := range outputs {
				if o.Type == OutputSendToValidator && o.Message != nil && o.Message.Type == MsgCommitVote {
					if rng.Float64() < 0.2 {
						continue
					}
					_ = h.engines[leaderIdx].ProcessEvent(ConsensusEvent{
						Type: EventMessage,
						Msg:  *o.Message,
					})
				}
			}
		}

		// Check for Decide and equivocations.
		leaderOutputs = drainOutputs(h.channels[leaderIdx])
		var decide *Decide
		for _, o := range leaderOutputs {
			if o.Type == OutputBroadcast && o.Message != nil && o.Message.Type == MsgDecide {
				decide = o.Message.Payload.(*Decide)
			}
			if o.Type == OutputBlockCommitted {
				h.committed = append(h.committed, struct {
					view ViewNumber
					hash types.Hash
				}{view: o.View, hash: o.Hash})
			}
			if o.Type == OutputEquivocationDetected {
				equivocations++
			}
		}

		if decide == nil {
			h.runTimeoutViewChange(currentView)
			continue
		}

		// Route Decide to non-leaders (with packet drop).
		for i := 0; i < n; i++ {
			if i == leaderIdx {
				continue
			}
			if rng.Float64() < 0.2 {
				continue
			}
			_ = h.engines[i].ProcessEvent(ConsensusEvent{
				Type: EventMessage,
				Msg:  ConsensusMsg{Type: MsgDecide, Payload: decide},
			})
		}

		// Drain remaining outputs from all nodes, check for equivocations.
		allOutputs := h.drainAll()
		for _, outputs := range allOutputs {
			for _, o := range outputs {
				if o.Type == OutputEquivocationDetected {
					equivocations++
				}
			}
		}

		// If some nodes didn't get the Decide, they need to be brought up to date.
		// The next round's leader may timeout; that's fine.
	}

	// Count total commits (from the tracked committed list).
	successfulCommits := len(h.committed) - initialCommits
	t.Logf("packet drop test: %d successful commits out of 20 rounds (3 initial + %d chaos)",
		successfulCommits, successfulCommits)

	if successfulCommits < 5 {
		t.Fatalf("expected at least 5 commits in 20 chaos rounds, got %d", successfulCommits)
	}

	if equivocations > 0 {
		t.Fatalf("detected %d equivocations, expected 0", equivocations)
	}
}

// buildTestTC builds a valid TimeoutCertificate for `view`, signed by a quorum
// of validators, embedding a real high_qc at `highQCView` (0 for the genesis
// QC). It is the only way to legitimately justify a view jump — a single node
// cannot forge it.
func buildTestTC(t *testing.T, setup *testSetup, view, highQCView ViewNumber) *TimeoutCertificate {
	t.Helper()
	n := setup.vs.Len()
	q := setup.vs.QuorumSize()

	highQC := GenesisQC()
	if highQCView > 0 {
		hash := types.Hash{byte(highQCView)}
		vc := NewVoteCollector(highQCView, hash, n)
		for i := 0; i < q; i++ {
			_ = vc.AddVote(ValidatorIndex(i), setup.keys[i].Sign(SigningMessage(highQCView, hash)))
		}
		built, err := vc.BuildQC(setup.vs)
		if err != nil {
			t.Fatalf("buildTestTC high_qc(view=%d): %v", highQCView, err)
		}
		highQC = *built
	}

	tc := NewTimeoutCollector(view, n)
	msg := TimeoutSigningMessage(view)
	for i := 0; i < q; i++ {
		if err := tc.AddVerifiedTimeout(ValidatorIndex(i), setup.keys[i].Sign(msg), highQC); err != nil {
			t.Fatalf("buildTestTC timeout(%d): %v", i, err)
		}
	}
	built, err := tc.BuildTC(setup.vs)
	if err != nil {
		t.Fatalf("buildTestTC: %v", err)
	}
	return built
}

// TestLaggingNodeCatchesUpViaTC is the legitimate-catch-up counterpart to the
// H2 fix: a validator far behind adopts a HIGHER view the instant it sees a
// message carrying a valid TimeoutCertificate (a quorum proof the network
// reached that view), via processEmbeddedTC — no raw single-timeout jump
// required. This is how real scattered/recovered nodes converge, and it keeps
// working after the raw future-view jump is removed.
func TestLaggingNodeCatchesUpViaTC(t *testing.T) {
	setup := newTestSetup(t, 7)
	engine, _ := newTestEngine(t, setup, 0) // starts at view 1
	if engine.CurrentView() != 1 {
		t.Fatalf("expected starting view 1, got %d", engine.CurrentView())
	}

	// The network genuinely reached view 21: it holds a TC for view 20.
	tc := buildTestTC(t, setup, 20, 0)

	// Any message from the network piggybacks that TC. Deliver a timeout for
	// view 21 carrying it; the lagging node must jump to view 21 (= tc.View+1).
	netTimeout := &TimeoutMessage{
		View:      21,
		HighQC:    GenesisQC(),
		Sender:    1,
		Signature: setup.keys[1].Sign(TimeoutSigningMessage(21)).Marshal(),
		HighTC:    tc,
	}
	if err := engine.ProcessEvent(ConsensusEvent{
		Type: EventMessage,
		Msg:  ConsensusMsg{Type: MsgTimeout, Payload: netTimeout},
	}); err != nil {
		t.Fatalf("process: %v", err)
	}
	if got := engine.CurrentView(); got != 21 {
		t.Fatalf("lagging node did not catch up via TC: view = %d, want 21", got)
	}
}

// TestRawFutureTimeoutDoesNotAdvanceView is the H2 regression: a single
// validator's timeout for a far-future view, carrying NO justifying TC, must
// NOT advance a healthy node's view or make it broadcast its own timeout there.
// Otherwise one Byzantine validator could sign timeout(view+k) in a loop and
// drag the whole fleet forward forever, stalling liveness within the f
// tolerance.
func TestRawFutureTimeoutDoesNotAdvanceView(t *testing.T) {
	setup := newTestSetup(t, 7)
	engine, outputCh := newTestEngine(t, setup, 0)
	startView := engine.CurrentView()

	byzTimeout := &TimeoutMessage{
		View:      startView + 40, // within FutureViewWindow, but unjustified
		HighQC:    GenesisQC(),
		Sender:    1,
		Signature: setup.keys[1].Sign(TimeoutSigningMessage(startView + 40)).Marshal(),
		HighTC:    nil, // no quorum proof
	}
	if err := engine.ProcessEvent(ConsensusEvent{
		Type: EventMessage,
		Msg:  ConsensusMsg{Type: MsgTimeout, Payload: byzTimeout},
	}); err != nil {
		t.Fatalf("process: %v", err)
	}
	if got := engine.CurrentView(); got != startView {
		t.Fatalf("raw future timeout advanced the view %d -> %d (H2 not closed)", startView, got)
	}
	for _, o := range drainOutputs(outputCh) {
		if o.Type == OutputBroadcast && o.Message != nil && o.Message.Type == MsgTimeout {
			t.Fatal("raw future timeout triggered a self-broadcast (H2 amplification)")
		}
	}
}

// TestFPlusOneFutureTimeoutsRecoverScatteredViews covers the restart state
// observed by the seven-node fleet: clean rolling shutdowns can persist nearby
// but different views without leaving any one node a complete TC. Distinct
// verified reports from f+1 validators must coalesce the lagging node on the
// highest bounded reported view, while fewer reports (including repeats from
// one sender) must not move it.
func TestFPlusOneFutureTimeoutsRecoverScatteredViews(t *testing.T) {
	setup := newTestSetup(t, 7) // f=2, weak-synchronizer threshold=3
	engine, outputCh := newTestEngine(t, setup, 0)
	startView := engine.CurrentView()

	deliver := func(sender int, view ViewNumber) {
		t.Helper()
		tm := &TimeoutMessage{
			View:      view,
			HighQC:    GenesisQC(),
			Sender:    ValidatorIndex(sender),
			Signature: setup.keys[sender].Sign(TimeoutSigningMessage(view)).Marshal(),
		}
		if err := engine.ProcessEvent(ConsensusEvent{
			Type: EventMessage,
			Msg:  ConsensusMsg{Type: MsgTimeout, Payload: tm},
		}); err != nil {
			t.Fatalf("deliver sender=%d view=%d: %v", sender, view, err)
		}
	}

	deliver(1, startView+4)
	deliver(1, startView+6) // same validator still counts only once
	deliver(2, startView+5)
	if got := engine.CurrentView(); got != startView {
		t.Fatalf("fewer than f+1 validators advanced view: got %d, want %d", got, startView)
	}
	if outputs := drainOutputs(outputCh); len(outputs) != 0 {
		t.Fatalf("fewer than f+1 validators emitted %d outputs", len(outputs))
	}

	// Reports are now [v+6, v+5, v+4], so recovery coalesces on their highest
	// bounded view. The two Byzantine validators cannot reach this branch alone.
	deliver(3, startView+4)
	targetView := startView + 6
	if got := engine.CurrentView(); got != targetView {
		t.Fatalf("f+1 recovery view = %d, want %d", got, targetView)
	}
	if phase := engine.CurrentPhase(); phase != PhaseTimedOut {
		t.Fatalf("recovery phase = %s, want TimedOut", phase)
	}

	foundOwnTimeout := false
	for _, output := range drainOutputs(outputCh) {
		if output.Type != OutputBroadcast || output.Message == nil || output.Message.Type != MsgTimeout {
			continue
		}
		tm := output.Message.Payload.(*TimeoutMessage)
		if tm.Sender == 0 && tm.View == targetView {
			foundOwnTimeout = true
		}
	}
	if !foundOwnTimeout {
		t.Fatalf("f+1 recovery did not broadcast own timeout for view %d", targetView)
	}

	// The latest retained target report (validator 1) plus our own timeout seed
	// two of the five quorum slots. Three more target timeouts must form a TC
	// and advance, proving the weak jump can finish through the strong path.
	deliver(4, targetView)
	if got := engine.CurrentView(); got != targetView {
		t.Fatalf("advanced before timeout quorum: got %d, want %d", got, targetView)
	}
	deliver(5, targetView)
	if got := engine.CurrentView(); got != targetView {
		t.Fatalf("advanced before timeout quorum: got %d, want %d", got, targetView)
	}
	deliver(6, targetView)
	if got := engine.CurrentView(); got != targetView+1 {
		t.Fatalf("timeout quorum did not advance: got %d, want %d", got, targetView+1)
	}
}

// TestChaos7Node_ConvergesFromScatteredRecoveredViews reproduces the exact
// production shape that motivated the weak synchronizer: all validators shut
// down cleanly during a rolling update, but persist nearby different views and
// restart without any process retaining a complete TC.
func TestChaos7Node_ConvergesFromScatteredRecoveredViews(t *testing.T) {
	setup := newTestSetup(t, 7)
	views := []ViewNumber{100, 100, 100, 101, 102, 103, 104}
	h := &chaosHarness{
		t:        t,
		setup:    setup,
		engines:  make([]*ConsensusEngine, len(views)),
		channels: make([]chan EngineOutput, len(views)),
	}
	for i, view := range views {
		h.channels[i] = make(chan EngineOutput, 256)
		h.engines[i] = WithRecoveredState(
			ValidatorIndex(i), setup.keys[i], NewEpochManager(setup.vs),
			1000, 10000, h.channels[i], view,
			GenesisQC(), GenesisQC(), 0,
		)
	}

	// Relay timeout and NewView broadcasts until all nodes coalesce. Outputs
	// emitted while processing a message are drained by the next relay pass.
	converged := false
	for iteration := 0; iteration < 20 && !converged; iteration++ {
		for i, engine := range h.engines {
			if err := timeoutNow(engine); err != nil {
				t.Fatalf("iteration %d node %d timeout: %v", iteration, i, err)
			}
		}
		for relayPass := 0; relayPass < 20; relayPass++ {
			allOutputs := h.drainAll()
			delivered := 0
			for _, outputs := range allOutputs {
				for _, output := range outputs {
					if output.Type != OutputBroadcast || output.Message == nil {
						continue
					}
					for i := range h.engines {
						sender, ok := messageSenderIndex(output.Message)
						if ok && ValidatorIndex(i) == sender {
							continue
						}
						if err := h.engines[i].ProcessEvent(ConsensusEvent{
							Type: EventMessage,
							Msg:  *output.Message,
						}); err != nil {
							t.Fatalf("iteration %d relay pass %d node %d: %v", iteration, relayPass, i, err)
						}
						delivered++
					}
				}
			}
			if delivered == 0 {
				break
			}
		}

		minView, maxView := h.engines[0].CurrentView(), h.engines[0].CurrentView()
		for _, engine := range h.engines[1:] {
			if view := engine.CurrentView(); view < minView {
				minView = view
			} else if view > maxView {
				maxView = view
			}
		}
		converged = minView == maxView
	}
	if !converged {
		got := make([]ViewNumber, len(h.engines))
		for i, engine := range h.engines {
			got[i] = engine.CurrentView()
		}
		t.Fatalf("scattered recovered views did not converge: %v", got)
	}

	// One aligned timeout round puts every engine in the same fresh waiting
	// view. A normal proposal must then commit, proving recovery is not merely
	// numeric convergence.
	view := h.engines[0].CurrentView()
	h.runTimeoutViewChange(view)
	view = h.engines[0].CurrentView()
	for i, engine := range h.engines {
		if got := engine.CurrentView(); got != view {
			t.Fatalf("node %d not aligned after recovery: got %d, want %d", i, got, view)
		}
	}
	blockHash := blockHashForView(view)
	h.markBlockImported(blockHash)
	h.runConsensusRound(view, blockHash)
}

// TestChaos7Node_FastPropose verifies that consensus rounds complete quickly
// when using direct message routing (simulating FastPropose behavior where
// there is no slot boundary wait). Five rounds should complete well under
// 5 seconds since messages are delivered synchronously without network delay.
func TestChaos7Node_FastPropose(t *testing.T) {
	// Create harness with 7 validators.
	h := newChaosHarness(t, 7)

	// Run 5 rounds and measure timing.
	start := time.Now()
	for view := ViewNumber(1); view <= 5; view++ {
		blockHash := crypto.Keccak256Hash([]byte(fmt.Sprintf("fast-block-%d", view)))
		h.markBlockImported(blockHash)
		h.runConsensusRound(view, blockHash)
	}
	elapsed := time.Since(start)

	// With FastPropose (direct routing, no slot boundary delay), 5 rounds
	// should complete well under 5 seconds.
	if elapsed > 5*time.Second {
		t.Errorf("FastPropose: 5 rounds took %v, expected < 5s", elapsed)
	}

	// Verify all 5 committed.
	if len(h.committed) != 5 {
		t.Errorf("committed %d blocks, want 5", len(h.committed))
	}

	// All engines should be at view 6.
	for i, e := range h.engines {
		if v := e.CurrentView(); v != 6 {
			t.Errorf("engine %d at view %d, want 6", i, v)
		}
	}

	t.Logf("FastPropose: 5 rounds completed in %v", elapsed)
}

// TestChaos7Node_50RoundContinuousBlockProduction verifies that 7 nodes
// produce 50 consecutive blocks with correct leader rotation, no equivocations,
// and all nodes staying in sync.
func TestChaos7Node_50RoundContinuousBlockProduction(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping long block production test in -short mode")
	}

	const totalRounds = 50
	const numNodes = 7

	h := newChaosHarness(t, numNodes)
	start := time.Now()

	for view := ViewNumber(1); view <= totalRounds; view++ {
		blockHash := blockHashForView(view)
		expectedLeader := int(view % numNodes)

		// Verify leader rotation is correct.
		leader := LeaderForView(view, h.setup.vs)
		if int(leader) != expectedLeader {
			t.Fatalf("view %d: expected leader %d, got %d", view, expectedLeader, leader)
		}

		h.markBlockImported(blockHash)
		h.runConsensusRound(view, blockHash)
	}

	elapsed := time.Since(start)

	// Verify all 50 blocks were committed.
	if len(h.committed) < totalRounds {
		t.Fatalf("expected %d committed blocks, got %d", totalRounds, len(h.committed))
	}

	// Verify commits are sequential (view 1..50).
	for i, c := range h.committed {
		expectedView := ViewNumber(i + 1)
		if c.view != expectedView {
			t.Fatalf("commit %d: expected view %d, got %d", i, expectedView, c.view)
		}
		expectedHash := blockHashForView(expectedView)
		if c.hash != expectedHash {
			t.Fatalf("commit %d: hash mismatch at view %d", i, expectedView)
		}
	}

	// Verify all engines are at view 51 (ready for next block).
	for i, e := range h.engines {
		expected := ViewNumber(totalRounds + 1)
		if v := e.CurrentView(); v != expected {
			t.Errorf("engine %d at view %d, want %d", i, v, expected)
		}
	}

	// Verify leader distribution: each node should have been leader ~7 times.
	leaderCounts := make(map[int]int)
	for view := ViewNumber(1); view <= totalRounds; view++ {
		leaderCounts[int(view%numNodes)]++
	}
	for i := 0; i < numNodes; i++ {
		if leaderCounts[i] < 5 || leaderCounts[i] > 10 {
			t.Errorf("node %d was leader %d times (expected ~7)", i, leaderCounts[i])
		}
	}

	t.Logf("50-round continuous block production: %d commits in %v (avg %v/block)",
		len(h.committed), elapsed, elapsed/time.Duration(totalRounds))
}

// TestFPlusOneFutureTimeoutsWithNonGenesisHighQC covers the embedded-QC
// verification path the scattered-recovery tests skip by using GenesisQC
// (view 0, which verifyEmbeddedQC exempts): f+1 future timeouts each carrying a
// REAL non-genesis HighQC must be accepted and drive the weak-synchronizer
// jump, and a timeout whose embedded QC is forged must be rejected without
// contributing to the f+1 count.
func TestFPlusOneFutureTimeoutsWithNonGenesisHighQC(t *testing.T) {
	setup := newTestSetup(t, 7) // f=2, threshold=3
	engine, _ := newTestEngine(t, setup, 0)
	startView := engine.CurrentView()

	// A genuine QC at view 2 (non-genesis) that all timeouts piggyback.
	realQC := buildRealQC(t, setup, 2, types.Hash{0x9})

	deliver := func(sender int, view ViewNumber, qc *QuorumCertificate) error {
		tm := &TimeoutMessage{
			View:      view,
			HighQC:    *qc,
			Sender:    ValidatorIndex(sender),
			Signature: setup.keys[sender].Sign(TimeoutSigningMessage(view)).Marshal(),
		}
		return engine.ProcessEvent(ConsensusEvent{
			Type: EventMessage,
			Msg:  ConsensusMsg{Type: MsgTimeout, Payload: tm},
		})
	}

	// A forged embedded QC must be rejected and must NOT count toward f+1.
	forged := *realQC
	forged.AggregateSignature = append([]byte(nil), realQC.AggregateSignature...)
	forged.AggregateSignature[0] ^= 0xFF
	if err := deliver(1, startView+3, &forged); err == nil {
		t.Fatal("timeout carrying a forged embedded QC should be rejected")
	}
	if got := engine.CurrentView(); got != startView {
		t.Fatalf("forged-QC timeout advanced the view: got %d", got)
	}

	// f+1 valid non-genesis-QC reports must drive the recovery jump.
	if err := deliver(1, startView+3, realQC); err != nil {
		t.Fatalf("deliver 1: %v", err)
	}
	if err := deliver(2, startView+5, realQC); err != nil {
		t.Fatalf("deliver 2: %v", err)
	}
	if got := engine.CurrentView(); got != startView {
		t.Fatalf("advanced before f+1: got %d", got)
	}
	if err := deliver(3, startView+4, realQC); err != nil {
		t.Fatalf("deliver 3: %v", err)
	}
	if got := engine.CurrentView(); got != startView+5 { // max of {3,5,4}
		t.Fatalf("f+1 non-genesis-QC recovery view = %d, want %d", got, startView+5)
	}
	if phase := engine.CurrentPhase(); phase != PhaseTimedOut {
		t.Fatalf("recovery phase = %s, want TimedOut", phase)
	}
}
