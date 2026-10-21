package sharedexec

import (
	"context"
	"errors"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/log"
)

var errCertifiedDuplicateReadPanic = errors.New("certified duplicate physical read panicked")

// The complete request and certificate are comparable without hashing or
// normalizing any signer/signature bytes. Only overlapping reads may share it.
type certifiedReadKey struct {
	request   CommitRequest
	view      hotstuff.ViewNumber
	blockHash types.Hash
	signers   [7]bool
	signature [96]byte
}

type certifiedReadJob struct {
	done         chan struct{}
	result       CommitResult
	handled      bool
	err          error
	duplicate    bool
	transactions int
}

func certifiedDuplicateKey(request CommitRequest, certificate *hotstuff.QuorumCertificate) (certifiedReadKey, bool) {
	if certificate == nil || len(certificate.Signers) != 7 || len(certificate.AggregateSignature) != 96 {
		return certifiedReadKey{}, false
	}
	key := certifiedReadKey{request: request, view: certificate.View, blockHash: certificate.BlockHash}
	copy(key.signers[:], certificate.Signers)
	copy(key.signature[:], certificate.AggregateSignature)
	return key, true
}

// tryCertifiedDuplicateRead coalesces strictly in-flight identical certified
// reads. Completed observations are removed before publication, never cached.
func (s *Server) tryCertifiedDuplicateRead(ctx context.Context, request CommitRequest, certificate *hotstuff.QuorumCertificate, phases *ownerOperationPhases) (CommitResult, bool, error) {
	key, eligible := certifiedDuplicateKey(request, certificate)
	if !eligible || s.certifiedReadSlots == nil {
		return s.readCertifiedDuplicate(ctx, request, certificate, phases, nil)
	}
	if err := ctx.Err(); err != nil {
		return CommitResult{}, false, err
	}
	s.mu.Lock()
	if err := s.ctx.Err(); err != nil {
		s.mu.Unlock()
		return CommitResult{}, false, err
	}
	job := s.certifiedReads[key]
	if job == nil {
		if len(s.certifiedReads) >= s.capacity {
			s.mu.Unlock()
			return s.readCertifiedDuplicate(ctx, request, certificate, phases, nil)
		}
		if s.certifiedReads == nil {
			s.certifiedReads = make(map[certifiedReadKey]*certifiedReadJob)
		}
		job = &certifiedReadJob{done: make(chan struct{})}
		s.certifiedReads[key] = job
		s.certifiedFlights.Add(1)
		s.wg.Add(1)
		go s.runCertifiedRead(key, job)
	} else {
		s.certifiedJoins.Add(1)
	}
	s.mu.Unlock()
	phases.phase("certified_singleflight_wait")
	select {
	case <-job.done:
		// Done and cancellation can become ready together. Cancellation must
		// not turn into a late success for this requester or after owner close.
		if err := ctx.Err(); err != nil {
			return CommitResult{}, false, err
		}
		if err := s.ctx.Err(); err != nil {
			return CommitResult{}, false, err
		}
		if phases != nil {
			phases.duplicate, phases.transactions = job.duplicate, job.transactions
		}
		return job.result, job.handled, job.err
	case <-ctx.Done():
		return CommitResult{}, false, ctx.Err()
	case <-s.ctx.Done():
		return CommitResult{}, false, s.ctx.Err()
	}
}

func (s *Server) runCertifiedRead(key certifiedReadKey, job *certifiedReadJob) {
	var phases *ownerOperationPhases
	published := false
	publish := func(result CommitResult, handled bool, err error) {
		if published {
			return
		}
		// Physical completion calls this while execute.RLock is still held.
		// Delete and publish atomically; new callers can only start a fresh read.
		s.mu.Lock()
		delete(s.certifiedReads, key)
		job.result, job.handled, job.err = result, handled, err
		if phases != nil {
			job.duplicate, job.transactions = phases.duplicate, phases.transactions
		}
		published = true
		close(job.done)
		s.mu.Unlock()
	}
	defer func() {
		// HTTP handlers previously isolated panics from their synchronous read.
		// An independent worker must likewise never kill the owner or strand waiters.
		if recover() != nil {
			publish(CommitResult{}, false, errCertifiedDuplicateReadPanic)
		}
		s.wg.Done()
	}()
	phases = newOwnerOperationPhases("certified_duplicate_read", s)
	phases.hash, phases.parent, phases.number = key.request.BlockHash, key.request.ParentHash, key.request.Number
	certificate := hotstuff.QuorumCertificate{View: key.view, BlockHash: key.blockHash,
		Signers: append([]bool(nil), key.signers[:]...), AggregateSignature: append([]byte(nil), key.signature[:]...)}
	_, _, err := s.readCertifiedDuplicate(s.ctx, key.request, &certificate, phases, publish)
	phases.finish(err)
	flights, joins := s.CertifiedDuplicateReadCounts()
	log.Info("shared owner certified duplicate read counts", "flights", flights, "joins", joins, "number", key.request.Number, "hash", key.request.BlockHash)
}

// CertifiedDuplicateReadCounts reports cumulative in-flight jobs and matching
// callers that joined them. Completed observations are never retained.
func (s *Server) CertifiedDuplicateReadCounts() (flights, joins uint64) {
	return s.certifiedFlights.Load(), s.certifiedJoins.Load()
}
