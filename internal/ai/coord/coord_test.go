// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package coord

import (
	"errors"
	"testing"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
)

// ---------- AgentRegistry ----------

func TestAgentRegistry(t *testing.T) {
	reg := NewAgentRegistry(100)
	stake := new(uint256.Int).Mul(uint256.NewInt(2), uint256.NewInt(1_000_000_000_000_000_000)) // 2 ETH

	err := reg.RegisterAgent("did:n42:agent1", []string{"text-gen", "embedding"}, "http://localhost:8080", stake)
	if err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}
	if reg.Count() != 1 {
		t.Fatalf("count = %d, want 1", reg.Count())
	}

	// Find by capability.
	agents := reg.FindByCapability("text-gen")
	if len(agents) != 1 {
		t.Fatalf("found %d agents with text-gen, want 1", len(agents))
	}
	if agents[0].DID != "did:n42:agent1" {
		t.Fatalf("DID = %q, want did:n42:agent1", agents[0].DID)
	}

	// Update reputation.
	if err := reg.UpdateReputation("did:n42:agent1", 10.0); err != nil {
		t.Fatalf("UpdateReputation: %v", err)
	}
	info, _ := reg.GetAgent("did:n42:agent1")
	if info.Reputation != 60.0 {
		t.Fatalf("reputation = %f, want 60.0", info.Reputation)
	}

	// Clamping.
	if err := reg.UpdateReputation("did:n42:agent1", 100.0); err != nil {
		t.Fatalf("UpdateReputation: %v", err)
	}
	info, _ = reg.GetAgent("did:n42:agent1")
	if info.Reputation != 100.0 {
		t.Fatalf("reputation = %f, want 100.0 (clamped)", info.Reputation)
	}

	// Insufficient stake.
	err = reg.RegisterAgent("did:n42:agent2", []string{"a"}, "http://x", uint256.NewInt(1))
	if !errors.Is(err, ErrInsufficientStake) {
		t.Fatalf("expected ErrInsufficientStake, got %v", err)
	}
}

func TestRegistryUnregister(t *testing.T) {
	reg := NewAgentRegistry(100)
	stake := new(uint256.Int).Mul(uint256.NewInt(2), uint256.NewInt(1_000_000_000_000_000_000))

	err := reg.RegisterAgent("did:n42:unreg1", []string{"cap-a", "cap-b"}, "http://localhost:1", stake)
	if err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}

	// Verify agent exists.
	if reg.Count() != 1 {
		t.Fatalf("count = %d, want 1", reg.Count())
	}

	// Unregister.
	if err := reg.UnregisterAgent("did:n42:unreg1"); err != nil {
		t.Fatalf("UnregisterAgent: %v", err)
	}

	// Count should be 0.
	if reg.Count() != 0 {
		t.Fatalf("count after unregister = %d, want 0", reg.Count())
	}

	// FindByCapability should return empty.
	agents := reg.FindByCapability("cap-a")
	if len(agents) != 0 {
		t.Fatalf("FindByCapability returned %d agents, want 0", len(agents))
	}
	agents = reg.FindByCapability("cap-b")
	if len(agents) != 0 {
		t.Fatalf("FindByCapability for cap-b returned %d agents, want 0", len(agents))
	}

	// GetAgent should return error.
	_, err = reg.GetAgent("did:n42:unreg1")
	if !errors.Is(err, ErrAgentNotFound) {
		t.Fatalf("expected ErrAgentNotFound, got %v", err)
	}

	// Unregistering again should fail.
	err = reg.UnregisterAgent("did:n42:unreg1")
	if !errors.Is(err, ErrAgentNotFound) {
		t.Fatalf("expected ErrAgentNotFound on double unregister, got %v", err)
	}
}

