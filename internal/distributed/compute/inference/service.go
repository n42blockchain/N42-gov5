// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package inference

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/n42blockchain/N42/common/crypto"
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
	mu       sync.RWMutex
	models   *ModelRegistry
	requests map[types.Hash]*trackedRequest
	executor InferenceExecutor
	nonce    uint64
}

// NewInferenceService creates an inference service with the given model registry.
func NewInferenceService(models *ModelRegistry) *InferenceService {
	return &InferenceService{
		models:   models,
		requests: make(map[types.Hash]*trackedRequest),
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

// SubmitRequest creates a new inference request.
func (s *InferenceService) SubmitRequest(modelHash types.Hash, input []byte, submitter types.Address) (types.Hash, error) {
	if _, ok := s.models.Get(modelHash); !ok {
		return types.Hash{}, fmt.Errorf("inference: model %s not registered", modelHash.Hex())
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.nonce++
	data := make([]byte, 0, 72)
	data = append(data, modelHash[:]...)
	data = append(data, submitter[:]...)
	data = append(data, byte(s.nonce>>56), byte(s.nonce>>48), byte(s.nonce>>40), byte(s.nonce>>32),
		byte(s.nonce>>24), byte(s.nonce>>16), byte(s.nonce>>8), byte(s.nonce))
	requestID := crypto.Keccak256Hash(data)

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
	return nil
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
