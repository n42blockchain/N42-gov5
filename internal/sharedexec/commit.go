package sharedexec

import (
	"context"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/rawdb"
)

type CommitRequest struct {
	Identity   Identity   `json:"identity"`
	BlockHash  types.Hash `json:"block_hash"`
	ParentHash types.Hash `json:"parent_hash"`
	Number     uint64     `json:"number"`
}

type CommitResult struct {
	CommitRequest
	Committed  bool       `json:"committed"`
	HeadHash   types.Hash `json:"head_hash"`
	HeadNumber uint64     `json:"head_number"`
}

// NewWithTrustedCommitOwner grants finality decisions to one trusted owner.
// The caller must exclusively own the BlockChain and its database. This secret
// is an owner capability, not a QC or a validator signing key. Ordinary import
// clients must not receive it. New leaves commit disabled.
func NewWithTrustedCommitOwner(chain *internal.BlockChain, capacity int, owner [32]byte) (*Server, error) {
	if owner == ([32]byte{}) {
		return nil, errors.New("nonzero commit owner capability required")
	}
	s, err := New(chain, capacity)
	if err != nil {
		return nil, err
	}
	s.commitOwner = owner
	return s, nil
}

func (s *Server) authorizedCommit(owner [32]byte) bool {
	return s.commitOwner != ([32]byte{}) && subtle.ConstantTimeCompare(owner[:], s.commitOwner[:]) == 1
}

// Commit only appends to the canonical prefix or acknowledges an existing
// canonical block. It never authorizes a branch switch across committed state.
func (s *Server) Commit(ctx context.Context, request CommitRequest, owner [32]byte) (CommitResult, error) {
	if !s.authorizedCommit(owner) {
		return CommitResult{}, errors.New("commit owner authorization required")
	}
	return s.commit(ctx, request, nil)
}

func (s *Server) commit(ctx context.Context, request CommitRequest, certificate *hotstuff.QuorumCertificate) (result CommitResult, err error) {
	phases := newOwnerOperationPhases("commit", s)
	phases.hash, phases.parent, phases.number = request.BlockHash, request.ParentHash, request.Number
	defer func() { phases.finish(err) }()
	phases.phase("request_binding")
	if request.Identity != s.identity || request.Number == 0 || request.BlockHash == (types.Hash{}) {
		return CommitResult{}, errors.New("commit chain identity, hash or height mismatch")
	}
	s.mu.Lock()
	if s.closed || s.resumeArming {
		s.mu.Unlock()
		return CommitResult{}, errors.New("execution service closed")
	}
	s.started = true
	s.wg.Add(1)
	s.mu.Unlock()
	defer s.wg.Done()
	var freshJob *certifiedWriteJob
	var freshKey certifiedReadKey
	var freshComplete func(CommitResult, error, bool)
	allowShared := certificate != nil
	admitted := false
	releaseAdmission := func() {
		if admitted {
			<-s.certifiedWriteGate
			admitted = false
		}
	}
	defer releaseAdmission()
	if certificate != nil {
		var eligible bool
		freshKey, eligible = certifiedDuplicateKey(request, certificate)
		if eligible {
			job, _, lookupErr := s.certifiedFreshCommit(ctx, freshKey, false)
			if lookupErr != nil {
				return CommitResult{}, lookupErr
			}
			if job != nil {
				return s.waitCertifiedFreshCommit(ctx, request, certificate, phases, job)
			}
		}
		hint, probeErr := s.canonicalDuplicateHint(ctx, request, phases)
		if probeErr != nil {
			return CommitResult{}, probeErr
		}
		if !hint {
			if eligible {
				job, created, joinErr := s.certifiedFreshCommit(ctx, freshKey, true)
				if joinErr != nil {
					return CommitResult{}, joinErr
				}
				if job != nil && !created {
					return s.waitCertifiedFreshCommit(ctx, request, certificate, phases, job)
				}
				if created {
					freshJob = job
					phases.certifiedWriteCreated = true
					freshComplete = func(value CommitResult, finalErr error, fresh bool) {
						s.finishCertifiedFreshCommit(ctx, freshKey, freshJob, value, finalErr, fresh, phases.transactions)
					}
					// Covers admission/probe errors and panics before execute is held.
					defer func() {
						if caught := recover(); caught != nil {
							freshComplete(CommitResult{}, errCertifiedFreshCommitPanic, false)
							panic(caught)
						}
						freshComplete(CommitResult{}, err, false)
					}()
				}
			}
			phases.phase("certified_writer_admission_wait")
			select {
			case s.certifiedWriteGate <- struct{}{}:
				admitted = true
			case <-ctx.Done():
				return CommitResult{}, ctx.Err()
			case <-s.ctx.Done():
				return CommitResult{}, s.ctx.Err()
			}
			if err := ctx.Err(); err != nil {
				return CommitResult{}, err
			}
			if err := s.ctx.Err(); err != nil {
				return CommitResult{}, err
			}
			// An earlier writer may have made this request canonical while
			// admission was queued. Route from a fresh physical observation;
			// no result or QC verification is retained by this gate.
			hint, probeErr = s.canonicalDuplicateHint(ctx, request, phases)
			if probeErr != nil {
				return CommitResult{}, probeErr
			}
			if hint {
				if freshComplete != nil {
					freshComplete(CommitResult{}, nil, false)
				}
				releaseAdmission()
			}
		}
		if hint {
			shared, handled, readErr := s.tryCertifiedDuplicateRead(ctx, request, certificate, phases)
			if readErr != nil || handled {
				return shared, readErr
			}
			allowShared = false
		}
	}
	for {
		var retry bool
		result, retry, err = s.commitExclusiveAttemptWithFresh(ctx, request, certificate, phases, allowShared, freshComplete)
		if !retry {
			return result, err
		}
		allowShared = false
	}
}

