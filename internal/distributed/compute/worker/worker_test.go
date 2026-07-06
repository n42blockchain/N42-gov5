// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Acceptance tests for slice 2 of the distributed compute rollout
// (docs/distributed-compute-design-2026-07-06.md §9): the worker runtime
// (availability-gated, result-signing) and the RemoteExecutor that carries
// scheduler attempts over a Transport. The full scheduler pipeline from
// slice 1 is exercised end-to-end over the in-memory bus in W6.

package worker

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/distributed/compute/scheduler"
	"github.com/n42blockchain/N42/internal/distributed/coprocessor"
)

// genWorkerKey creates a fresh worker identity (key + derived address).
func genWorkerKey(t *testing.T) (*ecdsa.PrivateKey, types.Address) {
	t.Helper()
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return key, crypto.PubkeyToAddress(key.PublicKey)
}

// echoRunner returns a fixed output regardless of input.
func echoRunner(out []byte) Runner {
	return RunnerFunc(func(ctx context.Context, spec *scheduler.TaskSpec) ([]byte, error) {
		return out, nil
	})
}

func testSpec() *scheduler.TaskSpec {
	return &scheduler.TaskSpec{
		ProgramHash: crypto.Keccak256Hash([]byte("prog")),
		Bytecode:    []byte("\x00asm"),
		FuncName:    "run",
		Input:       []byte("in"),
		FuelLimit:   1000,
		MaxPrice:    100,
		Deadline:    2 * time.Second,
		Lease:       500 * time.Millisecond,
		Mode:        scheduler.VerifyQuorum,
		Redundancy:  1,
		Capability:  coprocessor.CapWASM,
	}
}

// W1: end-to-end remote execution with a valid signature — the executor
// returns the worker's output after verifying the signature binds
// (worker address, request ID, output).
func TestRemoteExecuteSigned(t *testing.T) {
	bus := NewLocalBus()
	key, addr := genWorkerKey(t)

	w := NewWorker(key, echoRunner([]byte("hello")), bus)
	if err := w.Start(); err != nil {
		t.Fatalf("start worker: %v", err)
	}
	defer w.Stop()

	re := NewRemoteExecutor(bus)
	out, err := re.Execute(context.Background(), addr, testSpec())
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !bytes.Equal(out, []byte("hello")) {
		t.Fatalf("output = %q", out)
	}
}

// W2: a worker signing with a key that does not match its claimed address is
// rejected — forged results never reach the scheduler as successes.
func TestForgedSignatureRejected(t *testing.T) {
	bus := NewLocalBus()
	realKey, realAddr := genWorkerKey(t)
	wrongKey, _ := genWorkerKey(t)

	// Malicious worker: listens as realAddr but signs with wrongKey.
	w := NewWorker(realKey, echoRunner([]byte("evil")), bus)
	w.overrideSigningKey(wrongKey) // test hook
	if err := w.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer w.Stop()

	re := NewRemoteExecutor(bus)
	_, err := re.Execute(context.Background(), realAddr, testSpec())
	if err == nil {
		t.Fatalf("forged signature must be rejected")
	}
}

// W3: availability gating — an unavailable worker (not charging / metered
// network) refuses the task with a typed busy error; the executor surfaces
// it so the scheduler can reassign immediately instead of burning the lease.
func TestAvailabilityGate(t *testing.T) {
	bus := NewLocalBus()
	key, addr := genWorkerKey(t)

	available := false
	var mu sync.Mutex
	w := NewWorker(key, echoRunner([]byte("x")), bus)
	w.SetAvailability(func() bool {
		mu.Lock()
		defer mu.Unlock()
		return available
	})
	if err := w.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer w.Stop()

	re := NewRemoteExecutor(bus)
	if _, err := re.Execute(context.Background(), addr, testSpec()); err == nil {
		t.Fatalf("unavailable worker must refuse the task")
	}

	mu.Lock()
	available = true
	mu.Unlock()
	out, err := re.Execute(context.Background(), addr, testSpec())
	if err != nil {
		t.Fatalf("execute after becoming available: %v", err)
	}
	if !bytes.Equal(out, []byte("x")) {
		t.Fatalf("output = %q", out)
	}
}

// W4: offline worker — nobody is listening at the address; the executor
// blocks until the caller's ctx (the scheduler's lease) expires.
func TestOfflineWorkerTimesOut(t *testing.T) {
	bus := NewLocalBus()
	_, ghost := genWorkerKey(t)

	re := NewRemoteExecutor(bus)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := re.Execute(ctx, ghost, testSpec())
	if err == nil {
		t.Fatalf("offline worker must time out")
	}
	if time.Since(start) < 90*time.Millisecond {
		t.Fatalf("returned before ctx expiry: %v", time.Since(start))
	}
}

