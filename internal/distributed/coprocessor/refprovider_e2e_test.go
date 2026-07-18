// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// End-to-end: the reference provider runs a REAL WASM program on the wazero
// engine and drives the full submit → claim → execute → optimistic-verify →
// settle loop the coprocessor's pieces implement but that nothing exercised
// together before. Plus the adversarial branch: a challenger who recomputes a
// different result gets the provider slashed.

package coprocessor

import (
	"context"
	"encoding/binary"
	"os"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/distributed/compute/wasm"
)

// facWasm is a real compiled WASM factorial module (export "fac-ssa", i64→i64)
// borrowed from wazero's corpus and used by the wasm engine's own tests.
func facWasm(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("../compute/wasm/testdata/fac.wasm")
	if err != nil {
		t.Fatalf("read fac.wasm: %v", err)
	}
	return b
}

func i64BE(v uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, v)
	return b
}

func newE2EService(t *testing.T) *Service {
	t.Helper()
	cfg := testConfig()
	cfg.MinProviderStake = 1        // low bar for the test provider
	cfg.OptimisticChallengeSec = 1  // short window so finalize fires fast
	cfg.PruneIntervalSec = 1        // maintenance ticks every second
	svc, err := NewService(cfg)
	if err != nil {
		t.Fatalf("service: %v", err)
	}
	svc.Start()
	t.Cleanup(svc.Stop)
	return svc
}

