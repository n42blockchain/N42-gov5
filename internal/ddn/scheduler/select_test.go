package scheduler

import (
	"context"
	chain "github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/ddn/provider"
	d "github.com/n42blockchain/N42/internal/ddn/types"
	"github.com/n42blockchain/N42/internal/distributed/coprocessor"
	"strings"
	"testing"
	"time"
)

func TestSelectionUsesCapabilitiesReputationPriceAndStatus(t *testing.T) {
	registry := coprocessor.NewProviderRegistry(1)
	var offers []Offer
	for i := byte(1); i <= 4; i++ {
		addr := chain.Address{i}
		cap := coprocessor.CapAI
		if i == 1 {
			cap = coprocessor.CapWASM
		}
		if err := registry.Register(addr, 10, []coprocessor.Capability{cap}); err != nil {
			t.Fatal(err)
		}
		id := provider.Identity{DID: "did:n42:" + strings.ToLower(addr.Hex()), ModelHash: chain.Hash{i}, Tasks: []string{"node.anomaly"}, Schemas: []string{"health-v1"}}
		offers = append(offers, Offer{Address: addr, Backend: provider.Stub{ID: id, Result: d.DecisionResult{Label: "NORMAL"}}, Price: uint64(i), ETA: time.Millisecond})
	}
	registry.UpdateReputation(chain.Address{2}, -5000)
	s := Scheduler{Registry: registry, MinReputation: 1000, Strategy: coprocessor.StrategyLowestPrice}
	r := d.DecisionRequest{Task: "node.anomaly", SchemaID: "health-v1", Quorum: 1, MaxCost: "10", MaxLatencyMs: 10, PrivacyMode: "public"}
	chosen, err := s.Select(r, offers)
	if err != nil {
		t.Fatal(err)
	}
	if chosen.Price.Uint64() != 3 {
		t.Fatal("capability/reputation/price filters ignored")
	}
	registry.UpdateStatus(chain.Address{3}, coprocessor.ProviderSuspended)
	if _, err := chosen.Providers[0].Decide(context.Background(), r, ""); err == nil {
		t.Fatal("suspended provider executed")
	}
	r.Quorum = 2
	if _, err = s.Select(r, offers); err == nil {
		t.Fatal("insufficient eligible quorum accepted")
	}
	r.Quorum = 1
	r.MaxCost = "3"
	if _, err = s.Select(r, offers); err == nil {
		t.Fatal("budget exceeded")
	}
	r.MaxCost = "10"
	r.ModelRequirements.ModelHash = chain.Hash{4}
	chosen, err = s.Select(r, offers)
	if err != nil || chosen.Price.Uint64() != 4 {
		t.Fatal("model filter ignored")
	}
}