// W5: late responses to an abandoned request are dropped and do not bleed
// into a subsequent request from the same executor.
func TestLateResponseIgnored(t *testing.T) {
	bus := NewLocalBus()
	slowKey, slowAddr := genWorkerKey(t)
	fastKey, fastAddr := genWorkerKey(t)

	slow := NewWorker(slowKey, RunnerFunc(func(ctx context.Context, spec *scheduler.TaskSpec) ([]byte, error) {
		time.Sleep(200 * time.Millisecond) // deliberately ignore ctx: a rude worker
		return []byte("stale"), nil
	}), bus)
	if err := slow.Start(); err != nil {
		t.Fatalf("start slow: %v", err)
	}
	defer slow.Stop()

	fast := NewWorker(fastKey, echoRunner([]byte("fresh")), bus)
	if err := fast.Start(); err != nil {
		t.Fatalf("start fast: %v", err)
	}
	defer fast.Stop()

	re := NewRemoteExecutor(bus)

	// First request times out while the slow worker is still computing.
	ctx1, cancel1 := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel1()
	if _, err := re.Execute(ctx1, slowAddr, testSpec()); err == nil {
		t.Fatalf("expected timeout")
	}

	// Second request to the fast worker must return the fast worker's output,
	// even when the stale response arrives in between.
	out, err := re.Execute(context.Background(), fastAddr, testSpec())
	if err != nil {
		t.Fatalf("second execute: %v", err)
	}
	if !bytes.Equal(out, []byte("fresh")) {
		t.Fatalf("output = %q (stale response bled through?)", out)
	}
}

// W6: full pipeline — slice-1 scheduler drives a quorum task over the bus
// against three signing workers; majority accepted, poisoner penalized.
func TestSchedulerOverBusQuorum(t *testing.T) {
	bus := NewLocalBus()

	tasks := coprocessor.NewTaskManager(64, time.Hour)
	registry := coprocessor.NewProviderRegistry(1)
	market := coprocessor.NewMarketplace(registry)
	slasher := coprocessor.NewSlashManager(registry, 10)
	challenges := coprocessor.NewChallengeManager(60)

	re := NewRemoteExecutor(bus)
	sched := scheduler.New(scheduler.Config{
		Tasks:      tasks,
		Registry:   registry,
		Market:     market,
		Slasher:    slasher,
		Challenges: challenges,
		Executor:   re,
	})
	defer sched.Close()

	outputs := [][]byte{[]byte("agree"), []byte("agree"), []byte("poison")}
	var addrs []types.Address
	for i := 0; i < 3; i++ {
		key, a := genWorkerKey(t)
		w := NewWorker(key, echoRunner(outputs[i]), bus)
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
		addrs = append(addrs, a)
	}

	spec := testSpec()
	spec.Redundancy = 3
	spec.RequireDisperse = true
	spec.MaxPrice = 900
	id, err := sched.Submit(spec, types.Address{0xEE})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	res, err := sched.WaitTask(id, 10*time.Second)
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if res.Status != coprocessor.TaskVerified {
		t.Fatalf("status = %v (err=%s)", res.Status, res.Err)
	}
	if !bytes.Equal(res.Output, []byte("agree")) {
		t.Fatalf("output = %q", res.Output)
	}
	// The poisoner (index 2) must not be among the paid workers.
	for _, w := range res.Workers {
		if w == addrs[2] {
			t.Fatalf("poisoner was accepted into the majority")
		}
	}
}

// W7: concurrent remote executions over one shared bus stay isolated
// (request/response correlation under load). Run with -race.
func TestConcurrentRemoteExecutions(t *testing.T) {
	bus := NewLocalBus()

	const n = 8
	addrs := make([]types.Address, n)
	for i := 0; i < n; i++ {
		key, a := genWorkerKey(t)
		out := []byte(fmt.Sprintf("out-%d", i))
		w := NewWorker(key, echoRunner(out), bus)
		if err := w.Start(); err != nil {
			t.Fatalf("start %d: %v", i, err)
		}
		defer w.Stop()
		addrs[i] = a
	}

	re := NewRemoteExecutor(bus)
	var wg sync.WaitGroup
	errs := make([]error, n*4)
	for j := 0; j < n*4; j++ {
		wg.Add(1)
		go func(j int) {
			defer wg.Done()
			i := j % n
			spec := testSpec()
			spec.Input = []byte(fmt.Sprintf("req-%d", j))
			out, err := re.Execute(context.Background(), addrs[i], spec)
			if err != nil {
				errs[j] = err
				return
			}
			if want := fmt.Sprintf("out-%d", i); string(out) != want {
				errs[j] = fmt.Errorf("cross-talk: got %q want %q", out, want)
			}
		}(j)
	}
	wg.Wait()
	for j, err := range errs {
		if err != nil {
			t.Fatalf("request %d: %v", j, err)
		}
	}
}

// W8: the wire round-trip preserves every spec field the runner depends on
// (determinism prerequisite: what the worker executes is exactly what the
// scheduler dispatched).
func TestSpecWireFidelity(t *testing.T) {
	bus := NewLocalBus()
	key, addr := genWorkerKey(t)

	var got *scheduler.TaskSpec
	var mu sync.Mutex
	w := NewWorker(key, RunnerFunc(func(ctx context.Context, spec *scheduler.TaskSpec) ([]byte, error) {
		mu.Lock()
		got = spec
		mu.Unlock()
		return []byte("ok"), nil
	}), bus)
	if err := w.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer w.Stop()

	spec := testSpec()
	spec.Input = []byte{0x00, 0xFF, 0x10}
	spec.FuncName = "custom_entry"
	spec.FuelLimit = 424242

	re := NewRemoteExecutor(bus)
	if _, err := re.Execute(context.Background(), addr, spec); err != nil {
		t.Fatalf("execute: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if got == nil {
		t.Fatalf("runner never saw the spec")
	}
	if got.ProgramHash != spec.ProgramHash || !bytes.Equal(got.Bytecode, spec.Bytecode) ||
		got.FuncName != spec.FuncName || !bytes.Equal(got.Input, spec.Input) ||
		got.FuelLimit != spec.FuelLimit {
		t.Fatalf("spec mutated in transit: got %+v want %+v", got, spec)
	}
}
