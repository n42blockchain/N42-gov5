package quorum

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	chain "github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/ddn/provider"
	d "github.com/n42blockchain/N42/internal/ddn/types"
)

func member(did, family, label string) Member {
	return Member{Provider: provider.Stub{ID: provider.Identity{DID: did, Family: family, ModelHash: chain.Hash{1}, Tasks: []string{"node.anomaly"}, Schemas: []string{"health-v1"}}, Result: d.DecisionResult{Label: label, ConfidencePPM: 900000}}}
}
func TestQuorumAgreementAndSplit(t *testing.T) {
	for _, label := range []string{"NORMAL", "NETWORK"} {
		g, err := New("did:n42:aggregate", []Member{member("a", "rules", "NORMAL"), member("b", "gli", label)}, 2)
		if err != nil {
			t.Fatal(err)
		}
		r, h, err := g.DecideWithEvidence(context.Background(), d.DecisionRequest{Quorum: 2, MaxCost: "0"}, "healthy")
		if err != nil {
			t.Fatal(err)
		}
		g.Wait()
		if h == (chain.Hash{}) || r.NeedEscalation != (label != "NORMAL") {
			t.Fatal("quorum policy failed")
		}
	}
}

type boundedProvider struct {
	id              provider.Identity
	active, maximum *atomic.Int32
	fail            bool
}

func (p boundedProvider) Identity() provider.Identity { return p.id }
func (p boundedProvider) Decide(ctx context.Context, _ d.DecisionRequest, _ string) (d.DecisionResult, error) {
	n := p.active.Add(1)
	defer p.active.Add(-1)
	for {
		old := p.maximum.Load()
		if old >= n || p.maximum.CompareAndSwap(old, n) {
			break
		}
	}
	if p.fail {
		return d.DecisionResult{}, errors.New("offline")
	}
	select {
	case <-ctx.Done():
		return d.DecisionResult{}, ctx.Err()
	case <-time.After(5 * time.Millisecond):
		return d.DecisionResult{Label: "NORMAL"}, nil
	}
}
func TestFanoutBoundsTimeoutAndPartialFailure(t *testing.T) {
	var active, maximum atomic.Int32
	members := []Member{}
	for _, m := range []Member{member("a", "rules", "NORMAL"), member("b", "gli", "NORMAL"), member("c", "other", "NORMAL")} {
		members = append(members, Member{Provider: boundedProvider{id: m.Provider.Identity(), active: &active, maximum: &maximum}})
	}
	g, _ := New("aggregate", members, 1)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
			defer cancel()
			r, _, err := g.DecideWithEvidence(ctx, d.DecisionRequest{Quorum: 3, MaxCost: "0"}, "")
			if err != nil || !r.NeedEscalation {
				t.Error("timeout did not escalate")
			}
		}()
	}
	wg.Wait()
	g.Wait()
	if maximum.Load() > 1 || active.Load() != 0 {
		t.Fatal("global provider concurrency cap failed")
	}
	members[1].Provider = boundedProvider{id: members[1].Provider.Identity(), active: &active, maximum: &maximum, fail: true}
	g, _ = New("aggregate", members, 2)
	r, _, err := g.DecideWithEvidence(context.Background(), d.DecisionRequest{Quorum: 3, MaxCost: "0"}, "")
	g.Wait()
	if err != nil || !r.NeedEscalation || r.Label != "UNKNOWN" {
		t.Fatal("partial failure silently accepted")
	}
}
func TestQuorumCannotDowngradeOrExceedBudget(t *testing.T) {
	a, b := member("a", "rules", "NORMAL"), member("b", "gli", "NORMAL")
	a.Price = 2
	b.Price = 3
	g, _ := New("aggregate", []Member{a, b}, 2)
	if g.ValidateRequest(d.DecisionRequest{Quorum: 1, MaxCost: "5"}) == nil {
		t.Fatal("quorum downgraded")
	}
	if g.ValidateRequest(d.DecisionRequest{Quorum: 2, MaxCost: "4"}) == nil {
		t.Fatal("budget exceeded")
	}
	if _, err := New("aggregate", []Member{a, a}, 2); err == nil {
		t.Fatal("duplicate provider identity accepted")
	}
}
