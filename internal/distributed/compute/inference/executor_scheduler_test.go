// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Tests for SchedulerInferenceExecutor (slice 3): AI inference routed through
// the distributed compute scheduler for quorum/optimistic verification instead
// of a single trusted local executor. Unit tests pin the spec construction and
// result mapping against a fake scheduler; the full-stack test drives the real
// scheduler + signing workers over the in-memory bus, including a byzantine
// worker that quorum must outvote.

package inference

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/distributed/compute/scheduler"
	"github.com/n42blockchain/N42/internal/distributed/compute/worker"
	"github.com/n42blockchain/N42/internal/distributed/coprocessor"
)

// a valid minimal WASM module header (\0asm + version).
var testWASM = []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}

// fakeScheduler records the submitted spec and returns a scripted result.
type fakeScheduler struct {
	lastSpec  *scheduler.TaskSpec
	result    *scheduler.TaskResult
	submitErr error
	waitErr   error
}

func (f *fakeScheduler) Submit(spec *scheduler.TaskSpec, submitter types.Address) (types.Hash, error) {
	f.lastSpec = spec
	if f.submitErr != nil {
		return types.Hash{}, f.submitErr
	}
	return crypto.Keccak256Hash(spec.Input), nil
}

func (f *fakeScheduler) WaitTask(id types.Hash, timeout time.Duration) (*scheduler.TaskResult, error) {
	if f.waitErr != nil {
		return nil, f.waitErr
	}
	return f.result, nil
}

func loadedExecutor(t *testing.T, fs taskScheduler) (*SchedulerInferenceExecutor, types.Hash) {
	t.Helper()
	modelHash := crypto.Keccak256Hash(testWASM)
	ex := NewSchedulerInferenceExecutor(fs, SchedulerExecutorConfig{
		Mode:       scheduler.VerifyQuorum,
		Redundancy: 3,
		FuelLimit:  1_000_000,
		MaxPrice:   900,
		Deadline:   2 * time.Second,
		Lease:      500 * time.Millisecond,
	})
	if err := ex.LoadModel(modelHash, testWASM); err != nil {
		t.Fatalf("load model: %v", err)
	}
	return ex, modelHash
}

// S1: a verified task maps to an InferenceResult carrying the worker output.
func TestSchedulerExecutorVerified(t *testing.T) {
	fs := &fakeScheduler{result: &scheduler.TaskResult{
		Status: coprocessor.TaskVerified,
		Output: []byte("prediction"),
	}}
	ex, modelHash := loadedExecutor(t, fs)

	res, err := ex.Execute(context.Background(), modelHash, []byte("features"))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !bytes.Equal(res.Output, []byte("prediction")) {
		t.Fatalf("output = %q", res.Output)
	}
	// Spec must carry the model as program + bytecode and the raw input.
	if fs.lastSpec.ProgramHash != modelHash {
		t.Fatalf("spec program = %s, want model %s", fs.lastSpec.ProgramHash.Hex(), modelHash.Hex())
	}
	if !bytes.Equal(fs.lastSpec.Bytecode, testWASM) {
		t.Fatalf("spec bytecode not the model module")
	}
	if !bytes.Equal(fs.lastSpec.Input, []byte("features")) {
		t.Fatalf("spec input = %q", fs.lastSpec.Input)
	}
	if fs.lastSpec.Mode != scheduler.VerifyQuorum || fs.lastSpec.Redundancy != 3 {
		t.Fatalf("spec mode/redundancy = %v/%d", fs.lastSpec.Mode, fs.lastSpec.Redundancy)
	}
}

// S2: a failed task becomes an executor error (no result leaks upstream).
func TestSchedulerExecutorFailed(t *testing.T) {
	fs := &fakeScheduler{result: &scheduler.TaskResult{
		Status: coprocessor.TaskFailed,
		Err:    "challenge upheld",
	}}
	ex, modelHash := loadedExecutor(t, fs)
	if _, err := ex.Execute(context.Background(), modelHash, []byte("x")); err == nil {
		t.Fatalf("failed task must surface an error")
	}
}

// S3: an unknown model is rejected before any scheduling happens.
func TestSchedulerExecutorUnknownModel(t *testing.T) {
	fs := &fakeScheduler{}
	ex, _ := loadedExecutor(t, fs)
	if _, err := ex.Execute(context.Background(), types.Hash{0xAB}, []byte("x")); err == nil {
		t.Fatalf("unknown model must error")
	}
	if fs.lastSpec != nil {
		t.Fatalf("must not submit a task for an unknown model")
	}
}

