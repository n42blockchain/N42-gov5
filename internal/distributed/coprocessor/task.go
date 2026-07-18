// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// TaskManager tracks compute tasks keyed by hash. Enforces maxPending
// and taskTimeout, maintains a cached pendingCount, and provides
// atomic status transitions via validTransition plus TransitionToProving
// and TransitionToChallenged to prevent TOCTOU races.

package coprocessor

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
)

// TaskManager manages the lifecycle of compute tasks.
// Thread-safe for concurrent access.
type TaskManager struct {
	mu           sync.RWMutex
	tasks        map[types.Hash]*Task
	nonce        atomic.Uint64
	maxPending   int
	taskTimeout  time.Duration
	pendingCount int // cached count of Pending + Proving tasks
}

// NewTaskManager creates a task manager with the given limits.
func NewTaskManager(maxPending int, taskTimeout time.Duration) *TaskManager {
	return &TaskManager{
		tasks:       make(map[types.Hash]*Task),
		maxPending:  maxPending,
		taskTimeout: taskTimeout,
	}
}

// Submit creates a new compute task and returns its ID.
func (tm *TaskManager) Submit(programHash types.Hash, input []byte, submitter types.Address) (types.Hash, error) {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	if tm.pendingCount >= tm.maxPending {
		return types.Hash{}, fmt.Errorf("coprocessor: max pending tasks reached (%d)", tm.maxPending)
	}

	nonce := tm.nonce.Add(1)
	taskID := ComputeTaskID(programHash, input, nonce)

	inputCopy := make([]byte, len(input))
	copy(inputCopy, input)

	tm.tasks[taskID] = &Task{
		ID:          taskID,
		ProgramHash: programHash,
		InputHash:   crypto.Keccak256Hash(input),
		Input:       inputCopy,
		Status:      TaskPending,
		Submitter:   submitter,
		CreatedAt:   time.Now(),
	}
	tm.pendingCount++
	return taskID, nil
}

// GetTask returns a task by ID.
// NOTE: The returned pointer references the live task object. Reading its fields
// concurrently with service operations is only safe when the service is stopped.
// Use GetTaskSnapshot for a race-free copy.
func (tm *TaskManager) GetTask(id types.Hash) (*Task, bool) {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	t, ok := tm.tasks[id]
	return t, ok
}

// GetTaskSnapshot returns a shallow copy of a task, safe to read without holding locks.
func (tm *TaskManager) GetTaskSnapshot(id types.Hash) (Task, bool) {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	t, ok := tm.tasks[id]
	if !ok {
		return Task{}, false
	}
	return *t, true
}

// validTransition checks if a status transition is allowed by the state machine.
func validTransition(from, to TaskStatus) bool {
	switch from {
	case TaskPending:
		return to == TaskProving || to == TaskExpired
	case TaskProving:
		return to == TaskVerified || to == TaskFailed || to == TaskExpired || to == TaskOptimisticVerified
	case TaskOptimisticVerified:
		return to == TaskVerified || to == TaskChallenged
	case TaskChallenged:
		return to == TaskVerified || to == TaskFailed
	case TaskVerified, TaskFailed, TaskExpired:
		return false // terminal states
	default:
		return false
	}
}

// UpdateStatus transitions a task to a new status with optional proof data.
// Returns error if the transition violates the state machine.
func (tm *TaskManager) UpdateStatus(id types.Hash, status TaskStatus, proofData, publicOutputs []byte, errMsg string) error {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	t, ok := tm.tasks[id]
	if !ok {
		return fmt.Errorf("task %s not found", id.Hex())
	}
	if !validTransition(t.Status, status) {
		return fmt.Errorf("invalid transition %s → %s for task %s", t.Status, status, id.Hex()[:10])
	}
	wasPending := t.Status == TaskPending || t.Status == TaskProving
	isPending := status == TaskPending || status == TaskProving
	if wasPending && !isPending {
		tm.pendingCount--
	} else if !wasPending && isPending {
		tm.pendingCount++
	}
	t.Status = status
	if len(proofData) > 0 {
		t.ProofData = proofData
	}
	if len(publicOutputs) > 0 {
		t.PublicOutputs = publicOutputs
	}
	if errMsg != "" {
		t.Error = errMsg
	}
	if status == TaskVerified || status == TaskFailed || status == TaskExpired {
		t.CompletedAt = time.Now()
		t.Input = nil // free memory after completion
	}
	return nil
}

// TransitionToProving atomically checks that the task is in Pending/Proving state
// and transitions it to Proving. Returns the programHash for verification.
// This prevents TOCTOU races where multiple proofs are submitted concurrently.
func (tm *TaskManager) TransitionToProving(id types.Hash) (types.Hash, error) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	t, ok := tm.tasks[id]
	if !ok {
		return types.Hash{}, ErrTaskNotFound
	}
	if t.Status != TaskPending && t.Status != TaskProving {
		return types.Hash{}, ErrTaskNotPending
	}
	t.Status = TaskProving
	return t.ProgramHash, nil
}