// commitExclusiveAttempt classifies every request after all earlier writers.
// A shared fallback is retried once by commit with no reusable observation.
func (s *Server) commitExclusiveAttempt(ctx context.Context, request CommitRequest, certificate *hotstuff.QuorumCertificate, phases *ownerOperationPhases, allowShared bool) (result CommitResult, retry bool, err error) {
	return s.commitExclusiveAttemptWithFresh(ctx, request, certificate, phases, allowShared, nil)
}

func (s *Server) commitExclusiveAttemptWithFresh(ctx context.Context, request CommitRequest, certificate *hotstuff.QuorumCertificate, phases *ownerOperationPhases, allowShared bool, complete func(CommitResult, error, bool)) (result CommitResult, retry bool, err error) {
	phases.phase("execute_wait")
	s.execute.Lock()
	held := true
	defer func() {
		if held {
			s.execute.Unlock()
		}
	}()
	s.beginSpeculativeExecutionExclusion()
	defer s.endSpeculativeExecutionExclusion()
	s.stopSpeculativeQueueUnlessCertifiedParent(certificate, request)
	defer func() {
		// A failed matching writer may have advanced the queue generation
		// without canonicalizing its parent. Revoke its child immediately;
		// generation arithmetic alone must never authorize reuse.
		if caught := recover(); caught != nil {
			if certificate != nil {
				s.discardSpeculativeQueueSlot()
			}
			panic(caught)
		}
		if err != nil && certificate != nil {
			s.discardSpeculativeQueueSlot()
		}
	}()
	classifiedFresh := false
	if complete != nil {
		// Later reconciliation runs first. Publish only its final result while
		// execute still protects the successful fresh physical commit.
		defer func() {
			if caught := recover(); caught != nil {
				complete(CommitResult{}, errCertifiedFreshCommitPanic, false)
				panic(caught)
			}
			complete(result, err, classifiedFresh && !retry)
		}()
	}
	phases.phase("request_context")
	if err := ctx.Err(); err != nil {
		return CommitResult{}, false, err
	}
	if err := s.ctx.Err(); err != nil {
		return CommitResult{}, false, err
	}
	var duplicate bool
	phases.phase("stored_header_read")
	err = s.chain.DB().View(ctx, func(tx kv.Tx) error {
		header, readErr := rawdb.ReadHeaderByHash(tx, request.BlockHash)
		if readErr != nil {
			return readErr
		}
		if header == nil || header.Number == nil || !header.Number.IsUint64() || header.Hash() != request.BlockHash || header.Number.Uint64() != request.Number || header.ParentHash != request.ParentHash {
			return errors.New("commit stored header identity mismatch or missing header")
		}

		head := rawdb.ReadHeadBlockHash(tx)
		number := rawdb.ReadHeaderNumber(tx, head)
		if number == nil {
			return errors.New("commit canonical head missing")
		}
		headCanonical, err := rawdb.ReadCanonicalHash(tx, *number)
		if err != nil {
			return err
		}
		if headCanonical != head {
			return errors.New("commit canonical head is inconsistent")
		}
		canonical, err := rawdb.ReadCanonicalHash(tx, request.Number)
		if err != nil {
			return err
		}
		// The existing finality marker must itself belong to this prefix.
		finalized := rawdb.ReadHotStuffCommittedHead(tx)
		if finalized != (types.Hash{}) {
			fn := rawdb.ReadHeaderNumber(tx, finalized)
			if fn == nil || *fn > *number {
				return errors.New("commit finality marker is inconsistent")
			}
			fh, err := rawdb.ReadCanonicalHash(tx, *fn)
			if err != nil {
				return err
			}
			if fh != finalized {
				return errors.New("commit finality marker conflicts with canonical prefix")
			}
		}
		if request.Number <= *number {
			if canonical != request.BlockHash {
				return errors.New("commit conflicts with canonical prefix")
			}
			duplicate = true
			return nil
		}
		if request.Number != *number+1 || request.ParentHash != head {
			return errors.New("commit requires direct canonical parent; retry after parent commit")
		}
		return nil
	})
	if err != nil {
		return CommitResult{}, false, err
	}
	phases.duplicate = duplicate
	classifiedFresh = !duplicate
	if duplicate && complete != nil {
		// A stale initial false hint cannot turn a canonical read into a
		// reusable fresh-write success, including before a shared-read unlock.
		complete(CommitResult{}, nil, false)
	}
	if duplicate && certificate != nil && allowShared {
		// Never wait for a read slot while holding exclusive ownership.
		s.execute.Unlock()
		held = false
		shared, handled, readErr := s.tryCertifiedDuplicateRead(ctx, request, certificate, phases)
		if readErr != nil || handled {
			return shared, false, readErr
		}
		// No proof survives this fallback. The caller repeats the complete
		// exclusive classification and legacy verification/repair once.
		return CommitResult{}, true, nil
	}
	var certifiedDuplicate bool
	if duplicate && certificate != nil {
		phases.phase("certified_execution_verification")
		head, existing, verifyErr := s.chain.VerifyCertifiedCanonicalExecution(certificate)
		if verifyErr != nil {
			return CommitResult{}, false, verifyErr
		}
		if existing {
			if head.BlockHash != request.BlockHash || head.ParentHash != request.ParentHash || head.Number != request.Number {
				return CommitResult{}, false, errors.New("certified canonical execution request binding differs")
			}
			phases.transactions = head.Transactions
			if q := s.ingress; q != nil {
				q.mu.Lock()
				certifiedDuplicate = !q.closed && !q.reconciling && !q.bootstrapPending && q.stamp.hash == head.HeadHash && q.stamp.number == head.HeadNumber
				q.mu.Unlock()
			} else {
				certifiedDuplicate = true
			}
		}
	}
	if !certifiedDuplicate {
		phases.phase("durable_evidence")
		transactions, verifyErr := s.durableStoredCommit(request)
		if verifyErr != nil {
			return CommitResult{}, false, verifyErr
		}
		phases.transactions = transactions

		canonicalWrote := false
		phases.phase("ingress_gate")
		if q := s.ingress; q != nil {
			q.mu.Lock()
			before := q.stamp
			epoch := q.epoch
			q.reconciling = true
			q.stamp.generation++
			q.mu.Unlock()
			// The request may be canceled after canonical mutation. Always reconcile
			// with the owner lifetime, retaining the dirty gate on any read failure.
			defer func() {
				phases.phase("ingress_reconcile")
				if reconcileErr := s.reconcileStrictIngress(phases); reconcileErr != nil && err == nil {
					err = reconcileErr
				}
				if canonicalWrote && err == nil && result.Committed && certificate != nil {
					q.mu.Lock()
					after, clean := q.stamp, !q.reconciling && !q.closed
					q.mu.Unlock()
					if clean && before.number+1 == request.Number && after.number == request.Number && after.hash == request.BlockHash && after.generation == before.generation+1 {
						s.specMu.Lock()
						s.specTransition = &speculativeCommitTransition{from: before, to: after, epoch: epoch}
						s.specMu.Unlock()
					}
				}
				phases.pause()
			}()
		}
		phases.phase("canonical_commit")
		if certificate != nil {
			if err := s.chain.CommitCertifiedExtension(certificate); err != nil {
				return CommitResult{}, false, err
			}
			canonicalWrote = true
		} else if !duplicate {
			// Do not use CommitToCanonicalWith's bookkeeping hook for authorization:
			// hook errors are swallowed. The service owns canonical decisions;
			// CommitExecutedExtension also rechecks applied state under bc.lock,
			// covering the chain's background future-block importer.
			if err := s.chain.CommitExecutedExtension(request.BlockHash); err != nil {
				return CommitResult{}, false, err
			}
		}
	}
	phases.phase("canonical_durability_read")
	result, err = s.readCommitDurability(ctx, request)
	return result, false, err
}

func (s *Server) serveCommit(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	var owner [32]byte
	raw, err := hex.DecodeString(r.Header.Get("X-N42-Commit-Owner"))
	if err != nil || len(raw) != len(owner) {
		http.Error(w, "commit owner authorization required", http.StatusForbidden)
		return
	}
	copy(owner[:], raw)
	if !s.authorizedCommit(owner) {
		http.Error(w, "commit owner authorization required", http.StatusForbidden)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var request CommitRequest
	if err := decoder.Decode(&request); err != nil {
		http.Error(w, "invalid commit request", http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		http.Error(w, "one commit request required", http.StatusBadRequest)
		return
	}
	result, err := s.Commit(r.Context(), request, owner)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}
