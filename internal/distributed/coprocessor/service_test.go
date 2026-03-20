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

	taskID, err := svc.SubmitTask(ph, []byte("input-data"), types.Address{})
	if err != nil {
		t.Fatalf("SubmitTask: %v", err)
	}

	ok, err := svc.SubmitProof(taskID, []byte("proof-data"), []byte("output-data"))
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
	tm.UpdateStatus(id, TaskVerified, []byte("p"), []byte("o"), "")

	time.Sleep(5 * time.Millisecond)
	pruned := tm.Prune(1 * time.Millisecond)
	if pruned != 1 {
		t.Fatalf("pruned = %d, want 1", pruned)
	}
	if tm.TotalCount() != 0 {
		t.Fatal("should have 0 tasks after prune")
	}
}
