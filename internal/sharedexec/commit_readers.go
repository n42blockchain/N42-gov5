package sharedexec

import (
	"context"
	"errors"

	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// canonicalDuplicateHint only routes the request. The full reader freshly
// validates every QC, request binding and physical execution record afterward.
func (s *Server) canonicalDuplicateHint(ctx context.Context, request CommitRequest, phases *ownerOperationPhases) (bool, error) {
	phases.phase("certified_read_probe_wait")
	s.execute.RLock()
	defer s.execute.RUnlock()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := s.ctx.Err(); err != nil {
		return false, err
	}
	phases.phase("certified_read_probe")
	var same bool
	err := s.chain.DB().View(ctx, func(tx kv.Tx) error {
		hash, err := rawdb.ReadCanonicalHash(tx, request.Number)
		same = hash == request.BlockHash
		return err
	})
	return same, err
}

// tryCertifiedDuplicateRead shares only fresh, read-only certified checks.
// handled=false requires a complete exclusive retry; errors never authorize it.
func (s *Server) tryCertifiedDuplicateRead(ctx context.Context, request CommitRequest, certificate *hotstuff.QuorumCertificate, phases *ownerOperationPhases) (result CommitResult, handled bool, err error) {
	if s.certifiedReadSlots == nil {
		return CommitResult{}, false, nil
	}
	phases.phase("certified_read_slot_wait")
	select {
	case s.certifiedReadSlots <- struct{}{}:
		defer func() { <-s.certifiedReadSlots }()
	case <-ctx.Done():
		return CommitResult{}, false, ctx.Err()
	case <-s.ctx.Done():
		return CommitResult{}, false, s.ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return CommitResult{}, false, err
	}
	if err := s.ctx.Err(); err != nil {
		return CommitResult{}, false, err
	}
	phases.phase("certified_read_lock_wait")
	s.execute.RLock()
	defer s.execute.RUnlock()
	if err := ctx.Err(); err != nil {
		return CommitResult{}, false, err
	}
	if err := s.ctx.Err(); err != nil {
		return CommitResult{}, false, err
	}
	phases.phase("certified_execution_verification")
	proof, existing, err := s.chain.VerifyCertifiedCanonicalExecution(certificate)
	if err != nil || !existing {
		return CommitResult{}, false, err
	}
	if proof.BlockHash != request.BlockHash || proof.ParentHash != request.ParentHash || proof.Number != request.Number {
		return CommitResult{}, false, errors.New("certified canonical execution request binding differs")
	}
	phases.duplicate = true
	phases.transactions = proof.Transactions
	if q := s.ingress; q != nil {
		q.mu.Lock()
		clean := !q.closed && !q.reconciling && !q.bootstrapPending && q.stamp.hash == proof.HeadHash && q.stamp.number == proof.HeadNumber
		q.mu.Unlock()
		if !clean {
			return CommitResult{}, false, nil
		}
	}
	phases.phase("canonical_durability_read")
	result, err = s.readCommitDurability(ctx, request)
	return result, err == nil, err
}

// Both exclusive and shared paths perform the same final physical durability read.
func (s *Server) readCommitDurability(ctx context.Context, request CommitRequest) (result CommitResult, err error) {
	result = CommitResult{CommitRequest: request}
	err = s.chain.DB().View(ctx, func(tx kv.Tx) error {
		hash, err := rawdb.ReadCanonicalHash(tx, request.Number)
		if err != nil {
			return err
		}
		result.HeadHash = rawdb.ReadHeadBlockHash(tx)
		number := rawdb.ReadHeaderNumber(tx, result.HeadHash)
		if hash != request.BlockHash || number == nil || *number < request.Number {
			return errors.New("commit canonical durability check failed")
		}
		result.HeadNumber = *number
		finalized := rawdb.ReadHotStuffCommittedHead(tx)
		fn := rawdb.ReadHeaderNumber(tx, finalized)
		if fn == nil || *fn < request.Number {
			return errors.New("commit finality marker durability check failed")
		}
		fh, err := rawdb.ReadCanonicalHash(tx, *fn)
		if err != nil {
			return err
		}
		if fh != finalized {
			return errors.New("commit finality marker durability check failed")
		}
		result.Committed = true
		return nil
	})
	return result, err
}
