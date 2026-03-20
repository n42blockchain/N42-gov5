// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package coprocessor

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/n42blockchain/N42/common/types"
)

// TaskManager manages the lifecycle of compute tasks.
// Thread-safe for concurrent access.
type TaskManager struct {
	mu          sync.RWMutex
	tasks       map[types.Hash]*Task
	nonce       atomic.Uint64
	maxPending  int
	taskTimeout time.Duration
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

	// Check pending limit
	pendingCount := 0
	for _, t := range tm.tasks {
		if t.Status == TaskPending || t.Status == TaskProving {
			pendingCount++
		}
	}
	if pendingCount >= tm.maxPending {
		return types.Hash{}, fmt.Errorf("coprocessor: max pending tasks reached (%d)", tm.maxPending)
	}

	nonce := tm.nonce.Add(1)
	taskID := ComputeTaskID(programHash, input, nonce)

	inputCopy := make([]byte, len(input))
	copy(inputCopy, input)

	tm.tasks[taskID] = &Task{
		ID:          taskID,
		ProgramHash: programHash,
		InputHash:   types.BytesToHash(input),
		Input:       inputCopy,
		Status:      TaskPending,
		Submitter:   submitter,
		CreatedAt:   time.Now(),
	}
	return taskID, nil
}

// GetTask returns a task by ID.
func (tm *TaskManager) GetTask(id types.Hash) (*Task, bool) {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	t, ok := tm.tasks[id]
	return t, ok
}

// UpdateStatus transitions a task to a new status with optional proof data.
func (tm *TaskManager) UpdateStatus(id types.Hash, status TaskStatus, proofData, publicOutputs []byte, errMsg string) error {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	t, ok := tm.tasks[id]
	if !ok {
		return fmt.Errorf("task %s not found", id.Hex())
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

// ListByStatus returns all tasks with the given status.
func (tm *TaskManager) ListByStatus(status TaskStatus) []*Task {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	var result []*Task
	for _, t := range tm.tasks {
		if t.Status == status {
			result = append(result, t)
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
			expired++
		}
	}
	return expired
}

// PendingCount returns the number of pending/proving tasks.
func (tm *TaskManager) PendingCount() int {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	count := 0
	for _, t := range tm.tasks {
		if t.Status == TaskPending || t.Status == TaskProving {
			count++
		}
	}
	return count
}

// TotalCount returns the total number of tracked tasks.
func (tm *TaskManager) TotalCount() int {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	return len(tm.tasks)
}
