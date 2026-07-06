// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// SchedulerInferenceExecutor implements InferenceExecutor by routing each
// inference through the distributed compute scheduler (slice 3 of
// docs/distributed-compute-design-2026-07-06.md §6.1). Instead of a single
// trusted local WASM run, each request becomes a scheduler task verified by
// quorum (majority of r replicas, for the phone fleet) or optimistically with
// a challenge window (for staked IDC) — the same verification the rest of the
// compute network uses. The precompile at 0x0301 → InferenceService → this
// executor → scheduler → workers is unchanged above the executor seam.
//
// Determinism is the correctness premise: the model WASM module + input run
// identically on every worker (sandbox: no fs/net/randomness, fuel-metered),
// so honest replicas agree and quorum/referee can verify. The model bytecode
// is carried in the task spec so any worker can execute without a prior
// model-distribution step.

package inference

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/distributed/compute/scheduler"
	"github.com/n42blockchain/N42/internal/distributed/coprocessor"
)

// taskScheduler is the subset of *scheduler.Scheduler this executor needs.
// Narrow interface keeps the executor unit-testable without the full stack.
type taskScheduler interface {
	Submit(spec *scheduler.TaskSpec, submitter types.Address) (types.Hash, error)
	WaitTask(id types.Hash, timeout time.Duration) (*scheduler.TaskResult, error)
}

// SchedulerExecutorConfig parameterizes how inference tasks are scheduled.
type SchedulerExecutorConfig struct {
	Mode            scheduler.VerifyMode // quorum (phones) or optimistic (IDC)
	Redundancy      int                  // quorum replica count (odd)
	RequireDisperse bool                 // quorum: spread replicas across groups
	ChallengeWindow time.Duration        // optimistic
	FuelLimit       uint64
	MaxPrice        uint64
	Deadline        time.Duration
	Lease           time.Duration
	HedgeDelay      time.Duration
	// FuncName is the WASM export invoked on each worker. Defaults to "infer".
	FuncName string
	// Submitter is the address charged/credited for tasks. Defaults to zero
	// (the inference precompile is a system caller).
	Submitter types.Address
}

// SchedulerInferenceExecutor runs inference as verified distributed compute.
type SchedulerInferenceExecutor struct {
	sched taskScheduler
	cfg   SchedulerExecutorConfig

	mu         sync.RWMutex
	modelCache map[types.Hash][]byte // modelHash → WASM module bytes
}

// NewSchedulerInferenceExecutor creates an executor backed by the scheduler.
func NewSchedulerInferenceExecutor(sched taskScheduler, cfg SchedulerExecutorConfig) *SchedulerInferenceExecutor {
	if cfg.FuncName == "" {
		cfg.FuncName = "infer"
	}
	if cfg.Deadline <= 0 {
		cfg.Deadline = 30 * time.Second
	}
	if cfg.Lease <= 0 || cfg.Lease > cfg.Deadline {
		cfg.Lease = cfg.Deadline / 2
	}
	if cfg.MaxPrice == 0 {
		cfg.MaxPrice = 1
	}
	return &SchedulerInferenceExecutor{
		sched:      sched,
		cfg:        cfg,
		modelCache: make(map[types.Hash][]byte),
	}
}

// LoadModel caches a model's WASM module so inference tasks can carry it to
// workers. Same content-addressing discipline as WASMExecutor.
func (e *SchedulerInferenceExecutor) LoadModel(modelHash types.Hash, wasmBytes []byte) error {
	if len(wasmBytes) < 4 || wasmBytes[0] != 0x00 || wasmBytes[1] != 0x61 ||
		wasmBytes[2] != 0x73 || wasmBytes[3] != 0x6d {
		return fmt.Errorf("scheduler inference: invalid WASM module for %s", modelHash.Hex())
	}
	cp := make([]byte, len(wasmBytes))
	copy(cp, wasmBytes)
	e.mu.Lock()
	e.modelCache[modelHash] = cp
	e.mu.Unlock()
	return nil
}

// UnloadModel drops a cached model.
func (e *SchedulerInferenceExecutor) UnloadModel(modelHash types.Hash) {
	e.mu.Lock()
	delete(e.modelCache, modelHash)
	e.mu.Unlock()
}

// Execute satisfies InferenceExecutor: schedule the inference as a verified
// task and map the terminal result back to an InferenceResult.
func (e *SchedulerInferenceExecutor) Execute(ctx context.Context, modelHash types.Hash, input []byte) (*InferenceResult, error) {
	e.mu.RLock()
	wasmBytes, ok := e.modelCache[modelHash]
	e.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("scheduler inference: model %s not loaded", modelHash.Hex())
	}

	spec := &scheduler.TaskSpec{
		ProgramHash:     modelHash,
		Bytecode:        wasmBytes,
		FuncName:        e.cfg.FuncName,
		Input:           input,
		FuelLimit:       e.cfg.FuelLimit,
		MaxPrice:        e.cfg.MaxPrice,
		Deadline:        e.cfg.Deadline,
		Lease:           e.cfg.Lease,
		HedgeDelay:      e.cfg.HedgeDelay,
		Mode:            e.cfg.Mode,
		Redundancy:      e.cfg.Redundancy,
		RequireDisperse: e.cfg.RequireDisperse,
		ChallengeWindow: e.cfg.ChallengeWindow,
		Capability:      coprocessor.CapWASM,
	}

	id, err := e.sched.Submit(spec, e.cfg.Submitter)
	if err != nil {
		return nil, fmt.Errorf("scheduler inference: submit: %w", err)
	}

	// Wait budget: the caller's ctx deadline if set, else the task deadline
	// plus slack for the challenge window and settlement.
	wait := e.cfg.Deadline + e.cfg.ChallengeWindow + time.Second
	if dl, hasDL := ctx.Deadline(); hasDL {
		if d := time.Until(dl); d > 0 && d < wait {
			wait = d
		}
	}

	res, err := e.sched.WaitTask(id, wait)
	if err != nil {
		return nil, fmt.Errorf("scheduler inference: wait: %w", err)
	}
	if res.Status != coprocessor.TaskVerified {
		msg := res.Err
		if msg == "" {
			msg = res.Status.String()
		}
		return nil, fmt.Errorf("scheduler inference: task %s: %s", id.Hex()[:10], msg)
	}

	return &InferenceResult{
		RequestID:    id,
		Output:       res.Output,
		Confidence:   1.0, // deterministic verified execution
		ModelVersion: "scheduler-v1",
		GasUsed:      e.cfg.FuelLimit,
		ComputedAt:   time.Now(),
	}, nil
}
