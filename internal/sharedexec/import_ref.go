package sharedexec

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
)

type ImportRefRequest struct {
	Identity  Identity   `json:"identity"`
	BlockHash types.Hash `json:"block_hash"`
}
type ImportRefResult struct {
	ImportRefRequest
	NeedBody bool          `json:"need_body"`
	Imported *ImportResult `json:"imported,omitempty"`
}

// ImportRef can only join a full-body import that already owns a sealed hash.
// A miss never registers work, takes execute, consults the Build cache, or reads
// chain state/receipts. The caller retains full local body availability.
func (s *Server) ImportRef(ctx context.Context, request ImportRefRequest) (ImportRefResult, error) {
	result := ImportRefResult{ImportRefRequest: request}
	if request.Identity != s.identity || request.BlockHash == (types.Hash{}) {
		return result, errors.New("execution reference identity/hash mismatch")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return result, errors.New("execution service closed")
	}
	job := s.jobs[importKey{s.identity, request.BlockHash}]
	s.mu.Unlock()
	if job == nil {
		result.NeedBody = true
		return result, nil
	}
	select {
	case <-ctx.Done():
		return result, ctx.Err()
	case <-job.done:
		imported := job.result
		result.Imported = &imported
		return result, nil
	}
}

func (s *Server) serveImportRef(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var request ImportRefRequest
	if err := decoder.Decode(&request); err != nil {
		http.Error(w, "invalid import reference request", http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		http.Error(w, "one import reference request required", http.StatusBadRequest)
		return
	}
	result, err := s.ImportRef(r.Context(), request)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if result.Imported != nil && !result.Imported.Durable {
		w.WriteHeader(http.StatusAccepted)
	}
	_ = json.NewEncoder(w).Encode(result)
}

// ImportRef requires the already locally verified sealed header. A need_body
// response is the sole allowed signal for a single full-body fallback. Invalid
// identity/commitments, transport errors and unsupported endpoints remain errors.
func (c *Client) ImportRef(ctx context.Context, request ImportRefRequest, header *block.Header) (ImportRefResult, error) {
	if request.Identity != c.identity || header == nil || header.Number == nil || !header.Number.IsUint64() || header.Number.IsZero() || header.Hash() != request.BlockHash {
		return ImportRefResult{}, errors.New("invalid client import reference")
	}
	raw, err := json.Marshal(request)
	if err != nil {
		return ImportRefResult{}, err
	}
	call, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://unix/v1/import-ref", bytes.NewReader(raw))
	if err != nil {
		return ImportRefResult{}, err
	}
	call.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(call)
	if err != nil {
		return ImportRefResult{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusAccepted {
		return ImportRefResult{}, errors.New("execution service rejected import reference")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 16385))
	if err != nil || len(data) > 16384 {
		return ImportRefResult{}, errors.New("invalid import reference response size")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var result ImportRefResult
	if err := decoder.Decode(&result); err != nil {
		return ImportRefResult{}, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return ImportRefResult{}, errors.New("invalid trailing import reference response")
	}
	if result.ImportRefRequest != request {
		return ImportRefResult{}, errors.New("import reference response binding mismatch")
	}
	if result.NeedBody {
		if result.Imported != nil || response.StatusCode != http.StatusOK {
			return ImportRefResult{}, errors.New("ambiguous import reference miss")
		}
		return result, nil
	}
	if result.Imported == nil {
		return ImportRefResult{}, errors.New("import reference response missing evidence")
	}
	if err := validateImportResult(c.identity, request.BlockHash, header, *result.Imported, response.StatusCode); err != nil {
		return ImportRefResult{}, err
	}
	return result, nil
}
