package coprocessor

import (
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
)

func TestMarketplaceSubmitBidAndSelect(t *testing.T) {
	pr := NewProviderRegistry(100)
	pr.Register(types.HexToAddress("0xa1"), 1000, []Capability{CapZK})
	pr.Register(types.HexToAddress("0xa2"), 1000, []Capability{CapZK})

	m := NewMarketplace(pr)
	taskID := types.HexToHash("0x1111")
	m.PublishTask(taskID)

	// Two bids
	_, err := m.SubmitBid(taskID, types.HexToAddress("0xa1"), 500, 10*time.Second)
	if err != nil {
		t.Fatalf("SubmitBid1: %v", err)
	}
	_, err = m.SubmitBid(taskID, types.HexToAddress("0xa2"), 300, 20*time.Second)
	if err != nil {
		t.Fatalf("SubmitBid2: %v", err)
	}

	if m.BidCount(taskID) != 2 {
		t.Fatalf("bid count = %d, want 2", m.BidCount(taskID))
	}

	// Select lowest price
	assignment, err := m.SelectWinner(taskID, StrategyLowestPrice)
	if err != nil {
		t.Fatalf("SelectWinner: %v", err)
	}
	if assignment.ProviderID != types.HexToAddress("0xa2") {
		t.Fatalf("winner = %v, want 0xa2", assignment.ProviderID)
	}
	if assignment.Price != 300 {
		t.Fatalf("price = %d, want 300", assignment.Price)
	}
}

func TestMarketplaceSelectFastestETA(t *testing.T) {
	pr := NewProviderRegistry(100)
	pr.Register(types.HexToAddress("0xb1"), 1000, nil)
	pr.Register(types.HexToAddress("0xb2"), 1000, nil)

	m := NewMarketplace(pr)
	taskID := types.HexToHash("0x2222")
	m.PublishTask(taskID)

	m.SubmitBid(taskID, types.HexToAddress("0xb1"), 500, 30*time.Second)
	m.SubmitBid(taskID, types.HexToAddress("0xb2"), 800, 5*time.Second)

	assignment, err := m.SelectWinner(taskID, StrategyFastestETA)
	if err != nil {
		t.Fatalf("SelectWinner: %v", err)
	}
	if assignment.ProviderID != types.HexToAddress("0xb2") {
		t.Fatalf("winner = %v, want 0xb2 (fastest ETA)", assignment.ProviderID)
	}
}

func TestMarketplaceSelectHighestReputation(t *testing.T) {
	pr := NewProviderRegistry(100)
	pr.Register(types.HexToAddress("0xc1"), 1000, nil)
	pr.Register(types.HexToAddress("0xc2"), 1000, nil)
	pr.UpdateReputation(types.HexToAddress("0xc2"), 3000) // 5000 + 3000 = 8000

	m := NewMarketplace(pr)
	taskID := types.HexToHash("0x3333")
	m.PublishTask(taskID)

	m.SubmitBid(taskID, types.HexToAddress("0xc1"), 100, time.Second)
	m.SubmitBid(taskID, types.HexToAddress("0xc2"), 200, time.Second)

	assignment, err := m.SelectWinner(taskID, StrategyHighestReputation)
	if err != nil {
		t.Fatalf("SelectWinner: %v", err)
	}
	if assignment.ProviderID != types.HexToAddress("0xc2") {
		t.Fatalf("winner = %v, want 0xc2 (highest reputation)", assignment.ProviderID)
	}
}

func TestMarketplaceDoubleAssign(t *testing.T) {
	pr := NewProviderRegistry(100)
	pr.Register(types.HexToAddress("0xd1"), 1000, nil)

	m := NewMarketplace(pr)
	taskID := types.HexToHash("0x4444")
	m.PublishTask(taskID)
	m.SubmitBid(taskID, types.HexToAddress("0xd1"), 100, time.Second)
	m.SelectWinner(taskID, StrategyLowestPrice)

	// Second selection should fail
	_, err := m.SelectWinner(taskID, StrategyLowestPrice)
	if err != ErrTaskAlreadyAssigned {
		t.Fatalf("expected ErrTaskAlreadyAssigned, got %v", err)
	}
}

func TestMarketplaceSuspendedProviderCantBid(t *testing.T) {
	pr := NewProviderRegistry(100)
	addr := types.HexToAddress("0xe1")
	pr.Register(addr, 1000, nil)
	pr.UpdateStatus(addr, ProviderSuspended)

	m := NewMarketplace(pr)
	taskID := types.HexToHash("0x5555")
	m.PublishTask(taskID)

	_, err := m.SubmitBid(taskID, addr, 100, time.Second)
	if err != ErrProviderSuspended {
		t.Fatalf("expected ErrProviderSuspended, got %v", err)
	}
}

func TestMarketplaceNoBids(t *testing.T) {
	m := NewMarketplace(NewProviderRegistry(100))
	taskID := types.HexToHash("0x6666")
	m.PublishTask(taskID)

	_, err := m.SelectWinner(taskID, StrategyLowestPrice)
	if err != ErrNoEligibleBids {
		t.Fatalf("expected ErrNoEligibleBids, got %v", err)
	}
}

func TestMarketplaceBidIDUniqueness(t *testing.T) {
	pr := NewProviderRegistry(100)
	addr1 := types.HexToAddress("0xf1")
	addr2 := types.HexToAddress("0xf2")
	pr.Register(addr1, 1000, nil)
	pr.Register(addr2, 1000, nil)

	m := NewMarketplace(pr)
	taskID := types.HexToHash("0x7777")
	m.PublishTask(taskID)

	bidID1, err := m.SubmitBid(taskID, addr1, 100, time.Second)
	if err != nil {
		t.Fatalf("SubmitBid1: %v", err)
	}
	bidID2, err := m.SubmitBid(taskID, addr2, 200, time.Second)
	if err != nil {
		t.Fatalf("SubmitBid2: %v", err)
	}

	if bidID1 == bidID2 {
		t.Fatal("different providers should produce different bid IDs for the same task")
	}
}

func TestMarketplaceGetAssignment(t *testing.T) {
	pr := NewProviderRegistry(100)
	addr := types.HexToAddress("0xe2")
	pr.Register(addr, 1000, nil)

	m := NewMarketplace(pr)
	taskID := types.HexToHash("0x8888")
	m.PublishTask(taskID)
	m.SubmitBid(taskID, addr, 150, time.Second)

	// Before selection, no assignment
	_, ok := m.GetAssignment(taskID)
	if ok {
		t.Fatal("should have no assignment before SelectWinner")
	}

	assignment, err := m.SelectWinner(taskID, StrategyLowestPrice)
	if err != nil {
		t.Fatalf("SelectWinner: %v", err)
	}

	// After selection, assignment should exist and match
	got, ok := m.GetAssignment(taskID)
	if !ok {
		t.Fatal("assignment should exist after SelectWinner")
	}
	if got.ProviderID != addr {
		t.Fatalf("assignment provider = %v, want %v", got.ProviderID, addr)
	}
	if got.Price != assignment.Price {
		t.Fatalf("assignment price mismatch: got %d, want %d", got.Price, assignment.Price)
	}
}
