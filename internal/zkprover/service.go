// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package zkprover

import (
	"context"
	"fmt"
	"sync"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/log"
)

const (
	proofCacheSize  = 128
	jobPollInterval = 5 * time.Second
)

// jobInfo tracks a pending proof generation job.
type jobInfo struct {
	JobID      string
	BlockHash  types.Hash
	BlockNum   uint64
	GuestInput []byte
	SubmitTime time.Time
}

// ProverClient abstracts the external prover communication.
// In production, this is implemented by a gRPC client connecting to the
// ZISK prover cluster. For testing, use the NoopProverClient.
type ProverClient interface {
	// Submit sends a proof generation request to the external prover.
	// Returns a job ID for status tracking.
	Submit(ctx context.Context, blockHash types.Hash, blockNum uint64, guestInput []byte) (string, error)

	// Status queries the status of a pending proof job.
	// Returns the proof if completed, nil if still pending, or an error.
	Status(ctx context.Context, jobID string) (*Proof, error)

	// Close releases resources held by the client.
	Close() error
}

// NoopProverClient is a no-op implementation for testing and
// for nodes that only verify proofs (VerifyOnly mode).
type NoopProverClient struct{}

func (n *NoopProverClient) Submit(_ context.Context, _ types.Hash, _ uint64, _ []byte) (string, error) {
	return "", fmt.Errorf("prover client not configured")
}

func (n *NoopProverClient) Status(_ context.Context, _ string) (*Proof, error) {
	return nil, nil
}

func (n *NoopProverClient) Close() error { return nil }

// Service manages communication with an external ZISK prover cluster.
// It submits proof requests via gRPC, tracks pending jobs, and caches
// completed proofs.
type Service struct {
	cfg *conf.ZKProverCfg

	proofCache *lru.Cache[types.Hash, *Proof]
	jobs       map[string]*jobInfo // jobID → info
	mu         sync.RWMutex

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// client is the external prover communication interface.
	// Set via SetProverClient before Start() for production use.
	// Defaults to NoopProverClient if not set.
	client      ProverClient
	connectOnce sync.Once
}

// NewService creates a new ZK prover service.
func NewService(cfg *conf.ZKProverCfg) (*Service, error) {
	if cfg == nil {
		return nil, fmt.Errorf("zkprover: nil config")
	}
	cache, _ := lru.New[types.Hash, *Proof](proofCacheSize)
	ctx, cancel := context.WithCancel(context.Background())

	return &Service{
		cfg:        cfg,
		proofCache: cache,
		jobs:       make(map[string]*jobInfo),
		ctx:        ctx,
		cancel:     cancel,
	}, nil
}

// SetProverClient sets the external prover client.
// Must be called before Start() in production. If not called,
// the service uses NoopProverClient (submit always fails).
func (s *Service) SetProverClient(client ProverClient) {
	s.client = client
}

// Start begins the background job poller.
func (s *Service) Start() {
	if s.client == nil {
		s.client = &NoopProverClient{}
	}
	s.wg.Add(1)
	go s.pollJobs()
	log.Info("ZK prover service started", "addr", s.cfg.ProverAddr, "proofType", s.cfg.ProofType)
}

// Stop shuts down the prover service and releases resources.
func (s *Service) Stop() {
	s.cancel()
	s.wg.Wait()
	if s.client != nil {
		if err := s.client.Close(); err != nil {
			log.Warn("Failed to close prover client", "err", err)
		}
	}
	log.Info("ZK prover service stopped")
}

// SubmitBlock submits a block for proof generation.
// guestInput is the serialized GuestInput for the zkVM.
func (s *Service) SubmitBlock(blockHash types.Hash, blockNum uint64, guestInput []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Check if already submitted or proven.
	if _, ok := s.proofCache.Get(blockHash); ok {
		return nil // already proven
	}
	for _, j := range s.jobs {
		if j.BlockHash == blockHash {
			return nil // already pending
		}
	}

	// Check concurrency limit. MaxConcurrent <= 0 means unlimited.
	if s.cfg.MaxConcurrent > 0 && len(s.jobs) >= s.cfg.MaxConcurrent {
		return fmt.Errorf("zkprover: max concurrent jobs reached (%d)", s.cfg.MaxConcurrent)
	}

	// Generate a default job ID (overridden by client if available).
	// Use 8 bytes of the hash to reduce collision probability.
	jobID := fmt.Sprintf("zk-%d-%x", blockNum, blockHash[:8])

	// Try to submit to external prover if client is available.
	if s.client != nil {
		if remoteID, err := s.client.Submit(s.ctx, blockHash, blockNum, guestInput); err != nil {
			log.Debug("Prover client submit failed, using local tracking", "err", err)
		} else if remoteID != "" {
			jobID = remoteID
		}
	}

	s.jobs[jobID] = &jobInfo{
		JobID:      jobID,
		BlockHash:  blockHash,
		BlockNum:   blockNum,
		GuestInput: guestInput,
		SubmitTime: time.Now(),
	}

	log.Info("ZK proof job submitted", "jobID", jobID, "block", blockNum, "inputSize", len(guestInput))

	zkProofSubmitted.Inc()
	zkWitnessSize.Set(uint64(len(guestInput)))
	return nil
}

