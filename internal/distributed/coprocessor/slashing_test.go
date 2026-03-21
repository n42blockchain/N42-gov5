package coprocessor

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

func TestSlashTimeout(t *testing.T) {
	pr := NewProviderRegistry(100)
	addr := types.HexToAddress("0xaa")
	pr.Register(addr, 10000, nil)

	sm := NewSlashManager(pr, 10) // 10%
	taskID := types.HexToHash("0xbb")

	sm.Slash(addr, SlashTimeout, taskID)

	p, _ := pr.Get(addr)
	if p.Stake != 9000 { // 10000 - 10% = 9000
		t.Fatalf("stake = %d, want 9000", p.Stake)
	}
	if p.TasksFailed != 1 {
		t.Fatalf("tasksFailed = %d, want 1", p.TasksFailed)
	}
	if p.Reputation >= 5000 {
		t.Fatalf("reputation = %d, should be < 5000 after slash", p.Reputation)
	}

	events := sm.SlashHistory()
	if len(events) != 1 {
		t.Fatalf("slash events = %d, want 1", len(events))
	}
	if events[0].Amount != 1000 {
		t.Fatalf("slash amount = %d, want 1000", events[0].Amount)
	}
}

func TestSlashChallengeUpheld(t *testing.T) {
	pr := NewProviderRegistry(100)
	addr := types.HexToAddress("0xcc")
	pr.Register(addr, 10000, nil)

	sm := NewSlashManager(pr, 10)
	sm.Slash(addr, SlashChallengeUpheld, types.HexToHash("0xdd"))

	p, _ := pr.Get(addr)
	if p.Status != ProviderSlashed {
		t.Fatalf("status = %v, want Slashed", p.Status)
	}
}

func TestSlashReward(t *testing.T) {
	pr := NewProviderRegistry(100)
	addr := types.HexToAddress("0xee")
	pr.Register(addr, 10000, nil)

	sm := NewSlashManager(pr, 10)
	taskID := types.HexToHash("0xff")

	sm.Reward(addr, 500, taskID)

	p, _ := pr.Get(addr)
	if p.TasksTotal != 1 {
		t.Fatalf("tasksTotal = %d, want 1", p.TasksTotal)
	}
	if p.TasksFailed != 0 {
		t.Fatalf("tasksFailed = %d, want 0", p.TasksFailed)
	}

	rewards := sm.RewardHistory()
	if len(rewards) != 1 {
		t.Fatalf("reward events = %d, want 1", len(rewards))
	}
	if rewards[0].Amount != 500 {
		t.Fatalf("reward amount = %d, want 500", rewards[0].Amount)
	}
}

func TestSlashTotalSlashedAndRewarded(t *testing.T) {
	pr := NewProviderRegistry(100)
	addr := types.HexToAddress("0x11")
	pr.Register(addr, 10000, nil)

	sm := NewSlashManager(pr, 10)

	sm.Slash(addr, SlashTimeout, types.HexToHash("0x01"))
	sm.Slash(addr, SlashInvalidProof, types.HexToHash("0x02"))
	sm.Reward(addr, 300, types.HexToHash("0x03"))
	sm.Reward(addr, 200, types.HexToHash("0x04"))

	if sm.TotalSlashed() != 1000+900 { // 10000*10%=1000, then 9000*10%=900
		t.Fatalf("total slashed = %d, want 1900", sm.TotalSlashed())
	}
	if sm.TotalRewarded() != 500 {
		t.Fatalf("total rewarded = %d, want 500", sm.TotalRewarded())
	}
}

func TestSlashNoDoubleSlash(t *testing.T) {
	// Simulate the double-slash fix: after slashing an expired task,
	// the provider assignment is cleared (set to zero address). A second
	// call to Slash for the same task should be a no-op because the
	// provider address is now zero and not found in the registry.
	pr := NewProviderRegistry(100)
	addr := types.HexToAddress("0x55")
	pr.Register(addr, 10000, nil)

	sm := NewSlashManager(pr, 10)
	taskID := types.HexToHash("0xabcd")

	// First slash
	sm.Slash(addr, SlashTimeout, taskID)
	eventsAfterFirst := len(sm.SlashHistory())
	if eventsAfterFirst != 1 {
		t.Fatalf("expected 1 slash event, got %d", eventsAfterFirst)
	}

	// Simulate assignment cleared (zero address is not registered)
	sm.Slash(types.Address{}, SlashTimeout, taskID)

	// Should still be only 1 event — zero address is not found, so Slash is a no-op
	if len(sm.SlashHistory()) != 1 {
		t.Fatalf("expected still 1 slash event after no-op, got %d", len(sm.SlashHistory()))
	}
}

func TestSlashUnknownProvider(t *testing.T) {
	pr := NewProviderRegistry(100)
	sm := NewSlashManager(pr, 10)

	// Slash an address that was never registered — should be silently ignored
	sm.Slash(types.HexToAddress("0xdeadbeef"), SlashTimeout, types.HexToHash("0x99"))

	if len(sm.SlashHistory()) != 0 {
		t.Fatalf("expected 0 slash events for unknown provider, got %d", len(sm.SlashHistory()))
	}
}
