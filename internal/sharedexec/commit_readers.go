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

// readCertifiedDuplicate performs one fresh, read-only certified check.
// Completion publishes only scalar observations while execute.RLock is held;
// handled=false still requires every caller to retry the full exclusive path.
func (s *Server) readCertifiedDuplicate(ctx context.Context, request CommitRequest, certificate *hotstuff.QuorumCertificate, phases *ownerOperationPhases, complete func(CommitResult, bool, error)) (result CommitResult, handled bool, err error) {
	var slot, locked bool
	defer func() {
		if recover() != nil {
			result, handled, err = CommitResult{}, false, errCertifiedDuplicateReadPanic
		}
		if complete != nil {
			complete(result, handled, err)
		}
		if locked {
			s.execute.RUnlock()
		}
		if slot {
			<-s.certifiedReadSlots
		}
	}()
	if s.certifiedReadSlots == nil {
		return CommitResult{}, false, nil
	}
	phases.phase("certified_read_slot_wait")
	select {
	case s.certifiedReadSlots <- struct{}{}:
		slot = true
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
	locked = true
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