// GetProof retrieves a cached proof for the given block hash.
func (s *Service) GetProof(blockHash types.Hash) (*Proof, bool) {
	return s.proofCache.Get(blockHash)
}

// StoreProof manually stores a proof (e.g., received from P2P) and removes
// any pending job for the same block.
func (s *Service) StoreProof(proof *Proof) {
	if proof == nil {
		return
	}

	// Lock first, then operate on cache, to avoid race with SubmitBlock.
	s.mu.Lock()
	defer s.mu.Unlock()

	s.proofCache.Add(proof.BlockHash, proof)
	zkProofGenerated.Inc()
	if len(proof.ProofData) > 0 {
		zkProofSize.Set(uint64(len(proof.ProofData)))
	}

	// Clean up any pending job for this block.
	for jobID, info := range s.jobs {
		if info.BlockHash == proof.BlockHash {
			delete(s.jobs, jobID)
			break
		}
	}
}

// CancelJob removes a pending proof generation job by block hash.
// Returns true if a job was found and removed.
func (s *Service) CancelJob(blockHash types.Hash) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for jobID, info := range s.jobs {
		if info.BlockHash == blockHash {
			delete(s.jobs, jobID)
			return true
		}
	}
	return false
}

// HasPendingJob returns true if there is a pending job for the given block hash.
func (s *Service) HasPendingJob(blockHash types.Hash) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, info := range s.jobs {
		if info.BlockHash == blockHash {
			return true
		}
	}
	return false
}

// completedProof pairs a proof with its original submit time for duration tracking.
type completedProof struct {
	proof      *Proof
	submitTime time.Time
}

// pollJobs periodically checks the status of pending proof generation jobs.
func (s *Service) pollJobs() {
	defer s.wg.Done()
	ticker := time.NewTicker(jobPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.checkJobs()
		}
	}
}

// checkJobs iterates pending jobs and checks their status.
// In production, this would call gRPC GetProofStatus/GetProof.
func (s *Service) checkJobs() {
	// Snapshot the jobs under lock, then process outside the lock.
	// This prevents holding the lock during future gRPC calls.
	s.mu.RLock()
	snapshot := make([]*jobInfo, 0, len(s.jobs))
	for _, info := range s.jobs {
		snapshot = append(snapshot, info)
	}
	s.mu.RUnlock()

	timeout := time.Duration(s.cfg.ProverTimeout) * time.Second
	var timedOut []string

	var completed []*completedProof

	for _, info := range snapshot {
		if time.Since(info.SubmitTime) > timeout {
			log.Warn("ZK proof job timed out", "jobID", info.JobID, "block", info.BlockNum)
			timedOut = append(timedOut, info.JobID)
			zkProofFailed.Inc()
			continue
		}

		// Poll external prover for completed proofs.
		if s.client != nil {
			proof, err := s.client.Status(s.ctx, info.JobID)
			if err != nil {
				log.Debug("Prover status check failed", "jobID", info.JobID, "err", err)
				continue
			}
			if proof != nil {
				proof.BlockHash = info.BlockHash
				proof.BlockNumber = info.BlockNum
				completed = append(completed, &completedProof{proof: proof, submitTime: info.SubmitTime})
			}
		}
	}

	// Store completed proofs and remove timed-out jobs under write lock.
	if len(timedOut) > 0 || len(completed) > 0 {
		s.mu.Lock()
		for _, jobID := range timedOut {
			delete(s.jobs, jobID)
		}
		s.mu.Unlock()

		// StoreProof acquires its own lock, so call outside.
		for _, cp := range completed {
			elapsed := time.Since(cp.submitTime)
			log.Info("ZK proof received from prover", "block", cp.proof.BlockNumber, "hash", cp.proof.BlockHash, "elapsed", elapsed)
			zkProvingDuration.UpdateDuration(cp.submitTime)
			s.StoreProof(cp.proof)
		}
	}
}

// PendingJobs returns the number of pending proof generation jobs.
func (s *Service) PendingJobs() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.jobs)
}