func TestRegistryRecordTasks(t *testing.T) {
	reg := NewAgentRegistry(100)
	stake := new(uint256.Int).Mul(uint256.NewInt(2), uint256.NewInt(1_000_000_000_000_000_000))

	err := reg.RegisterAgent("did:n42:tasks1", []string{"compute"}, "http://localhost:2", stake)
	if err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}

	// Record 3 completions and 2 failures.
	reg.RecordTaskComplete("did:n42:tasks1")
	reg.RecordTaskComplete("did:n42:tasks1")
	reg.RecordTaskComplete("did:n42:tasks1")
	reg.RecordTaskFailed("did:n42:tasks1")
	reg.RecordTaskFailed("did:n42:tasks1")

	info, err := reg.GetAgent("did:n42:tasks1")
	if err != nil {
		t.Fatalf("GetAgent: %v", err)
	}
	if info.TasksCompleted != 3 {
		t.Fatalf("TasksCompleted = %d, want 3", info.TasksCompleted)
	}
	if info.TasksFailed != 2 {
		t.Fatalf("TasksFailed = %d, want 2", info.TasksFailed)
	}
}

// ---------- TaskNegotiation ----------

func TestTaskNegotiation(t *testing.T) {
	tn := NewTaskNegotiation(1 * time.Hour)

	budget := uint256.NewInt(1000)
	spec := TaskSpec{
		Description: "classify images",
		Capability:  "image-classify",
		MaxBudget:   budget,
		Deadline:    time.Now().Add(2 * time.Hour),
	}

	negID, err := tn.RequestTask("did:requester", spec)
	if err != nil {
		t.Fatalf("RequestTask: %v", err)
	}

	// Bid.
	bidID, err := tn.BidOnTask("did:provider", negID, uint256.NewInt(500), 30*time.Minute)
	if err != nil {
		t.Fatalf("BidOnTask: %v", err)
	}

	neg, _ := tn.GetNegotiation(negID)
	if neg.Status != NegotiationBidding {
		t.Fatalf("status = %v, want bidding", neg.Status)
	}

	// Accept.
	if err := tn.AcceptBid("did:requester", bidID); err != nil {
		t.Fatalf("AcceptBid: %v", err)
	}
	neg, _ = tn.GetNegotiation(negID)
	if neg.Status != NegotiationAccepted {
		t.Fatalf("status = %v, want accepted", neg.Status)
	}
	if neg.Escrow.Cmp(uint256.NewInt(500)) != 0 {
		t.Fatalf("escrow = %s, want 500", neg.Escrow.String())
	}

	// Complete.
	resultCAS := types.HexToHash("0xdeadbeef")
	if err := tn.CompleteTask("did:provider", negID, resultCAS); err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}
	neg, _ = tn.GetNegotiation(negID)
	if neg.Status != NegotiationCompleted {
		t.Fatalf("status = %v, want completed", neg.Status)
	}
}

func TestNegotiationDisputeTask(t *testing.T) {
	tn := NewTaskNegotiation(1 * time.Hour)

	spec := TaskSpec{
		Description: "analyze data",
		Capability:  "data-analysis",
		MaxBudget:   uint256.NewInt(1000),
		Deadline:    time.Now().Add(2 * time.Hour),
	}

	negID, err := tn.RequestTask("did:requester", spec)
	if err != nil {
		t.Fatalf("RequestTask: %v", err)
	}

	bidID, err := tn.BidOnTask("did:provider", negID, uint256.NewInt(500), 30*time.Minute)
	if err != nil {
		t.Fatalf("BidOnTask: %v", err)
	}

	if err := tn.AcceptBid("did:requester", bidID); err != nil {
		t.Fatalf("AcceptBid: %v", err)
	}

	// Dispute.
	if err := tn.DisputeTask("did:requester", negID, "result quality too low"); err != nil {
		t.Fatalf("DisputeTask: %v", err)
	}

	neg, _ := tn.GetNegotiation(negID)
	if neg.Status != NegotiationDisputed {
		t.Fatalf("status = %v, want disputed", neg.Status)
	}
	if neg.DisputeReason != "result quality too low" {
		t.Fatalf("dispute reason = %q, want %q", neg.DisputeReason, "result quality too low")
	}
}

