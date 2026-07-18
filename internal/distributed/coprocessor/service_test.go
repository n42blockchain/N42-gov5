package coprocessor

import (
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
)

func testConfig() *conf.CoprocessorCfg {
	cfg := conf.DefaultCoprocessorCfg()
	cfg.Enabled = true
	cfg.TaskTimeoutSec = 2
	cfg.PruneIntervalSec = 1
	return &cfg
}

func TestRegistryLifecycle(t *testing.T) {
	r := NewRegistry()
	h := types.HexToHash("0x1234")
	vk := []byte("test-vk")

	if err := r.Register(h, vk, "test"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if r.Count() != 1 {
		t.Fatalf("Count = %d, want 1", r.Count())
	}

	p, ok := r.Get(h)
	if !ok || p.Name != "test" {
		t.Fatal("Get failed")
	}

	// Duplicate
	if err := r.Register(h, vk, "dup"); err == nil {
		t.Fatal("expected error on duplicate")
	}

	// Empty vk
	if err := r.Register(types.HexToHash("0x5678"), nil, "bad"); err == nil {
		t.Fatal("expected error on nil vk")
	}

	if err := r.Unregister(h); err != nil {
		t.Fatalf("Unregister: %v", err)
	}
	if r.Count() != 0 {
		t.Fatal("Count should be 0")
	}
}

func TestTaskSubmitAndGet(t *testing.T) {
	tm := NewTaskManager(10, 5*time.Second)
	ph := types.HexToHash("0xaabb")
	addr := types.HexToAddress("0x1111")

	id, err := tm.Submit(ph, []byte("input"), addr)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	task, ok := tm.GetTask(id)
	if !ok {
		t.Fatal("task not found")
	}
	if task.Status != TaskPending {
		t.Fatalf("status = %v, want Pending", task.Status)
	}
	if task.Submitter != addr {
		t.Fatal("wrong submitter")
	}
}

func TestTaskMaxPending(t *testing.T) {
	tm := NewTaskManager(2, 5*time.Second)
	ph := types.HexToHash("0xcc")
	addr := types.HexToAddress("0x22")

	tm.Submit(ph, []byte("a"), addr)
	tm.Submit(ph, []byte("b"), addr)
	_, err := tm.Submit(ph, []byte("c"), addr)
	if err == nil {
		t.Fatal("expected max pending error")
	}
}

func TestTaskExpireStale(t *testing.T) {
	tm := NewTaskManager(10, 1*time.Millisecond)
	ph := types.HexToHash("0xdd")
	addr := types.HexToAddress("0x33")

	tm.Submit(ph, []byte("x"), addr)
	time.Sleep(5 * time.Millisecond)

	expired := tm.ExpireStale()
	if expired != 1 {
		t.Fatalf("expired = %d, want 1", expired)
	}
}

func TestServiceLifecycle(t *testing.T) {
	cfg := testConfig()
	svc, err := NewService(cfg)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	svc.Start()
	time.Sleep(50 * time.Millisecond)
	svc.Stop()
	svc.Stop() // double stop safe
}

func TestServiceSubmitAndVerify(t *testing.T) {
	cfg := testConfig()
	svc, _ := NewService(cfg)
	svc.Start()
	defer svc.Stop()

	ph := types.HexToHash("0xee")
	svc.Registry().Register(ph, []byte("vk"), "test-prog")
	// ZK tier is fail-closed: wire a backend that accepts the blob.
	if err := svc.Verifier().SetZKBlobVerifier(func(*Program, []byte, types.Hash, types.Hash) (bool, error) {
		return true, nil
	}); err != nil {
		t.Fatalf("SetZKBlobVerifier: %v", err)
	}

	taskID, err := svc.SubmitTask(ph, []byte("input-data"), types.Address{})
	if err != nil {
		t.Fatalf("SubmitTask: %v", err)
	}

	task0, _ := svc.Tasks().GetTask(taskID)
	outputs := []byte("output-data")
	proof := BuildZKProofEnvelope(task0.ID, ph, task0.InputHash, outputs, []byte("proof-data"))
	ok, err := svc.SubmitProof(taskID, proof, outputs)
	if err != nil || !ok {
		t.Fatalf("SubmitProof: ok=%v err=%v", ok, err)
	}

	task, _ := svc.Tasks().GetTask(taskID)
	if task.Status != TaskVerified {
		t.Fatalf("status = %v, want Verified", task.Status)
	}
}

func TestServiceRejectUnregisteredProgram(t *testing.T) {
	cfg := testConfig()
	svc, _ := NewService(cfg)
	svc.Start()
	defer svc.Stop()

	_, err := svc.SubmitTask(types.HexToHash("0xff"), []byte("x"), types.Address{})
	if err != ErrProgramNotRegistered {
		t.Fatalf("expected ErrProgramNotRegistered, got %v", err)
	}
}

func TestTaskPrune(t *testing.T) {
	tm := NewTaskManager(10, 1*time.Millisecond)
	ph := types.HexToHash("0xaa")
	id, _ := tm.Submit(ph, []byte("x"), types.Address{})
	tm.TransitionToProving(id) // Pending → Proving
	tm.UpdateStatus(id, TaskVerified, []byte("p"), []byte("o"), "") // Proving → Verified

	time.Sleep(5 * time.Millisecond)
	pruned := tm.Prune(1 * time.Millisecond)
	if pruned != 1 {
		t.Fatalf("pruned = %d, want 1", pruned)
	}
	if tm.TotalCount() != 0 {
		t.Fatal("should have 0 tasks after prune")
	}
}

func TestTaskStateTransitionValidation(t *testing.T) {
	// Test the validTransition function through UpdateStatus
	tm := NewTaskManager(10, 5*time.Second)
	ph := types.HexToHash("0xbb")

	// Submit a task (Pending)
	id, _ := tm.Submit(ph, []byte("input"), types.Address{})

	// Invalid: Pending → Verified (must go through Proving first)
	err := tm.UpdateStatus(id, TaskVerified, nil, nil, "")
	if err == nil {
		t.Fatal("expected error for Pending → Verified transition")
	}

	// Valid: Pending → Proving
	_, err = tm.TransitionToProving(id)
	if err != nil {
		t.Fatalf("Pending → Proving: %v", err)
	}

	// Invalid: Proving → Pending (not allowed)
	err = tm.UpdateStatus(id, TaskPending, nil, nil, "")
	if err == nil {
		t.Fatal("expected error for Proving → Pending transition")
	}

	// Valid: Proving → Verified
	err = tm.UpdateStatus(id, TaskVerified, []byte("proof"), []byte("output"), "")
	if err != nil {
		t.Fatalf("Proving → Verified: %v", err)
	}

	// Terminal: Verified → anything should fail
	err = tm.UpdateStatus(id, TaskFailed, nil, nil, "retry")
	if err == nil {
		t.Fatal("expected error: Verified is a terminal state")
	}
}

func TestServiceClaimTask(t *testing.T) {
	cfg := testConfig()
	cfg.MinProviderStake = 1000 // low minimum so tests don't need ETH-scale stake
	svc, _ := NewService(cfg)
	svc.Start()
	defer svc.Stop()

	ph := types.HexToHash("0x1234")
	svc.Registry().Register(ph, []byte("vk"), "prog")

	providerAddr := types.HexToAddress("0xabcd")
	if err := svc.RegisterProvider(providerAddr, 5000, []Capability{CapZK}); err != nil {
		t.Fatalf("RegisterProvider: %v", err)
	}

	taskID, _ := svc.SubmitTask(ph, []byte("data"), types.Address{})

	// Claim the task
	if err := svc.ClaimTask(taskID, providerAddr); err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}

	task, _ := svc.Tasks().GetTask(taskID)
	if task.AssignedProvider != providerAddr {
		t.Fatalf("assigned provider = %v, want %v", task.AssignedProvider, providerAddr)
	}

	// Claiming again should fail: task already assigned
	providerAddr2 := types.HexToAddress("0xffff")
	svc.RegisterProvider(providerAddr2, 5000, []Capability{CapZK})
	err := svc.ClaimTask(taskID, providerAddr2)
	if err != ErrTaskAlreadyAssigned {
		t.Fatalf("expected ErrTaskAlreadyAssigned, got %v", err)
	}
}

