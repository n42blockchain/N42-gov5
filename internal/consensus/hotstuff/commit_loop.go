// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import "github.com/n42blockchain/N42/log"

// Commit loop.
//
// The output loop is serial: a committed block's canonicalization and state
// persist (handleBlockCommitted) ran inline, and the view change the engine
// emits right after the CommitQC waited behind it -- so a leader's build
// trigger for the next view waited for CommitToCanonical, which takes the
// chain database's single writer and queues behind the lag writer's block
// write. On the S99 leader at 163k-tx blocks that gap (CommitQC to "view
// changed") averaged 18 ms (p90 53 ms, up to 82 ms) of the propose span.
//
// Under the lag gate (deferred depth k > 1) the leader builds without the
// canonical head -- the gate asks only that this node executed block N-k --
// so the commit is handed to a loop of its own and the output loop goes on
// to the view change. Commits keep their order (one loop, FIFO). A commit
// that carries a pending reconfiguration is still waited for, so the
// reconfiguration is marked committed before the next output, as before.
// Without the lag gate everything runs inline as before.

// commitQueueLen bounds the commit loop's queue; a full queue makes the
// output loop wait (the inline behaviour), never drops a commit.
const commitQueueLen = 256

type commitJob struct {
	output EngineOutput
	done   chan struct{} // closed when handled; nil when nobody waits
}

// commitAsync reports whether committed blocks go to the commit loop.
func (s *Service) commitAsync() bool {
	return s.lagGate != nil && s.commitCh != nil
}

// enqueueCommit hands output to the commit loop, waiting for it when a
// reconfiguration is pending.
func (s *Service) enqueueCommit(output EngineOutput) {
	job := commitJob{output: output}
	if ce := s.engine.Engine(); ce != nil {
		if rm := ce.ReconfigManager(); rm != nil && rm.HasPendingChanges() {
			job.done = make(chan struct{})
		}
	}
	select {
	case s.commitCh <- job:
	case <-s.ctx.Done():
		return
	}
	if job.done != nil {
		select {
		case <-job.done:
		case <-s.ctx.Done():
		}
	}
}

// commitLoop handles committed blocks in order.
func (s *Service) commitLoop() {
	defer s.wg.Done()
	for {
		select {
		case <-s.ctx.Done():
			if n := len(s.commitCh); n > 0 {
				log.Info("hotstuff: commit loop stopping with commits queued; they reconcile on restart", "queued", n)
			}
			return
		case job := <-s.commitCh:
			s.handleBlockCommitted(job.output)
			if job.done != nil {
				close(job.done)
			}
		}
	}
}

func (s *Service) setLastPersistedView(v ViewNumber) {
	s.persistViewMu.Lock()
	s.lastPersistedView = v
	s.persistViewMu.Unlock()
}

func (s *Service) getLastPersistedView() ViewNumber {
	s.persistViewMu.Lock()
	defer s.persistViewMu.Unlock()
	return s.lastPersistedView
}