// S4: a wait timeout/expiry surfaces as an error.
func TestSchedulerExecutorExpired(t *testing.T) {
	fs := &fakeScheduler{result: &scheduler.TaskResult{Status: coprocessor.TaskExpired}}
	ex, modelHash := loadedExecutor(t, fs)
	if _, err := ex.Execute(context.Background(), modelHash, []byte("x")); err == nil {
		t.Fatalf("expired task must surface an error")
	}
}

// S5: full stack — precompile-facing InferenceService drives inference through
// the real scheduler and three signing workers over the bus; a byzantine
// worker returns garbage but quorum accepts the honest majority. Determinism:
// honest workers run the same deterministic Runner, so their outputs match.
func TestSchedulerExecutorFullStack(t *testing.T) {
	bus := worker.NewLocalBus()

	tasks := coprocessor.NewTaskManager(64, time.Hour)
	registry := coprocessor.NewProviderRegistry(1)
	market := coprocessor.NewMarketplace(registry)
	slasher := coprocessor.NewSlashManager(registry, 10)
	challenges := coprocessor.NewChallengeManager(60)
	re := worker.NewRemoteExecutor(bus)
	sched := scheduler.New(scheduler.Config{
		Tasks: tasks, Registry: registry, Market: market,
		Slasher: slasher, Challenges: challenges, Executor: re,
	})
	defer sched.Close()

	// Deterministic honest runner: output = keccak(bytecode || input), the same
	// function the WASM stub computes, so all honest workers agree.
	honest := worker.RunnerFunc(func(ctx context.Context, spec *scheduler.TaskSpec) ([]byte, error) {
		h := crypto.Keccak256Hash(spec.Bytecode, spec.Input)
		return h[:], nil
	})
	byzantine := worker.RunnerFunc(func(ctx context.Context, spec *scheduler.TaskSpec) ([]byte, error) {
		return []byte("garbage"), nil
	})

	runners := []worker.Runner{honest, honest, byzantine}
	for i, r := range runners {
		key, err := crypto.GenerateKey()
		if err != nil {
			t.Fatalf("key: %v", err)
		}
		a := crypto.PubkeyToAddress(key.PublicKey)
		w := worker.NewWorker(key, r, bus)
		if err := w.Start(); err != nil {
			t.Fatalf("start worker %d: %v", i, err)
		}
		defer w.Stop()
		if err := registry.Register(a, 100, []coprocessor.Capability{coprocessor.CapWASM}); err != nil {
			t.Fatalf("register %d: %v", i, err)
		}
		if err := sched.AddWorker(a, scheduler.ClassPhone, fmt.Sprintf("g%d", i)); err != nil {
			t.Fatalf("add worker %d: %v", i, err)
		}
	}

	ex := NewSchedulerInferenceExecutor(sched, SchedulerExecutorConfig{
		Mode:            scheduler.VerifyQuorum,
		Redundancy:      3,
		RequireDisperse: true,
		FuelLimit:       1_000_000,
		MaxPrice:        900,
		Deadline:        5 * time.Second,
		Lease:           2 * time.Second,
	})

	// Drive it through the real InferenceService, exactly as the precompile
	// does. The registry derives its own model hash (name:version:format);
	// the executor caches the WASM module under that same hash.
	models := NewModelRegistry()
	modelHash, err := models.Register("det-model", FormatCustom, uint64(len(testWASM)), "v1", []string{"test"})
	if err != nil {
		t.Fatalf("register model: %v", err)
	}
	if err := ex.LoadModel(modelHash, testWASM); err != nil {
		t.Fatalf("load model: %v", err)
	}
	svc := NewInferenceService(models)
	svc.SetExecutor(ex)

	reqID, err := svc.SubmitRequest(modelHash, []byte("features"), types.Address{0x01})
	if err != nil {
		t.Fatalf("submit request: %v", err)
	}
	result, err := svc.Execute(context.Background(), reqID)
	if err != nil {
		t.Fatalf("execute request: %v", err)
	}
	want := crypto.Keccak256Hash(testWASM, []byte("features"))
	if !bytes.Equal(result.Output, want[:]) {
		t.Fatalf("inference output = %x, want honest-majority %x", result.Output, want[:])
	}
}
