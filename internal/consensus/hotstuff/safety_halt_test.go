// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"errors"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus"
)

// A conflicting commit from the live Decide path halts the node: it is not
// queued for retry, the leader stops producing, and outbound consensus
// messages are dropped.
func TestConflictingCommitHaltsProductionAndVoting(t *testing.T) {
	s, _, producer := hsTNewOutputService(t)
	incoming := types.HexToHash("0xbb")
	producer.canonErr = &consensus.ConflictingCommitError{
		Target: incoming, TargetNumber: 9, Height: 9,
		Committed: types.HexToHash("0xaa"), Incoming: incoming,
		CommittedHead: types.HexToHash("0xaa"), CommittedHeadNumber: 9,
	}
	before := metricConflictingCommit.Get()

	s.handleOutput(EngineOutput{Type: OutputBlockCommitted, View: 12, Hash: incoming}, time.Time{})

	if !s.SafetyHalted() {
		t.Fatal("node not halted after a conflicting commit")
	}
	if metricConflictingCommit.Get() != before+1 || metricSafetyHalted.Get() != 1 {
		t.Fatal("conflict metrics not raised")
	}
	if _, pending := s.pendingCommits[incoming]; pending {
		t.Fatal("a conflicting commit must not be retried")
	}
	s.triggerBlockProduction(13, types.HexToHash("0xaa"))
	if len(producer.triggered) != 0 {
		t.Fatal("halted node produced a block")
	}
	for _, typ := range []EngineOutputType{OutputBroadcast, OutputSendToValidator, OutputSpeculativeBuild} {
		if !s.haltDropsOutput(EngineOutput{Type: typ, Message: &ConsensusMsg{Type: MsgVote}}) {
			t.Fatalf("output %v not dropped while halted", typ)
		}
	}
	if s.haltDropsOutput(EngineOutput{Type: OutputBlockCommitted}) {
		t.Fatal("inbound bookkeeping must keep running while halted")
	}
}

// An ordinary deferred commit (block not stored yet) is not a halt.
func TestDeferredCommitDoesNotHalt(t *testing.T) {
	s, _, producer := hsTNewOutputService(t)
	producer.canonErr = errors.New("block not stored yet")
	h := types.HexToHash("0xcc")
	s.handleOutput(EngineOutput{Type: OutputBlockCommitted, View: 5, Hash: h}, time.Time{})
	if s.SafetyHalted() {
		t.Fatal("a deferred commit halted the node")
	}
	if _, pending := s.pendingCommits[h]; !pending {
		t.Fatal("a deferred commit must stay queued for retry")
	}
}