// TestReferenceProviderHappyPath: submit an optimistic task, the provider runs
// the WASM program, submits the result, and after the challenge window the
// task finalizes to Verified and the provider is rewarded.
func TestReferenceProviderHappyPath(t *testing.T) {
	ctx := context.Background()
	svc := newE2EService(t)

	bytecode := facWasm(t)
	programHash := crypto.Keccak256Hash(bytecode)
	if err := svc.RegisterProgram(programHash, []byte("fac-vk"), "factorial"); err != nil {
		t.Fatalf("register program: %v", err)
	}

	engine := wasm.NewEngine(wasm.NewWazeroRuntime(ctx), 16, 30, 1.0)
	defer engine.Close(ctx)
	provAddr := types.HexToAddress("0xC0DE01")
	if err := svc.RegisterProvider(provAddr, 100, []Capability{CapWASM}); err != nil {
		t.Fatalf("register provider: %v", err)
	}
	prov := NewReferenceProvider(svc, provAddr, engine, "fac-ssa")
	prov.RegisterBytecode(programHash, bytecode)

	submitter := types.HexToAddress("0xABCD01")
	taskID, err := svc.SubmitTaskWithTier(programHash, i64BE(5), submitter, TierOptimistic, 50)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	out, err := prov.Serve(ctx, taskID, 10_000_000)
	if err != nil {
		t.Fatalf("provider serve: %v", err)
	}
	if len(out) != 8 || binary.BigEndian.Uint64(out) != 120 {
		t.Fatalf("fac(5) via provider = %x, want 120", out)
	}

	// Immediately after Serve the task is optimistic-verified.
	if snap, _ := svc.Tasks().GetTaskSnapshot(taskID); snap.Status != TaskOptimisticVerified {
		t.Fatalf("status after serve = %s, want OptimisticVerified", snap.Status)
	}

	// After the challenge window, the maintenance loop finalizes it to
	// Verified and settles the reward.
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		if snap, _ := svc.Tasks().GetTaskSnapshot(taskID); snap.Status == TaskVerified {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	snap, _ := svc.Tasks().GetTaskSnapshot(taskID)
	if snap.Status != TaskVerified {
		t.Fatalf("task never finalized: status = %s", snap.Status)
	}
	rewarded := false
	for _, r := range svc.Slasher().RewardHistory() {
		if r.Provider == provAddr && r.TaskID == taskID {
			rewarded = true
		}
	}
	if !rewarded {
		t.Fatal("provider was not rewarded on finalize — settlement did not close the loop")
	}
}

// TestReferenceProviderChallengeSlash: a challenger disputes the provider's
// optimistic result within the window; resolving the challenge upheld slashes
// the provider (Verify-or-Slash).
func TestReferenceProviderChallengeSlash(t *testing.T) {
	ctx := context.Background()
	svc := newE2EService(t)
	// Long window so the challenge lands before auto-finalize.
	svc.cfg.OptimisticChallengeSec = 30

	bytecode := facWasm(t)
	programHash := crypto.Keccak256Hash(bytecode)
	if err := svc.RegisterProgram(programHash, []byte("fac-vk"), "factorial"); err != nil {
		t.Fatal(err)
	}
	engine := wasm.NewEngine(wasm.NewWazeroRuntime(ctx), 16, 30, 1.0)
	defer engine.Close(ctx)
	provAddr := types.HexToAddress("0xC0DE02")
	if err := svc.RegisterProvider(provAddr, 100, []Capability{CapWASM}); err != nil {
		t.Fatal(err)
	}
	prov := NewReferenceProvider(svc, provAddr, engine, "fac-ssa")
	prov.RegisterBytecode(programHash, bytecode)

	taskID, err := svc.SubmitTaskWithTier(programHash, i64BE(6), types.HexToAddress("0xABCD02"), TierOptimistic, 50)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := prov.Serve(ctx, taskID, 10_000_000); err != nil {
		t.Fatalf("serve: %v", err)
	}

	// A challenger disputes the result.
	challenger := types.HexToAddress("0xBAD001")
	challengeID, err := svc.SubmitChallenge(taskID, challenger, 100, "recomputed a different output")
	if err != nil {
		t.Fatalf("submit challenge: %v", err)
	}
	if snap, _ := svc.Tasks().GetTaskSnapshot(taskID); snap.Status != TaskChallenged {
		t.Fatalf("status after challenge = %s, want Challenged", snap.Status)
	}

	// Resolve the challenge as upheld and slash the provider (Verify-or-Slash).
	if err := svc.Challenges().Resolve(challengeID, true); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	svc.Slasher().Slash(provAddr, SlashChallengeUpheld, taskID)

	slashed := false
	for _, s := range svc.Slasher().SlashHistory() {
		if s.Provider == provAddr && s.Condition == SlashChallengeUpheld {
			slashed = true
		}
	}
	if !slashed {
		t.Fatal("provider was not slashed after an upheld challenge")
	}
}

// TestResidentProviderAutoServe: with AttachProvider running, a submitted
// optimistic task is claimed, executed on the real wazero engine (bytecode
// fetched from the program registry, not supplied to the provider), and
// finalized to Verified with the provider rewarded — no manual Serve call.
func TestResidentProviderAutoServe(t *testing.T) {
	ctx := context.Background()
	svc := newE2EService(t)

	bytecode := facWasm(t)
	programHash := crypto.Keccak256Hash(bytecode)
	if err := svc.RegisterProgram(programHash, []byte("fac-vk"), "factorial"); err != nil {
		t.Fatalf("register program: %v", err)
	}
	// Bytecode travels through the registry — the resident provider must
	// pick it up from there.
	if err := svc.Registry().SetBytecode(programHash, bytecode); err != nil {
		t.Fatalf("set bytecode: %v", err)
	}

	engine := wasm.NewEngine(wasm.NewWazeroRuntime(ctx), 16, 30, 1.0)
	provAddr := types.HexToAddress("0xC0DE02")
	if err := svc.RegisterProvider(provAddr, 100, []Capability{CapWASM}); err != nil {
		t.Fatalf("register provider: %v", err)
	}
	prov := NewReferenceProvider(svc, provAddr, engine, "fac-ssa")
	engineClosed := false
	svc.AttachProvider(prov, 100*time.Millisecond, 10_000_000, func() {
		engineClosed = true
		_ = engine.Close(context.Background())
	})

	taskID, err := svc.SubmitTaskWithTier(programHash, i64BE(6), types.HexToAddress("0xABCD02"), TierOptimistic, 50)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	// The resident loop must pick the task up and drive it to Verified.
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if snap, _ := svc.Tasks().GetTaskSnapshot(taskID); snap.Status == TaskVerified {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	snap, _ := svc.Tasks().GetTaskSnapshot(taskID)
	if snap.Status != TaskVerified {
		t.Fatalf("task not auto-served to Verified: status = %s (err=%q)", snap.Status, snap.Error)
	}
	if snap.AssignedProvider != provAddr {
		t.Fatalf("assigned provider = %s, want %s", snap.AssignedProvider.Hex(), provAddr.Hex())
	}
	if len(snap.PublicOutputs) != 8 || binary.BigEndian.Uint64(snap.PublicOutputs) != 720 {
		t.Fatalf("fac(6) via resident provider = %x, want 720", snap.PublicOutputs)
	}
	rewarded := false
	for _, r := range svc.Slasher().RewardHistory() {
		if r.Provider == provAddr && r.TaskID == taskID {
			rewarded = true
		}
	}
	if !rewarded {
		t.Fatal("resident provider was not rewarded on finalize")
	}

	// Stopping the service must terminate the loop and run the cleanup.
	svc.Stop()
	if !engineClosed {
		t.Fatal("cleanup (engine close) did not run on service stop")
	}
}

// TestRegistryBytecodeHashCheck: bytecode that does not hash to the program
// hash must be rejected, and lookups miss until valid bytecode is attached.
func TestRegistryBytecodeHashCheck(t *testing.T) {
	svc := newE2EService(t)

	bytecode := facWasm(t)
	programHash := crypto.Keccak256Hash(bytecode)
	if err := svc.RegisterProgram(programHash, []byte("vk"), "p"); err != nil {
		t.Fatalf("register program: %v", err)
	}

	if err := svc.Registry().SetBytecode(programHash, []byte("not-the-program")); err == nil {
		t.Fatal("mismatched bytecode should be rejected")
	}
	if _, ok := svc.Registry().Bytecode(programHash); ok {
		t.Fatal("no bytecode should be attached after a rejected set")
	}
	if err := svc.Registry().SetBytecode(programHash, bytecode); err != nil {
		t.Fatalf("valid bytecode rejected: %v", err)
	}
	got, ok := svc.Registry().Bytecode(programHash)
	if !ok || len(got) != len(bytecode) {
		t.Fatal("bytecode roundtrip failed")
	}
}

// TestResidentProviderSkipsNonOptimisticTier: a default-tier (ZK) task must
// NOT be touched by the resident WASM provider — submitting raw output as a
// ZK proof would drive the honest submitter's task to Failed (regression for
// the f4de645b tier-blindness bug).
func TestResidentProviderSkipsNonOptimisticTier(t *testing.T) {
	ctx := context.Background()
	svc := newE2EService(t)

	bytecode := facWasm(t)
	programHash := crypto.Keccak256Hash(bytecode)
	if err := svc.RegisterProgram(programHash, []byte("fac-vk"), "factorial"); err != nil {
		t.Fatalf("register program: %v", err)
	}
	if err := svc.Registry().SetBytecode(programHash, bytecode); err != nil {
		t.Fatalf("set bytecode: %v", err)
	}

	engine := wasm.NewEngine(wasm.NewWazeroRuntime(ctx), 16, 30, 1.0)
	provAddr := types.HexToAddress("0xC0DE03")
	if err := svc.RegisterProvider(provAddr, 100, []Capability{CapWASM}); err != nil {
		t.Fatalf("register provider: %v", err)
	}
	prov := NewReferenceProvider(svc, provAddr, engine, "fac-ssa")
	svc.AttachProvider(prov, 100*time.Millisecond, 10_000_000, func() { _ = engine.Close(context.Background()) })

	// Default-tier submit (TierZK).
	taskID, err := svc.SubmitTask(programHash, i64BE(5), types.HexToAddress("0xABCD03"))
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	// Give the loop several polls; the task must stay Pending and unassigned.
	time.Sleep(700 * time.Millisecond)
	snap, ok := svc.Tasks().GetTaskSnapshot(taskID)
	if !ok {
		t.Fatal("task vanished")
	}
	if snap.Status != TaskPending {
		t.Fatalf("ZK task was touched by resident provider: status = %s (want Pending)", snap.Status)
	}
	if snap.AssignedProvider != (types.Address{}) {
		t.Fatalf("ZK task was claimed by resident provider: %s", snap.AssignedProvider.Hex())
	}
}

// TestResidentProviderReleasesClaimOnExecFailure: a task whose input the
// program cannot execute must not leave the provider holding the claim into
// expiry (which slashExpiredAssignments would then slash). The provider
// releases the claim and is never slashed (regression for the f4de645b
// claim-leak/self-slash bug).
func TestResidentProviderReleasesClaimOnExecFailure(t *testing.T) {
	ctx := context.Background()
	svc := newE2EService(t)

	bytecode := facWasm(t)
	programHash := crypto.Keccak256Hash(bytecode)
	if err := svc.RegisterProgram(programHash, []byte("fac-vk"), "factorial"); err != nil {
		t.Fatalf("register program: %v", err)
	}
	if err := svc.Registry().SetBytecode(programHash, bytecode); err != nil {
		t.Fatalf("set bytecode: %v", err)
	}

	engine := wasm.NewEngine(wasm.NewWazeroRuntime(ctx), 16, 30, 1.0)
	provAddr := types.HexToAddress("0xC0DE04")
	if err := svc.RegisterProvider(provAddr, 100, []Capability{CapWASM}); err != nil {
		t.Fatalf("register provider: %v", err)
	}
	prov := NewReferenceProvider(svc, provAddr, engine, "fac-ssa")

	// fac-ssa expects an 8-byte i64 argument; a 3-byte input makes Execute fail.
	taskID, err := svc.SubmitTaskWithTier(programHash, []byte{1, 2, 3}, types.HexToAddress("0xABCD04"), TierOptimistic, 50)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	// One manual Serve must fail and release the claim.
	if _, err := prov.Serve(ctx, taskID, 10_000_000); err == nil {
		t.Fatal("Serve should fail on malformed input")
	}
	snap, _ := svc.Tasks().GetTaskSnapshot(taskID)
	if snap.AssignedProvider != (types.Address{}) {
		t.Fatalf("claim not released after exec failure: still assigned to %s", snap.AssignedProvider.Hex())
	}
	if snap.Status != TaskPending {
		t.Fatalf("task status = %s, want Pending (available for retry)", snap.Status)
	}

	// Drive expiry + maintenance; the provider must NOT be slashed, because it
	// no longer holds the claim.
	engine.Close(context.Background())
	time.Sleep(1500 * time.Millisecond)
	for _, ev := range svc.Slasher().SlashHistory() {
		if ev.Provider == provAddr {
			t.Fatalf("provider was slashed for a task it released: %+v", ev)
		}
	}
}

// TestClaimIfPendingAtomic: two concurrent claims on the same task — exactly
// one wins, the other gets ErrTaskAlreadyAssigned (regression for the
// check-then-assign TOCTOU).
func TestClaimIfPendingAtomic(t *testing.T) {
	svc := newE2EService(t)
	bytecode := facWasm(t)
	programHash := crypto.Keccak256Hash(bytecode)
	if err := svc.RegisterProgram(programHash, []byte("vk"), "p"); err != nil {
		t.Fatalf("register: %v", err)
	}
	taskID, err := svc.SubmitTaskWithTier(programHash, i64BE(5), types.HexToAddress("0xABCD05"), TierOptimistic, 50)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	a := types.HexToAddress("0xAA")
	b := types.HexToAddress("0xBB")
	errA := svc.Tasks().ClaimIfPending(taskID, a, 0)
	errB := svc.Tasks().ClaimIfPending(taskID, b, 0)
	if (errA == nil) == (errB == nil) {
		t.Fatalf("exactly one claim should win: errA=%v errB=%v", errA, errB)
	}
	snap, _ := svc.Tasks().GetTaskSnapshot(taskID)
	winner := a
	if errB == nil {
		winner = b
	}
	if snap.AssignedProvider != winner {
		t.Fatalf("assigned = %s, want winner %s", snap.AssignedProvider.Hex(), winner.Hex())
	}
}
