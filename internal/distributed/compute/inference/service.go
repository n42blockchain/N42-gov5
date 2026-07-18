// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Inference service driving the opML request lifecycle. Tracks
// RequestStatus transitions (Pending → Processing → OptimisticVerified
// → Verified / Failed / Challenged), dispatches to an InferenceExecutor
// and publishes results into the ResultCache consumed by the precompile.

package inference

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/log"
)

// RequestStatus represents the lifecycle state of an inference request.
type RequestStatus uint8

const (
	RequestPending            RequestStatus = 0
	RequestProcessing         RequestStatus = 1
	RequestOptimisticVerified RequestStatus = 2
	RequestVerified           RequestStatus = 3
	RequestFailed             RequestStatus = 4
	RequestChallenged         RequestStatus = 5
)

func (s RequestStatus) String() string {
	switch s {
	case RequestPending:
		return "pending"
	case RequestProcessing:
		return "processing"
	case RequestOptimisticVerified:
		return "optimistic_verified"
	case RequestVerified:
		return "verified"
	case RequestFailed:
		return "failed"
	case RequestChallenged:
		return "challenged"
	default:
		return "unknown"
	}
}

// trackedRequest wraps an inference request with status tracking.
type trackedRequest struct {
	Request InferenceRequest
	Result  *InferenceResult
	Status  RequestStatus
	Error   string
}

// InferenceExecutor abstracts the actual model execution backend.
type InferenceExecutor interface {
	Execute(ctx context.Context, modelHash types.Hash, input []byte) (*InferenceResult, error)
}

// InferenceService manages the lifecycle of AI inference requests.
// Uses opML (optimistic machine learning) verification: results are accepted
// optimistically with an economic bond, subject to fraud proof challenges.
type InferenceService struct {
	mu          sync.RWMutex
	models      *ModelRegistry
	requests    map[types.Hash]*trackedRequest
	executor    InferenceExecutor
	resultCache *ResultCache
}

// NewInferenceService creates an inference service with the given model registry.
func NewInferenceService(models *ModelRegistry) *InferenceService {
	return &InferenceService{
		models:      models,
		requests:    make(map[types.Hash]*trackedRequest),
		resultCache: NewResultCache(4096, time.Hour),
	}
}

// SetExecutor sets the inference execution backend.
func (s *InferenceService) SetExecutor(executor InferenceExecutor) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.executor = executor
}

// Models returns the model registry.
func (s *InferenceService) Models() *ModelRegistry { return s.models }

// requestIDDomain separates inference request IDs from other Keccak uses.
var requestIDDomain = []byte("n42-inference-request-v1")

// ComputeRequestID derives the deterministic request ID for an inference
// submission. It is a pure function of the submission — no node-local
// state — so every node executing the same precompile call derives the
// same ID. The previous scheme mixed in an in-memory nonce, which
// diverges across nodes (restarts, local-only submissions) and would
// split consensus if the 0x0301 precompile were activated.
func ComputeRequestID(modelHash types.Hash, input []byte, submitter types.Address) types.Hash {
	inputHash := crypto.Keccak256Hash(input)
	data := make([]byte, 0, len(requestIDDomain)+96)
	data = append(data, requestIDDomain...)
	data = append(data, modelHash[:]...)
	data = append(data, inputHash[:]...)
	data = append(data, submitter[:]...)
	return crypto.Keccak256Hash(data)
}

// SubmitRequest creates a new inference request. Submission is
// idempotent: resubmitting the same (model, input, submitter) tuple while
// the original request is still tracked returns the existing request ID
// without resetting its state — the executor is deterministic, so a
// duplicate run would produce the same output anyway.
func (s *InferenceService) SubmitRequest(modelHash types.Hash, input []byte, submitter types.Address) (types.Hash, error) {
	if _, ok := s.models.Get(modelHash); !ok {
		return types.Hash{}, fmt.Errorf("inference: model %s not registered", modelHash.Hex())
	}

	requestID := ComputeRequestID(modelHash, input, submitter)

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.requests[requestID]; exists {
		return requestID, nil
	}

	inputCopy := make([]byte, len(input))
	copy(inputCopy, input)

	s.requests[requestID] = &trackedRequest{
		Request: InferenceRequest{
			ID:        requestID,
			ModelHash: modelHash,
			Input:     inputCopy,
			Submitter: submitter,
			CreatedAt: time.Now(),
		},
		Status: RequestPending,
	}

	log.Debug("Inference request submitted",
		"requestID", requestID.Hex()[:10],
		"model", modelHash.Hex()[:10],
	)
	return requestID, nil
}

