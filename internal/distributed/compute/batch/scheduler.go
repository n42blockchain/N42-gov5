// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package batch

import (
	"fmt"
	"sync"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/log"
)

// Scheduler manages batch job lifecycle: splitting input data into map tasks,
// distributing them to providers, tracking progress, and handling retries.
type Scheduler struct {
	mu           sync.RWMutex
	jobs         map[types.Hash]*Job
	maxParallel  int
	maxJobTasks  int
}

// NewScheduler creates a batch scheduler with the given limits.
func NewScheduler(maxJobTasks, maxParallel int) *Scheduler {
	return &Scheduler{
		jobs:        make(map[types.Hash]*Job),
		maxParallel: maxParallel,
		maxJobTasks: maxJobTasks,
	}
}

// SubmitJob registers a new batch job and splits it into map tasks.
func (s *Scheduler) SubmitJob(job *Job, partitions []DataRef) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.jobs[job.ID]; exists {
		return fmt.Errorf("batch: job %s already exists", job.ID.Hex())
	}

	if len(partitions) > s.maxJobTasks {
		return fmt.Errorf("batch: too many partitions (%d > %d)", len(partitions), s.maxJobTasks)
	}

	if len(partitions) == 0 {
		return fmt.Errorf("batch: at least one partition required")
	}

	// Create map tasks for each partition
	for i, part := range partitions {
		job.AddMapTask(part, i)
	}

	job.Status = JobMapping
	s.jobs[job.ID] = job

	log.Debug("Batch job submitted",
		"jobID", job.ID.Hex()[:10],
		"mapTasks", len(partitions),
		"parallelism", job.Parallelism,
	)
	return nil
}

// CompleteMapTask marks a map task as completed with the given output.
func (s *Scheduler) CompleteMapTask(jobID, taskID types.Hash, output DataRef) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	job, ok := s.jobs[jobID]
	if !ok {
		return fmt.Errorf("batch: job %s not found", jobID.Hex())
	}

	job.mu.Lock()
	defer job.mu.Unlock()

	for _, t := range job.MapTasks {
		if t.ID == taskID {
			t.Status = JobCompleted
			t.Output = output
			return nil
		}
	}
	return fmt.Errorf("batch: task %s not found in job %s", taskID.Hex(), jobID.Hex())
}

// FailMapTask marks a map task as failed. Retries if under the retry limit.
func (s *Scheduler) FailMapTask(jobID, taskID types.Hash, errMsg string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	job, ok := s.jobs[jobID]
	if !ok {
		return fmt.Errorf("batch: job %s not found", jobID.Hex())
	}

	job.mu.Lock()
	defer job.mu.Unlock()

	for _, t := range job.MapTasks {
		if t.ID == taskID {
			t.Status = JobFailed
			t.Error = errMsg
			return nil
		}
	}
	return fmt.Errorf("batch: task %s not found in job %s", taskID.Hex(), jobID.Hex())
}

// CheckProgress examines a job and transitions it to the reduce phase if all
// maps are complete, or marks it as failed if any map failed permanently.
func (s *Scheduler) CheckProgress(jobID types.Hash) (JobStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	job, ok := s.jobs[jobID]
	if !ok {
		return 0, fmt.Errorf("batch: job %s not found", jobID.Hex())
	}

	if job.HasFailedMaps() {
		job.Status = JobFailed
		job.Error = "one or more map tasks failed"
		return JobFailed, nil
	}

	if job.AllMapsComplete() {
		if job.ReduceHash != (types.Hash{}) {
			job.Status = JobReducing
			return JobReducing, nil
		}
		// No reduce phase — job is complete
		job.Status = JobCompleted
		return JobCompleted, nil
	}

	return JobMapping, nil
}

// CompleteJob marks a job as completed with the final output.
func (s *Scheduler) CompleteJob(jobID types.Hash, output DataRef) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	job, ok := s.jobs[jobID]
	if !ok {
		return fmt.Errorf("batch: job %s not found", jobID.Hex())
	}

	job.Status = JobCompleted
	job.Output = output
	return nil
}

// GetJob returns a job by ID.
func (s *Scheduler) GetJob(jobID types.Hash) (*Job, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	j, ok := s.jobs[jobID]
	return j, ok
}

// PendingMapTasks returns map tasks that are ready to be executed for a job.
func (s *Scheduler) PendingMapTasks(jobID types.Hash) []*MapTask {
	s.mu.RLock()
	defer s.mu.RUnlock()

	job, ok := s.jobs[jobID]
	if !ok {
		return nil
	}

	job.mu.RLock()
	defer job.mu.RUnlock()

	var pending []*MapTask
	for _, t := range job.MapTasks {
		if t.Status == JobPending {
			pending = append(pending, t)
		}
	}
	return pending
}

// NextBatch returns the next batch of map tasks to execute, respecting parallelism.
func (s *Scheduler) NextBatch(jobID types.Hash) []*MapTask {
	pending := s.PendingMapTasks(jobID)

	s.mu.RLock()
	job, ok := s.jobs[jobID]
	s.mu.RUnlock()
	if !ok {
		return nil
	}

	limit := job.Parallelism
	if limit <= 0 {
		limit = s.maxParallel
	}
	if len(pending) > limit {
		pending = pending[:limit]
	}
	return pending
}

// ListJobs returns all tracked jobs.
func (s *Scheduler) ListJobs() []*Job {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]*Job, 0, len(s.jobs))
	for _, j := range s.jobs {
		result = append(result, j)
	}
	return result
}

// JobCount returns the total number of tracked jobs.
func (s *Scheduler) JobCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.jobs)
}
