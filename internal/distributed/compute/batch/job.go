// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

// Package batch implements MapReduce-style batch compute over content-addressed
// data (Lagrange MapReduce + Bacalhau Compute-over-Data pattern).
package batch

import (
	"sync"
	"time"

	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/common/types"
)

// JobStatus represents the lifecycle state of a batch job.
type JobStatus uint8

const (
	JobPending   JobStatus = 0
	JobMapping   JobStatus = 1
	JobReducing  JobStatus = 2
	JobCompleted JobStatus = 3
	JobFailed    JobStatus = 4
)

func (s JobStatus) String() string {
	switch s {
	case JobPending:
		return "pending"
	case JobMapping:
		return "mapping"
	case JobReducing:
		return "reducing"
	case JobCompleted:
		return "completed"
	case JobFailed:
		return "failed"
	default:
		return "unknown"
	}
}

// DataRef is a content-addressed reference to input/output data,
// compatible with the CAS precompile and universal storage resolver.
type DataRef struct {
	Hash types.Hash `json:"hash"` // Keccak256 content hash
	Size uint64     `json:"size"` // byte size
	Name string     `json:"name,omitempty"`
}

// SplitStrategy determines how input data is partitioned for map tasks.
type SplitStrategy uint8

const (
	SplitBySize  SplitStrategy = 0 // split data into chunks of N bytes
	SplitByCount SplitStrategy = 1 // split data into exactly N chunks
	SplitByRange SplitStrategy = 2 // split by key/offset ranges
)

// MapTask represents a single map operation within a batch job.
type MapTask struct {
	ID        types.Hash `json:"id"`
	JobID     types.Hash `json:"jobId"`
	Input     DataRef    `json:"input"`
	Output    DataRef    `json:"output,omitempty"`
	Status    JobStatus  `json:"status"`
	Partition int        `json:"partition"` // partition index
	Error     string     `json:"error,omitempty"`
}

// Job represents a batch compute job composed of map and reduce phases.
type Job struct {
	mu sync.RWMutex

	ID            types.Hash    `json:"id"`
	Submitter     types.Address `json:"submitter"`
	ProgramHash   types.Hash    `json:"programHash"`   // WASM module for map
	ReduceHash    types.Hash    `json:"reduceHash"`    // WASM module for reduce (optional)
	Status        JobStatus     `json:"status"`
	Input         DataRef       `json:"input"`         // original input data reference
	Output        DataRef       `json:"output,omitempty"`
	MapTasks      []*MapTask    `json:"mapTasks"`
	SplitStrategy SplitStrategy `json:"splitStrategy"`
	Parallelism   int           `json:"parallelism"`
	RetryLimit    int           `json:"retryLimit"`
	CreatedAt     time.Time     `json:"createdAt"`
	CompletedAt   time.Time     `json:"completedAt,omitempty"`
	Deadline      time.Time     `json:"deadline,omitempty"`
	Error         string        `json:"error,omitempty"`
}

// NewJob creates a new batch job with the given parameters.
func NewJob(submitter types.Address, programHash types.Hash, input DataRef, splitStrategy SplitStrategy, parallelism int) *Job {
	// Derive deterministic job ID
	data := make([]byte, 0, 84)
	data = append(data, submitter[:]...)
	data = append(data, programHash[:]...)
	data = append(data, input.Hash[:]...)
	jobID := crypto.Keccak256Hash(data)

	return &Job{
		ID:            jobID,
		Submitter:     submitter,
		ProgramHash:   programHash,
		Status:        JobPending,
		Input:         input,
		SplitStrategy: splitStrategy,
		Parallelism:   parallelism,
		RetryLimit:    3,
		CreatedAt:     time.Now(),
	}
}

// MapReduceJob extends Job with a reduce phase.
func NewMapReduceJob(submitter types.Address, mapProgram, reduceProgram types.Hash, input DataRef, splitStrategy SplitStrategy, parallelism int) *Job {
	job := NewJob(submitter, mapProgram, input, splitStrategy, parallelism)
	job.ReduceHash = reduceProgram
	return job
}

// IsExpired returns true if the job has passed its deadline.
func (j *Job) IsExpired() bool {
	j.mu.RLock()
	defer j.mu.RUnlock()
	if j.Deadline.IsZero() {
		return false
	}
	return time.Now().After(j.Deadline)
}

// SetDeadline sets a deadline for job completion.
func (j *Job) SetDeadline(d time.Time) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.Deadline = d
}

// AddMapTask adds a map task to the job.
func (j *Job) AddMapTask(input DataRef, partition int) types.Hash {
	j.mu.Lock()
	defer j.mu.Unlock()

	data := make([]byte, 0, 68)
	data = append(data, j.ID[:]...)
	data = append(data, input.Hash[:]...)
	data = append(data, byte(partition>>24), byte(partition>>16), byte(partition>>8), byte(partition))
	taskID := crypto.Keccak256Hash(data)

	j.MapTasks = append(j.MapTasks, &MapTask{
		ID:        taskID,
		JobID:     j.ID,
		Input:     input,
		Status:    JobPending,
		Partition: partition,
	})
	return taskID
}

// Progress returns the number of completed map tasks and total map tasks.
func (j *Job) Progress() (completed int, total int) {
	j.mu.RLock()
	defer j.mu.RUnlock()
	total = len(j.MapTasks)
	for _, t := range j.MapTasks {
		if t.Status == JobCompleted {
			completed++
		}
	}
	return
}

// AllMapsComplete returns true if all map tasks are completed.
func (j *Job) AllMapsComplete() bool {
	j.mu.RLock()
	defer j.mu.RUnlock()
	for _, t := range j.MapTasks {
		if t.Status != JobCompleted {
			return false
		}
	}
	return len(j.MapTasks) > 0
}

// HasFailedMaps returns true if any map task has failed.
func (j *Job) HasFailedMaps() bool {
	j.mu.RLock()
	defer j.mu.RUnlock()
	for _, t := range j.MapTasks {
		if t.Status == JobFailed {
			return true
		}
	}
	return false
}

// MapOutputs returns the output data references from all completed map tasks.
func (j *Job) MapOutputs() []DataRef {
	j.mu.RLock()
	defer j.mu.RUnlock()
	var outputs []DataRef
	for _, t := range j.MapTasks {
		if t.Status == JobCompleted {
			outputs = append(outputs, t.Output)
		}
	}
	return outputs
}
