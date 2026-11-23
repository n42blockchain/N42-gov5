// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Safety halt on a conflicting commit (sync review S0e / C8, owner decision
// Q1).
//
// The chain refuses a CommitQC whose target does not extend the committed
// chain (internal/commit_conflict.go returns a
// *consensus.ConflictingCommitError and writes nothing). Such a QC can only
// exist after a safety failure: either more than f validators equivocated or
// this node's committed chain is wrong. Following either chain is unsafe, so
// the service logs both sides, raises hotstuff_conflicting_commit_total and
// hotstuff_safety_halted, and stops participating on this chain: no block
// production, and no outbound consensus message (votes, proposals,
// timeouts). Inbound processing continues so the evidence stays observable.
// The halt is in memory; on restart the chain refuses the same commit again
// and the node halts again. An operator decides which chain is correct.
//
// This complements the committed-floor vote rule on the performance line
// (committed_floor.go there), which refuses to VOTE for a block that does
// not extend the committed head: that rule prevents this node from helping
// form a conflicting QC, this halt handles one that formed anyway.

package hotstuff

import (
	"errors"

	prometheus "github.com/n42blockchain/N42/common/metrics"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/log"
)

var (
	metricConflictingCommit = prometheus.GetOrCreateCounter("hotstuff_conflicting_commit_total", false)
	metricSafetyHalted      = prometheus.GetOrCreateCounter("hotstuff_safety_halted", true)
)

// handleCommitConflict reports whether err is a conflicting commit and, if
// so, halts this node's participation. qcView and qcHash describe the
// incoming CommitQC; source names the commit path.
func (s *Service) handleCommitConflict(err error, qcView ViewNumber, qcHash types.Hash, source string) bool {
	var ce *consensus.ConflictingCommitError
	if err == nil || !errors.As(err, &ce) {
		return false
	}
	var lastView ViewNumber
	var lastHash types.Hash
	if s.engine != nil {
		if e := s.engine.Engine(); e != nil {
			last := e.LastCommittedQC()
			lastView, lastHash = last.View, last.BlockHash
		}
	}
	metricConflictingCommit.Inc()
	first := s.safetyHalted.CompareAndSwap(false, true)
	if first {
		metricSafetyHalted.Set(1)
	}
	log.Error("hotstuff: CONFLICTING COMMIT -- halting block production and voting on this chain. "+
		"A verified CommitQC names a block that does not extend this node's committed chain; "+
		"canonical rows were NOT rewritten. Operator action required: determine which chain is "+
		"correct before restarting.",
		"source", source,
		"incomingQCView", uint64(qcView), "incomingQCBlock", qcHash, "incomingTargetNumber", ce.TargetNumber,
		"conflictHeight", ce.Height, "committedHashAtHeight", ce.Committed, "incomingHashAtHeight", ce.Incoming,
		"localCommittedQCView", uint64(lastView), "localCommittedQCBlock", lastHash,
		"committedHead", ce.CommittedHead, "committedHeadNumber", ce.CommittedHeadNumber,
		"firstHalt", first)
	return true
}

// SafetyHalted reports whether a conflicting commit halted this node.
func (s *Service) SafetyHalted() bool { return s.safetyHalted.Load() }

// haltDropsOutput reports whether output must be dropped because the node is
// halted: every outbound consensus message, and speculative builds.
func (s *Service) haltDropsOutput(output EngineOutput) bool {
	if !s.safetyHalted.Load() {
		return false
	}
	switch output.Type {
	case OutputBroadcast, OutputSendToValidator, OutputSpeculativeBuild:
		return true
	}
	return false
}