func TestNegotiationCancelTask(t *testing.T) {
	tn := NewTaskNegotiation(1 * time.Hour)

	spec := TaskSpec{
		Description: "some task",
		Capability:  "generic",
		MaxBudget:   uint256.NewInt(500),
		Deadline:    time.Now().Add(1 * time.Hour),
	}

	negID, err := tn.RequestTask("did:requester", spec)
	if err != nil {
		t.Fatalf("RequestTask: %v", err)
	}

	// Cancel while Open.
	if err := tn.CancelTask("did:requester", negID); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}

	neg, _ := tn.GetNegotiation(negID)
	if neg.Status != NegotiationCancelled {
		t.Fatalf("status = %v, want cancelled", neg.Status)
	}

	// Cancelling again should fail (not open/bidding).
	err = tn.CancelTask("did:requester", negID)
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("expected ErrInvalidTransition on double cancel, got %v", err)
	}
}

func TestNegotiationPendingNegotiations(t *testing.T) {
	tn := NewTaskNegotiation(1 * time.Hour)

	makeSpec := func(cap string) TaskSpec {
		return TaskSpec{
			Description: cap,
			Capability:  cap,
			MaxBudget:   uint256.NewInt(1000),
			Deadline:    time.Now().Add(2 * time.Hour),
		}
	}

	// Create 4 negotiations.
	// neg1: Open
	neg1ID, _ := tn.RequestTask("did:r1", makeSpec("cap1"))

	// neg2: Bidding (has a bid)
	neg2ID, _ := tn.RequestTask("did:r2", makeSpec("cap2"))
	_, _ = tn.BidOnTask("did:p1", neg2ID, uint256.NewInt(100), 10*time.Minute)

	// neg3: Accepted (not pending)
	neg3ID, _ := tn.RequestTask("did:r3", makeSpec("cap3"))
	bidID3, _ := tn.BidOnTask("did:p2", neg3ID, uint256.NewInt(200), 15*time.Minute)
	_ = tn.AcceptBid("did:r3", bidID3)

	// neg4: Cancelled (not pending)
	neg4ID, _ := tn.RequestTask("did:r4", makeSpec("cap4"))
	_ = tn.CancelTask("did:r4", neg4ID)

	pending := tn.PendingNegotiations()
	if len(pending) != 2 {
		t.Fatalf("PendingNegotiations returned %d, want 2", len(pending))
	}

	// Verify that the pending ones are neg1 and neg2 (order not guaranteed).
	ids := make(map[types.Hash]bool)
	for _, p := range pending {
		ids[p.ID] = true
	}
	if !ids[neg1ID] {
		t.Fatalf("expected neg1 (%s) in pending results", neg1ID.Hex())
	}
	if !ids[neg2ID] {
		t.Fatalf("expected neg2 (%s) in pending results", neg2ID.Hex())
	}
}

func TestNegotiationExpiredCleanup(t *testing.T) {
	// Use a very short timeout so negotiations expire quickly.
	tn := NewTaskNegotiation(1 * time.Millisecond)

	spec := TaskSpec{
		Description: "expire me",
		Capability:  "ephemeral",
		MaxBudget:   uint256.NewInt(100),
	}

	negID, err := tn.RequestTask("did:r1", spec)
	if err != nil {
		t.Fatalf("RequestTask: %v", err)
	}

	// Sleep to let the negotiation expire.
	time.Sleep(5 * time.Millisecond)

	// Creating a new negotiation triggers cleanExpired inside RequestTask.
	_, err = tn.RequestTask("did:r2", TaskSpec{
		Description: "after cleanup",
		Capability:  "fresh",
		MaxBudget:   uint256.NewInt(200),
	})
	if err != nil {
		t.Fatalf("RequestTask 2: %v", err)
	}

	// The first negotiation should now be cancelled.
	neg, err := tn.GetNegotiation(negID)
	if err != nil {
		t.Fatalf("GetNegotiation: %v", err)
	}
	if neg.Status != NegotiationCancelled {
		t.Fatalf("expired negotiation status = %v, want cancelled", neg.Status)
	}
}

