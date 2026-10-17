package sharedexec

import (
	"context"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/n42blockchain/N42/common/block"
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
	s.execute.Lock()
	defer s.execute.Unlock()
	if err := ctx.Err(); err != nil {
		return CommitResult{}, err
	}
	if err := s.ctx.Err(); err != nil {
		return CommitResult{}, err
	}
	var b *block.Block
	var duplicate bool
	err = s.chain.DB().View(ctx, func(tx kv.Tx) error {
		var err error
		b, err = rawdb.ReadBlockByHash(tx, request.BlockHash)
		if err != nil {
			return err
		}
		if b == nil || b.Hash() != request.BlockHash || b.Number64().Uint64() != request.Number || b.ParentHash() != request.ParentHash {
			return errors.New("commit stored block identity mismatch or missing block")
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
		return CommitResult{}, err
	}
	verified, err := s.durable(b)
	if err != nil {
		return CommitResult{}, err
	}
	if verified == nil {
		return CommitResult{}, errors.New("commit requires actual durable execution")
	}
	if q := s.ingress; q != nil {
		q.mu.Lock()
		q.reconciling = true
		q.stamp.generation++
		q.mu.Unlock()
		// The request may be canceled after canonical mutation. Always reconcile
		// with the owner lifetime, retaining the dirty gate on any read failure.
		defer func() {
			if reconcileErr := s.reconcileStrictIngress(); reconcileErr != nil && err == nil {
				err = reconcileErr
			}
		}()
	}
	if certificate != nil {
		if err := s.chain.CommitCertifiedExtension(certificate); err != nil {
			return CommitResult{}, err
		}
	} else if !duplicate {
		// Do not use CommitToCanonicalWith's bookkeeping hook for authorization:
		// hook errors are swallowed. The service owns canonical decisions;
		// CommitExecutedExtension also rechecks applied state under bc.lock,
		// covering the chain's background future-block importer.
		if err := s.chain.CommitExecutedExtension(request.BlockHash); err != nil {
			return CommitResult{}, err
		}
	}
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
