// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Coprocessor Service wiring the Registry, TaskManager, TieredVerifier,
// ChallengeManager, ProviderRegistry, Marketplace and SlashManager
// into a single lifecycle. NewService validates the CoprocessorCfg
// before constructing subcomponents and exposes Start / Stop hooks.

package coprocessor

import (
	"context"
	"sync"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/log"
)

// Service manages the distributed coprocessor lifecycle: task submission, tiered
// verification, provider marketplace, challenge resolution, and pruning.
type Service struct {
	cfg      *conf.CoprocessorCfg
	registry *Registry
	tasks    *TaskManager

	// Phase 1: Tiered verification
	verifier   *TieredVerifier
	challenges *ChallengeManager

	// Phase 2: Provider network
	providers   *ProviderRegistry
	marketplace *Marketplace
	slasher     *SlashManager

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	once   sync.Once
}

// NewService creates a coprocessor service. Returns error if config is invalid.
func NewService(cfg *conf.CoprocessorCfg) (*Service, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())

	registry := NewRegistry()
	providers := NewProviderRegistry(cfg.MinProviderStake)

	challengeWindow := cfg.OptimisticChallengeSec
	if challengeWindow <= 0 {
		challengeWindow = 3600
	}

	slashPct := cfg.SlashPercentage
	if slashPct <= 0 {
		slashPct = 10
	}

	return &Service{
		cfg:         cfg,
		registry:    registry,
		tasks:       NewTaskManager(cfg.MaxPendingTasks, time.Duration(cfg.TaskTimeoutSec)*time.Second),
		verifier:    NewTieredVerifier(registry),
		challenges:  NewChallengeManager(challengeWindow),
		providers:   providers,
		marketplace: NewMarketplace(providers),
		slasher:     NewSlashManager(providers, slashPct),
		ctx:         ctx,
		cancel:      cancel,
	}, nil
}

// Start launches background workers for task expiry, pruning, and challenge finalization.
func (s *Service) Start() {
	s.wg.Add(1)
	go s.maintenanceLoop()
	log.Info("Distributed coprocessor service started",
		"maxConcurrent", s.cfg.MaxConcurrentTasks,
		"maxPending", s.cfg.MaxPendingTasks,
		"taskTimeout", s.cfg.TaskTimeoutSec,
		"marketplace", s.cfg.EnableMarketplace,
	)
}

// Stop gracefully shuts down the service.
func (s *Service) Stop() {
	s.once.Do(func() {
		s.cancel()
		s.wg.Wait()
		log.Info("Distributed coprocessor service stopped",
			"totalTasks", s.tasks.TotalCount(),
			"programs", s.registry.Count(),
			"providers", s.providers.ActiveCount(),
		)
	})
}

// Registry returns the program registry for external access.
func (s *Service) Registry() *Registry { return s.registry }

// Tasks returns the task manager for external access.
func (s *Service) Tasks() *TaskManager { return s.tasks }

// Verifier returns the tiered verifier for external access.
func (s *Service) Verifier() *TieredVerifier { return s.verifier }

// Challenges returns the challenge manager for external access.
func (s *Service) Challenges() *ChallengeManager { return s.challenges }

// Providers returns the provider registry for external access.
func (s *Service) Providers() *ProviderRegistry { return s.providers }

// Marketplace returns the marketplace for external access.
func (s *Service) MarketplaceManager() *Marketplace { return s.marketplace }

// Slasher returns the slash manager for external access.
func (s *Service) Slasher() *SlashManager { return s.slasher }

// SubmitTask creates a new compute task after validating the program exists.
// If marketplace is enabled and a provider is available, the task is published for bidding.
func (s *Service) SubmitTask(programHash types.Hash, input []byte, submitter types.Address) (types.Hash, error) {
	if _, ok := s.registry.Get(programHash); !ok {
		return types.Hash{}, ErrProgramNotRegistered
	}
	return s.tasks.Submit(programHash, input, submitter)
}

// SubmitProof validates and records a proof for a task using tiered verification.
// Uses atomic status check-and-update to prevent TOCTOU races.
func (s *Service) SubmitProof(taskID types.Hash, proofData, publicOutputs []byte) (bool, error) {
	if len(proofData) == 0 {
		return false, ErrInvalidProof
	}

	// Atomic check-and-transition: Pending/Proving → Proving (under single lock hold)
	_, err := s.tasks.TransitionToProving(taskID)
	if err != nil {
		return false, err
	}

	task, ok := s.tasks.GetTask(taskID)
	if !ok {
		return false, ErrTaskNotFound
	}

	// Tiered verification
	valid, verifyErr := s.verifier.Verify(task, proofData)
	if verifyErr != nil || !valid {
		errMsg := "verification failed"
		if verifyErr != nil {
			errMsg = verifyErr.Error()
		}
		s.tasks.UpdateStatus(taskID, TaskFailed, nil, nil, errMsg)
		if verifyErr != nil {
			return false, verifyErr
		}
		return false, ErrInvalidProof
	}

	// Route by verification tier
	if task.VerificationTier == TierOptimistic {
		deadline := time.Now().Add(time.Duration(s.cfg.OptimisticChallengeSec) * time.Second)
		s.tasks.SetOptimisticVerified(taskID, proofData, publicOutputs, deadline)
		log.Debug("Task optimistic-verified, challenge window open",
			"taskID", taskID.Hex()[:10],
			"deadline", deadline.Format(time.RFC3339),
		)
		return true, nil
	}

	// TierZK or TierTEE: immediate final verification
	s.tasks.UpdateStatus(taskID, TaskVerified, proofData, publicOutputs, "")
	log.Debug("Coprocessor task proof verified",
		"taskID", taskID.Hex()[:10],
		"tier", task.VerificationTier.String(),
		"proofSize", len(proofData),
		"outputSize", len(publicOutputs),
	)

	// Reward provider if assigned
	if task.AssignedProvider != (types.Address{}) {
		s.slasher.Reward(task.AssignedProvider, task.RewardAmount, taskID)
	}

	return true, nil
}

