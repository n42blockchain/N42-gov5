package hotstuff

import (
	"errors"

	"github.com/n42blockchain/N42/common/types"
)

// RequestPerformanceProduction bootstraps/retries the current E1 leader through
// the same synchronization, committed-parent and applied-parent gates used by a
// ViewChanged output. It cannot enable production before the durable journal.
func (s *Service) RequestPerformanceProduction() error {
	if !s.performanceReady.Load() || s.certificateSink == nil || s.blockProducer == nil || s.engine == nil || s.engine.chainConfig == nil || s.engine.chainConfig.Performance == nil || s.ctx.Err() != nil {
		return errors.New("performance production service is not ready")
	}
	engine := s.engine.Engine()
	if engine == nil {
		return errors.New("performance production engine unavailable")
	}
	if !engine.IsCurrentLeader() {
		return nil
	}
	view, parent := engine.CurrentView(), engine.LockedQC().BlockHash
	s.triggerBlockProduction(view, parent)
	return nil
}

// NotifyPerformanceBlockSealed publishes a remotely executed candidate only in
// the view for which its local candidate WAL was written. Legacy miner callers
// continue to use NotifyBlockSealed.
func (h *HotStuff) NotifyPerformanceBlockSealed(view ViewNumber, hash, txRoot, parent types.Hash) error {
	if h.chainConfig == nil || h.chainConfig.Performance == nil {
		return errors.New("performance proposal requires explicit performance configuration")
	}
	e := h.Engine()
	if e == nil {
		return errors.New("performance proposal engine unavailable")
	}
	return e.performanceBlockReady(view, hash, txRoot, parent)
}

func (e *ConsensusEngine) performanceBlockReady(view ViewNumber, hash, txRoot, parent types.Hash) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.roundState.CurrentView() != view || !IsLeader(e.myIndex, view, e.validatorSet()) {
		return errors.New("performance proposal view or leader changed")
	}
	if e.roundState.Phase() != PhaseWaitingForProposal {
		// Retrying the same durable candidate after it was already proposed is
		// idempotent. Never emit another proposal or replace the vote binding.
		if (e.roundState.Phase() == PhaseVoting || e.roundState.Phase() == PhasePreCommit || e.roundState.Phase() == PhaseCommitted) &&
			e.roundState.votedInView == view && e.roundState.votedHash == hash {
			return nil
		}
		return errors.New("performance proposal phase changed")
	}
	if actual, known := e.importedParents[hash]; !known || actual != parent {
		return errors.New("performance proposal lacks imported parent binding")
	}
	justify := e.roundState.LockedQC().BlockHash
	if hash == justify || (justify != (types.Hash{}) && parent != justify) {
		return errors.New("performance proposal no longer extends locked QC")
	}
	return e.onBlockReady(hash, txRoot)
}
