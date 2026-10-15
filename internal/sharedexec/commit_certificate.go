package sharedexec

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
)

type CertifiedCommitRequest struct {
	CommitRequest
	Certificate hotstuff.QuorumCertificate `json:"certificate"`
}

// CommitCertified requires a public CommitQC, never an owner capability.
func (s *Server) CommitCertified(ctx context.Context, request CertifiedCommitRequest) (CommitResult, error) {
	qc := request.Certificate.Clone()
	if qc.BlockHash != request.BlockHash || qc.View == 0 || len(qc.Signers) != 7 || len(qc.AggregateSignature) != 96 {
		return CommitResult{}, errors.New("invalid commit certificate binding or size")
	}
	return s.commit(ctx, request.CommitRequest, &qc)
}

func (s *Server) serveCertifiedCommit(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var request CertifiedCommitRequest
	if err := decoder.Decode(&request); err != nil {
		http.Error(w, "invalid certified commit request", http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		http.Error(w, "one certified commit request required", http.StatusBadRequest)
		return
	}
	result, err := s.CommitCertified(r.Context(), request)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}