// SubmitChallenge files a challenge against an optimistic-verified task.
// The task must be in OptimisticVerified state and within the challenge window.
// Uses atomic status transition to prevent TOCTOU races.
func (s *Service) SubmitChallenge(taskID types.Hash, challenger types.Address, bond uint64, reason string) (types.Hash, error) {
	// Atomic check: status == OptimisticVerified && within window → Challenged
	if err := s.tasks.TransitionToChallenged(taskID, challenger); err != nil {
		return types.Hash{}, err
	}

	challengeID, err := s.challenges.Submit(taskID, challenger, bond, reason)
	if err != nil {
		return types.Hash{}, err
	}

	log.Info("Challenge submitted",
		"taskID", taskID.Hex()[:10],
		"challenger", challenger.Hex()[:10],
		"bond", bond,
	)
	return challengeID, nil
}

// ClaimTask allows a provider to claim an unassigned pending task.
func (s *Service) ClaimTask(taskID types.Hash, providerAddr types.Address) error {
	provider, ok := s.providers.Get(providerAddr)
	if !ok {
		return ErrProviderNotFound
	}
	if provider.Status != ProviderActive {
		return ErrProviderSuspended
	}

	task, ok := s.tasks.GetTask(taskID)
	if !ok {
		return ErrTaskNotFound
	}
	if task.Status != TaskPending {
		return ErrTaskNotPending
	}
	if task.AssignedProvider != (types.Address{}) {
		return ErrTaskAlreadyAssigned
	}

	return s.tasks.AssignProvider(taskID, providerAddr, 0)
}

// RegisterProvider registers a new compute provider.
func (s *Service) RegisterProvider(addr types.Address, stake uint64, capabilities []Capability) error {
	return s.providers.Register(addr, stake, capabilities)
}

// UnregisterProvider removes a compute provider.
func (s *Service) UnregisterProvider(addr types.Address) error {
	return s.providers.Unregister(addr)
}

// maintenanceLoop periodically expires stale tasks, prunes old completed tasks,
// and finalizes optimistic verifications past their challenge window.
func (s *Service) maintenanceLoop() {
	defer s.wg.Done()

	interval := time.Duration(s.cfg.PruneIntervalSec) * time.Second
	if interval <= 0 {
		interval = 60 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			expired := s.tasks.ExpireStale()
			pruned := s.tasks.Prune(time.Duration(s.cfg.TaskTimeoutSec*2) * time.Second)

			// Finalize optimistic tasks past challenge window with no pending challenges
			finalized := s.finalizeOptimisticTasks()

			// Slash providers for expired tasks they were assigned
			s.slashExpiredAssignments()

			if expired > 0 || pruned > 0 || finalized > 0 {
				log.Debug("Coprocessor maintenance",
					"expired", expired,
					"pruned", pruned,
					"finalized", finalized,
					"pending", s.tasks.PendingCount(),
					"total", s.tasks.TotalCount(),
				)
			}
		}
	}
}

// finalizeOptimisticTasks checks for optimistic-verified tasks whose challenge
// window has expired without pending challenges and marks them as verified.
func (s *Service) finalizeOptimisticTasks() int {
	optimistic := s.tasks.ListByStatus(TaskOptimisticVerified)
	readyIDs := s.challenges.ExpireWindow(optimistic)
	for _, taskID := range readyIDs {
		s.tasks.UpdateStatus(taskID, TaskVerified, nil, nil, "")
		task, ok := s.tasks.GetTask(taskID)
		if ok && task.AssignedProvider != (types.Address{}) {
			s.slasher.Reward(task.AssignedProvider, task.RewardAmount, taskID)
		}
		log.Debug("Optimistic task finalized",
			"taskID", taskID.Hex()[:10],
		)
	}
	return len(readyIDs)
}

// slashExpiredAssignments slashes providers whose assigned tasks expired.
// After slashing, clears the provider assignment to prevent double-slashing
// on subsequent maintenance ticks.
func (s *Service) slashExpiredAssignments() {
	expiredTasks := s.tasks.ListByStatus(TaskExpired)
	for _, task := range expiredTasks {
		if task.AssignedProvider != (types.Address{}) {
			s.slasher.Slash(task.AssignedProvider, SlashTimeout, task.ID)
			// Clear assignment to prevent re-slashing on next tick
			s.tasks.AssignProvider(task.ID, types.Address{}, 0)
		}
	}
}