// Execute runs the inference for a request using the registered executor.
func (s *InferenceService) Execute(ctx context.Context, requestID types.Hash) (*InferenceResult, error) {
	s.mu.Lock()
	tracked, ok := s.requests[requestID]
	if !ok {
		s.mu.Unlock()
		return nil, fmt.Errorf("inference: request %s not found", requestID.Hex())
	}
	if tracked.Status != RequestPending {
		s.mu.Unlock()
		return nil, fmt.Errorf("inference: request %s not in pending state", requestID.Hex())
	}
	tracked.Status = RequestProcessing
	executor := s.executor
	s.mu.Unlock()

	if executor == nil {
		s.mu.Lock()
		tracked.Status = RequestFailed
		tracked.Error = "no executor configured"
		s.mu.Unlock()
		return nil, fmt.Errorf("inference: no executor configured")
	}

	result, err := executor.Execute(ctx, tracked.Request.ModelHash, tracked.Request.Input)
	if err != nil {
		s.mu.Lock()
		tracked.Status = RequestFailed
		tracked.Error = err.Error()
		s.mu.Unlock()
		return nil, err
	}

	result.RequestID = requestID

	s.mu.Lock()
	// Re-check: status may have been changed concurrently (e.g. FinalizeRequest)
	if tracked.Status == RequestProcessing {
		tracked.Result = result
		tracked.Status = RequestOptimisticVerified
	}
	s.mu.Unlock()

	// Write to result cache for precompile access.
	if s.resultCache != nil && result != nil {
		outputCAS := crypto.Keccak256Hash(result.Output)
		s.resultCache.Put(requestID, RequestOptimisticVerified, outputCAS)
	}

	log.Debug("Inference completed (optimistic)",
		"requestID", requestID.Hex()[:10],
		"confidence", result.Confidence,
	)
	return result, nil
}

// FinalizeRequest marks an optimistic-verified request as fully verified.
func (s *InferenceService) FinalizeRequest(requestID types.Hash) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tracked, ok := s.requests[requestID]
	if !ok {
		return fmt.Errorf("inference: request %s not found", requestID.Hex())
	}
	if tracked.Status != RequestOptimisticVerified {
		return fmt.Errorf("inference: request %s not in optimistic-verified state", requestID.Hex())
	}
	tracked.Status = RequestVerified

	// Update cache with verified status.
	if s.resultCache != nil && tracked.Result != nil {
		outputCAS := crypto.Keccak256Hash(tracked.Result.Output)
		s.resultCache.Put(requestID, RequestVerified, outputCAS)
	}
	return nil
}

// GetCachedResult returns a cached inference result for precompile access.
func (s *InferenceService) GetCachedResult(requestID types.Hash) (*CachedResult, bool) {
	if s.resultCache == nil {
		return nil, false
	}
	return s.resultCache.Get(requestID)
}

// ResultCacheRef returns the result cache for external access.
func (s *InferenceService) ResultCacheRef() *ResultCache {
	return s.resultCache
}

// ChallengeRequest marks a request as challenged for fraud proof verification.
func (s *InferenceService) ChallengeRequest(requestID types.Hash) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tracked, ok := s.requests[requestID]
	if !ok {
		return fmt.Errorf("inference: request %s not found", requestID.Hex())
	}
	if tracked.Status != RequestOptimisticVerified {
		return fmt.Errorf("inference: request %s not challengeable", requestID.Hex())
	}
	tracked.Status = RequestChallenged
	return nil
}

// GetRequest returns the status and result for an inference request.
func (s *InferenceService) GetRequest(requestID types.Hash) (*InferenceRequest, *InferenceResult, RequestStatus, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	tracked, ok := s.requests[requestID]
	if !ok {
		return nil, nil, 0, fmt.Errorf("inference: request %s not found", requestID.Hex())
	}
	return &tracked.Request, tracked.Result, tracked.Status, nil
}

// PendingCount returns the number of pending inference requests.
func (s *InferenceService) PendingCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	count := 0
	for _, r := range s.requests {
		if r.Status == RequestPending || r.Status == RequestProcessing {
			count++
		}
	}
	return count
}

// TotalCount returns the total number of tracked requests.
func (s *InferenceService) TotalCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.requests)
}

// PurgeCompleted removes all completed/failed/challenged requests. Returns count purged.
func (s *InferenceService) PurgeCompleted() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	purged := 0
	for id, r := range s.requests {
		if r.Status == RequestVerified || r.Status == RequestFailed || r.Status == RequestChallenged {
			delete(s.requests, id)
			purged++
		}
	}
	return purged
}

// ListRequests returns all tracked requests with their statuses.
func (s *InferenceService) ListRequests() []struct {
	ID     types.Hash
	Status RequestStatus
} {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]struct {
		ID     types.Hash
		Status RequestStatus
	}, 0, len(s.requests))
	for id, r := range s.requests {
		result = append(result, struct {
			ID     types.Hash
			Status RequestStatus
		}{id, r.Status})
	}
	return result
}