func TestNegotiationDeadlineInPast(t *testing.T) {
	tn := NewTaskNegotiation(1 * time.Hour)

	spec := TaskSpec{
		Description: "past deadline",
		Capability:  "test",
		MaxBudget:   uint256.NewInt(100),
		Deadline:    time.Now().Add(-1 * time.Second),
	}

	_, err := tn.RequestTask("did:r1", spec)
	if !errors.Is(err, ErrDeadlineInPast) {
		t.Fatalf("expected ErrDeadlineInPast, got %v", err)
	}
}

// ---------- ReputationSystem ----------

func TestReputationSystem(t *testing.T) {
	rs := NewReputationSystem(0.95)

	// Unknown agent gets default score.
	if score := rs.Score("did:unknown"); score != 50.0 {
		t.Fatalf("unknown score = %f, want 50.0", score)
	}

	// Record completions.
	rs.RecordCompletion("did:agent1", 30*time.Second)
	rs.RecordCompletion("did:agent1", 60*time.Second)

	details := rs.GetDetails("did:agent1")
	if details == nil {
		t.Fatal("expected details for did:agent1")
	}
	if details.Completions != 2 {
		t.Fatalf("completions = %d, want 2", details.Completions)
	}
	if details.CompletionRate != 1.0 {
		t.Fatalf("completion rate = %f, want 1.0", details.CompletionRate)
	}

	// Record failure to reduce completion rate.
	rs.RecordFailure("did:agent1")
	details = rs.GetDetails("did:agent1")
	if details.TotalTasks != 3 {
		t.Fatalf("total tasks = %d, want 3", details.TotalTasks)
	}
	expectedRate := 2.0 / 3.0
	if diff := details.CompletionRate - expectedRate; diff > 0.01 || diff < -0.01 {
		t.Fatalf("completion rate = %f, want ~%f", details.CompletionRate, expectedRate)
	}

	// Score should decrease after failure.
	score := rs.Score("did:agent1")
	if score >= 100.0 || score <= 0 {
		t.Fatalf("score = %f, should be in (0, 100)", score)
	}

	// Apply decay.
	scoreBefore := rs.Score("did:agent1")
	rs.ApplyDecay()
	scoreAfter := rs.Score("did:agent1")
	// Decay moves toward default (50). If score > 50, it should decrease.
	if scoreBefore > 50 && scoreAfter >= scoreBefore {
		t.Fatalf("decay should move score toward 50: before=%f, after=%f", scoreBefore, scoreAfter)
	}
}

func TestReputationGetDetailsUnknown(t *testing.T) {
	rs := NewReputationSystem(0.95)

	details := rs.GetDetails("did:unknown:agent")
	if details != nil {
		t.Fatalf("expected nil for unknown agent, got %+v", details)
	}
}

func TestReputationDecayEffect(t *testing.T) {
	rs := NewReputationSystem(0.90) // 10% decay per epoch

	// Record several completions to push score above default (50).
	for i := 0; i < 10; i++ {
		rs.RecordCompletion("did:decay-test", 10*time.Second)
	}

	scoreBefore := rs.Score("did:decay-test")
	if scoreBefore <= defaultReputation {
		t.Fatalf("score before decay = %f, expected > %f", scoreBefore, defaultReputation)
	}

	// Apply decay. Score should decrease (move toward 50).
	rs.ApplyDecay()
	scoreAfter := rs.Score("did:decay-test")
	if scoreAfter >= scoreBefore {
		t.Fatalf("score should decrease after decay: before=%f, after=%f", scoreBefore, scoreAfter)
	}

	// Apply decay multiple times; score should keep decreasing toward 50.
	for i := 0; i < 10; i++ {
		rs.ApplyDecay()
	}
	scoreFinal := rs.Score("did:decay-test")
	if scoreFinal >= scoreAfter {
		t.Fatalf("score should keep decreasing: after1=%f, afterMany=%f", scoreAfter, scoreFinal)
	}
	// Score should still be above 50 (asymptotically approaches but never crosses).
	if scoreFinal < defaultReputation {
		t.Fatalf("score should remain >= default (50): got %f", scoreFinal)
	}
}
