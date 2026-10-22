package sharedexec

import (
	"context"
	"errors"

	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
)

var errCertifiedFreshCommitPanic = errors.New("certified fresh commit panicked")

// A synchronous creator may share only its still-in-flight successful fresh
// commit with callers of the identical complete request and QC. No completed
// observation is retained; canonical duplicates keep their physical read path.
type certifiedWriteJob struct {
	done         chan struct{}
	result       CommitResult
	success      bool
	err          error
	transactions int
}

func (s *Server) certifiedFreshCommit(ctx context.Context, key certifiedReadKey, create bool) (*certifiedWriteJob, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ctx.Err(); err != nil {
		return nil, false, err
	}
	if job := s.certifiedWrites[key]; job != nil {
		s.certifiedWriteJoins.Add(1)
		return job, false, nil
	}
	if !create || len(s.certifiedWrites) >= s.capacity {
		return nil, false, nil
	}
	if s.certifiedWrites == nil {
		s.certifiedWrites = make(map[certifiedReadKey]*certifiedWriteJob)
	}
	job := &certifiedWriteJob{done: make(chan struct{})}
	s.certifiedWrites[key] = job
	s.certifiedWriteFlights.Add(1)
	return job, true, nil
}

// The success caller holds execute until after reconciliation and publication.
// The identity guard makes every pre-execute/error/panic retirement idempotent.
func (s *Server) finishCertifiedFreshCommit(ctx context.Context, key certifiedReadKey, job *certifiedWriteJob, result CommitResult, err error, fresh bool, transactions int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.certifiedWrites[key] != job {
		return
	}
	delete(s.certifiedWrites, key)
	job.err = err
	job.success = fresh && err == nil && result.Committed && ctx.Err() == nil && s.ctx.Err() == nil && !s.closed
	if job.success {
		job.result, job.transactions = result, transactions
	}
	close(job.done)
}

func (s *Server) waitCertifiedFreshCommit(ctx context.Context, request CommitRequest, certificate *hotstuff.QuorumCertificate, phases *ownerOperationPhases, job *certifiedWriteJob) (CommitResult, error) {
	phases.certifiedWriteJoined = true
	phases.phase("certified_fresh_singleflight_wait")
	if err := ctx.Err(); err != nil {
		return CommitResult{}, err
	}
	if err := s.ctx.Err(); err != nil {
		return CommitResult{}, err
	}
	select {
	case <-job.done:
		if err := ctx.Err(); err != nil {
			return CommitResult{}, err
		}
		if err := s.ctx.Err(); err != nil {
			return CommitResult{}, err
		}
		if job.success {
			phases.duplicate, phases.transactions = true, job.transactions
			return job.result, nil
		}
		// No observation survives a failed or rerouted creator. Exactly one
		// original exclusive classification/verification/repair is permitted;
		// neither fresh coalescing nor the shared read path participates.
		result, _, err := s.commitExclusiveAttempt(ctx, request, certificate, phases, false)
		return result, err
	case <-ctx.Done():
		return CommitResult{}, ctx.Err()
	case <-s.ctx.Done():
		return CommitResult{}, s.ctx.Err()
	}
}

// Counts are created synchronous cohorts and joined callers, not physical
// writes, reads, successful commits, or a measurement-window count.
func (s *Server) CertifiedFreshCommitCounts() (created, joined uint64) {
	return s.certifiedWriteFlights.Load(), s.certifiedWriteJoins.Load()
}