func TestServiceRegisterUnregisterProvider(t *testing.T) {
	cfg := testConfig()
	cfg.MinProviderStake = 1000 // low minimum so tests don't need ETH-scale stake
	svc, _ := NewService(cfg)
	svc.Start()
	defer svc.Stop()

	addr := types.HexToAddress("0x9999")
	caps := []Capability{CapWASM, CapGeneral}

	// Register
	if err := svc.RegisterProvider(addr, 2000, caps); err != nil {
		t.Fatalf("RegisterProvider: %v", err)
	}

	p, ok := svc.Providers().Get(addr)
	if !ok {
		t.Fatal("provider not found after registration")
	}
	if p.Status != ProviderActive {
		t.Fatalf("status = %v, want Active", p.Status)
	}

	// Duplicate registration should fail
	err := svc.RegisterProvider(addr, 2000, caps)
	if err != ErrProviderExists {
		t.Fatalf("expected ErrProviderExists, got %v", err)
	}

	// Unregister
	if err := svc.UnregisterProvider(addr); err != nil {
		t.Fatalf("UnregisterProvider: %v", err)
	}
	_, ok = svc.Providers().Get(addr)
	if ok {
		t.Fatal("provider should be removed after unregistration")
	}

	// Unregister again should fail
	err = svc.UnregisterProvider(addr)
	if err != ErrProviderNotFound {
		t.Fatalf("expected ErrProviderNotFound, got %v", err)
	}
}
