package coprocessor

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

func TestProviderRegisterAndGet(t *testing.T) {
	pr := NewProviderRegistry(1000)
	addr := types.HexToAddress("0xp1")
	caps := []Capability{CapZK, CapWASM}

	if err := pr.Register(addr, 5000, caps); err != nil {
		t.Fatalf("Register: %v", err)
	}

	p, ok := pr.Get(addr)
	if !ok {
		t.Fatal("provider not found")
	}
	if p.Stake != 5000 {
		t.Fatalf("stake = %d, want 5000", p.Stake)
	}
	if p.Status != ProviderActive {
		t.Fatalf("status = %v, want Active", p.Status)
	}
	if p.Reputation != 5000 {
		t.Fatalf("reputation = %d, want 5000", p.Reputation)
	}
	if !p.HasCapability(CapZK) || !p.HasCapability(CapWASM) {
		t.Fatal("missing capability")
	}
	if p.HasCapability(CapAI) {
		t.Fatal("should not have AI capability")
	}
}

func TestProviderDuplicate(t *testing.T) {
	pr := NewProviderRegistry(1000)
	addr := types.HexToAddress("0xp2")

	pr.Register(addr, 5000, nil)
	err := pr.Register(addr, 5000, nil)
	if err != ErrProviderExists {
		t.Fatalf("expected ErrProviderExists, got %v", err)
	}
}

func TestProviderInsufficientStake(t *testing.T) {
	pr := NewProviderRegistry(1000)
	addr := types.HexToAddress("0xp3")

	err := pr.Register(addr, 500, nil)
	if err != ErrInsufficientStake {
		t.Fatalf("expected ErrInsufficientStake, got %v", err)
	}
}

func TestProviderUnregister(t *testing.T) {
	pr := NewProviderRegistry(100)
	addr := types.HexToAddress("0xp4")

	pr.Register(addr, 500, nil)
	if err := pr.Unregister(addr); err != nil {
		t.Fatalf("Unregister: %v", err)
	}

	_, ok := pr.Get(addr)
	if ok {
		t.Fatal("provider should be removed")
	}

	err := pr.Unregister(addr)
	if err != ErrProviderNotFound {
		t.Fatalf("expected ErrProviderNotFound, got %v", err)
	}
}

func TestProviderReputation(t *testing.T) {
	pr := NewProviderRegistry(100)
	addr := types.HexToAddress("0xp5")
	pr.Register(addr, 1000, nil)

	pr.UpdateReputation(addr, 1000)
	p, _ := pr.Get(addr)
	if p.Reputation != 6000 {
		t.Fatalf("reputation = %d, want 6000", p.Reputation)
	}

	// Cap at 10000
	pr.UpdateReputation(addr, 10000)
	p, _ = pr.Get(addr)
	if p.Reputation != 10000 {
		t.Fatalf("reputation = %d, want 10000 (cap)", p.Reputation)
	}

	// Floor at 0
	pr.UpdateReputation(addr, -20000)
	p, _ = pr.Get(addr)
	if p.Reputation != 0 {
		t.Fatalf("reputation = %d, want 0 (floor)", p.Reputation)
	}
}

func TestProviderDeductStake(t *testing.T) {
	pr := NewProviderRegistry(100)
	addr := types.HexToAddress("0xp6")
	pr.Register(addr, 1000, nil)

	pr.DeductStake(addr, 300)
	p, _ := pr.Get(addr)
	if p.Stake != 700 {
		t.Fatalf("stake = %d, want 700", p.Stake)
	}

	// Deduct more than stake → floor at 0
	pr.DeductStake(addr, 999)
	p, _ = pr.Get(addr)
	if p.Stake != 0 {
		t.Fatalf("stake = %d, want 0", p.Stake)
	}
}

func TestProviderListByCapability(t *testing.T) {
	pr := NewProviderRegistry(100)

	pr.Register(types.HexToAddress("0xa1"), 1000, []Capability{CapZK})
	pr.Register(types.HexToAddress("0xa2"), 1000, []Capability{CapWASM})
	pr.Register(types.HexToAddress("0xa3"), 1000, []Capability{CapZK, CapWASM})

	zkProviders := pr.ListByCapability(CapZK)
	if len(zkProviders) != 2 {
		t.Fatalf("ZK providers = %d, want 2", len(zkProviders))
	}

	wasmProviders := pr.ListByCapability(CapWASM)
	if len(wasmProviders) != 2 {
		t.Fatalf("WASM providers = %d, want 2", len(wasmProviders))
	}

	aiProviders := pr.ListByCapability(CapAI)
	if len(aiProviders) != 0 {
		t.Fatalf("AI providers = %d, want 0", len(aiProviders))
	}
}

func TestProviderActiveCount(t *testing.T) {
	pr := NewProviderRegistry(100)

	pr.Register(types.HexToAddress("0xb1"), 1000, nil)
	pr.Register(types.HexToAddress("0xb2"), 1000, nil)
	pr.Register(types.HexToAddress("0xb3"), 1000, nil)

	if pr.ActiveCount() != 3 {
		t.Fatalf("active = %d, want 3", pr.ActiveCount())
	}

	pr.UpdateStatus(types.HexToAddress("0xb2"), ProviderSuspended)
	if pr.ActiveCount() != 2 {
		t.Fatalf("active = %d, want 2", pr.ActiveCount())
	}
}

func TestProviderIncrementTasks(t *testing.T) {
	pr := NewProviderRegistry(100)
	addr := types.HexToAddress("0xc1")
	pr.Register(addr, 1000, nil)

	// Record one success and one failure
	pr.IncrementTasks(addr, false) // success
	pr.IncrementTasks(addr, false) // success
	pr.IncrementTasks(addr, true)  // failure

	p, _ := pr.Get(addr)
	if p.TasksTotal != 3 {
		t.Fatalf("tasksTotal = %d, want 3", p.TasksTotal)
	}
	if p.TasksFailed != 1 {
		t.Fatalf("tasksFailed = %d, want 1", p.TasksFailed)
	}
}

func TestProviderStatusString(t *testing.T) {
	cases := []struct {
		status ProviderStatus
		want   string
	}{
		{ProviderActive, "active"},
		{ProviderSuspended, "suspended"},
		{ProviderSlashed, "slashed"},
		{ProviderStatus(99), "unknown"},
	}
	for _, tc := range cases {
		if got := tc.status.String(); got != tc.want {
			t.Errorf("ProviderStatus(%d).String() = %q, want %q", tc.status, got, tc.want)
		}
	}
}