// SetOptimisticVerified marks a task as optimistic-verified and sets the challenge deadline.
func (tm *TaskManager) SetOptimisticVerified(id types.Hash, proofData, publicOutputs []byte, deadline time.Time) error {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	t, ok := tm.tasks[id]
	if !ok {
		return ErrTaskNotFound
	}
	if t.Status == TaskPending || t.Status == TaskProving {
		tm.pendingCount--
	}
	t.Status = TaskOptimisticVerified
	if len(proofData) > 0 {
		t.ProofData = proofData
	}
	if len(publicOutputs) > 0 {
		t.PublicOutputs = publicOutputs
	}
	t.ChallengeDeadline = deadline
	return nil
}

// TransitionToChallenged atomically checks that the task is in OptimisticVerified
// state and within the challenge window, then transitions it to Challenged.
// This prevents TOCTOU races in SubmitChallenge.
func (tm *TaskManager) TransitionToChallenged(id types.Hash, challenger types.Address) error {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	t, ok := tm.tasks[id]
	if !ok {
		return ErrTaskNotFound
	}
	if t.Status != TaskOptimisticVerified {
		return ErrTaskNotChallengeable
	}
	if time.Now().After(t.ChallengeDeadline) {
		return ErrChallengeWindowExpired
	}
	t.Status = TaskChallenged
	t.Challenger = challenger
	return nil
}

// AssignProvider assigns a provider and bid price to a task.
func (tm *TaskManager) AssignProvider(id types.Hash, provider types.Address, bidPrice uint64) error {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	t, ok := tm.tasks[id]
	if !ok {
		return ErrTaskNotFound
	}
	t.AssignedProvider = provider
	t.BidPrice = bidPrice
	return nil
}

// ClaimIfPending atomically assigns an unclaimed Pending task to provider. It
// is the race-free claim primitive: the check ("is it Pending and
// unassigned?") and the act (assign) happen under one lock hold, so two
// concurrent claimers cannot both succeed (the second gets ErrTaskAlreadyClaimed).
func (tm *TaskManager) ClaimIfPending(id types.Hash, provider types.Address, bidPrice uint64) error {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	t, ok := tm.tasks[id]
	if !ok {
		return ErrTaskNotFound
	}
	if t.Status != TaskPending {
		return ErrTaskNotPending
	}
	if t.AssignedProvider != (types.Address{}) {
		return ErrTaskAlreadyAssigned
	}
	t.AssignedProvider = provider
	t.BidPrice = bidPrice
	return nil
}

// ReleaseAssignment clears a Pending task's assignment if it is currently
// held by provider. Used when a provider claims a task but then cannot
// execute it (e.g. malformed input): releasing the claim keeps the task
// available for another provider and, crucially, stops the claimant from
// being timeout-slashed for a task it never could have completed. A no-op
// (nil) if the task is not Pending or not held by provider.
func (tm *TaskManager) ReleaseAssignment(id types.Hash, provider types.Address) error {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	t, ok := tm.tasks[id]
	if !ok {
		return ErrTaskNotFound
	}
	if t.Status == TaskPending && t.AssignedProvider == provider {
		t.AssignedProvider = types.Address{}
		t.BidPrice = 0
	}
	return nil
}

// SetTierAndBond sets a task's verification tier and bond before it is proven.
// Optimistic verification requires a non-zero bond (the value put at risk of
// slashing); this lets a submitter route a task through the bond+challenge tier
// instead of the default ZK tier. Only valid while Pending.
func (tm *TaskManager) SetTierAndBond(id types.Hash, tier VerificationTier, bond uint64) error {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	t, ok := tm.tasks[id]
	if !ok {
		return ErrTaskNotFound
	}
	if t.Status != TaskPending {
		return ErrTaskNotPending
	}
	t.VerificationTier = tier
	t.Bond = bond
	return nil
}

// ListByStatus returns snapshots of all tasks with the given status. Returning
// live pointers after releasing the manager lock made maintenance and resident
// provider loops race concurrent status/assignment updates.
func (tm *TaskManager) ListByStatus(status TaskStatus) []*Task {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	var result []*Task
	for _, t := range tm.tasks {
		if t.Status == status {
			snapshot := *t
			result = append(result, &snapshot)
		}
	}
	return result
}

// Prune removes tasks older than maxAge that are in a terminal state.
// Returns the number of pruned tasks.
func (tm *TaskManager) Prune(maxAge time.Duration) int {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	cutoff := time.Now().Add(-maxAge)
	pruned := 0
	for id, t := range tm.tasks {
		if (t.Status == TaskVerified || t.Status == TaskFailed || t.Status == TaskExpired) &&
			t.CompletedAt.Before(cutoff) {
			delete(tm.tasks, id)
			pruned++
		}
	}
	return pruned
}

// ExpireStale marks pending/proving tasks as expired if they exceed the timeout.
func (tm *TaskManager) ExpireStale() int {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	cutoff := time.Now().Add(-tm.taskTimeout)
	expired := 0
	for _, t := range tm.tasks {
		if (t.Status == TaskPending || t.Status == TaskProving) &&
			t.CreatedAt.Before(cutoff) {
			t.Status = TaskExpired
			t.CompletedAt = time.Now()
			t.Error = "task timed out"
			t.Input = nil
			tm.pendingCount--
			expired++
		}
	}
	return expired
}

// PendingCount returns the number of pending/proving tasks.
func (tm *TaskManager) PendingCount() int {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	return tm.pendingCount
}

// TotalCount returns the total number of tracked tasks.
func (tm *TaskManager) TotalCount() int {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	return len(tm.tasks)
}
