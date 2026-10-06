// Package scheduler adapts DDN offers to the existing coprocessor auction.
package scheduler

import (
	"errors"
	"math/big"
	"time"

	chain "github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/ddn/provider"
	d "github.com/n42blockchain/N42/internal/ddn/types"
	"github.com/n42blockchain/N42/internal/distributed/coprocessor"
)

type Offer struct {
	Address chain.Address
	Backend provider.DecisionProvider
	Price   uint64
	ETA     time.Duration
}
type Selection struct {
	Providers []provider.DecisionProvider
	Price     *big.Int
}
type Scheduler struct {
	Registry      *coprocessor.ProviderRegistry
	MinReputation uint64
	Strategy      coprocessor.SelectionStrategy
}

func (s Scheduler) Select(r d.DecisionRequest, offers []Offer) (Selection, error) {
	if s.Registry == nil || r.Quorum < 1 || r.Quorum > 16 || len(offers) > 64 || r.MaxLatencyMs < 1 || r.MaxLatencyMs > 60000 || r.PrivacyMode != "public" {
		return Selection{}, errors.New("invalid DDN selection request")
	}
	budget, ok := new(big.Int).SetString(r.MaxCost, 10)
	if !ok || budget.Sign() < 0 || budget.BitLen() > 256 {
		return Selection{}, errors.New("invalid DDN selection budget")
	}
	requestID, err := r.CanonicalHash()
	if err != nil {
		return Selection{}, err
	}
	selected := map[chain.Address]bool{}
	total := new(big.Int)
	out := Selection{Price: total}
	for len(out.Providers) < int(r.Quorum) {
		// A per-selection auction reuses bidding/ranking without accumulating DDN
		// assignments in the coprocessor's long-lived settlement marketplace.
		market := coprocessor.NewMarketplace(s.Registry)
		market.PublishTask(requestID)
		eligible := map[chain.Address]*provider.CoprocessorAdapter{}
		for _, offer := range offers {
			if selected[offer.Address] || eligible[offer.Address] != nil || offer.Backend == nil || offer.ETA < 0 || offer.ETA > time.Duration(r.MaxLatencyMs)*time.Millisecond {
				continue
			}
			price := new(big.Int).SetUint64(offer.Price)
			if new(big.Int).Add(total, price).Cmp(budget) > 0 {
				continue
			}
			a := &provider.CoprocessorAdapter{Registry: s.Registry, Address: offer.Address, Backend: offer.Backend, MinReputation: s.MinReputation}
			if a.Eligible() != nil {
				continue
			}
			id := a.Identity()
			if !has(id.Tasks, r.Task) || !has(id.Schemas, r.SchemaID) || (r.ModelRequirements.ModelHash != (chain.Hash{}) && id.ModelHash != r.ModelRequirements.ModelHash) || (r.ModelRequirements.Family != "" && id.Family != r.ModelRequirements.Family) {
				continue
			}
			if _, err := market.SubmitBid(requestID, offer.Address, offer.Price, offer.ETA); err != nil {
				continue
			}
			eligible[offer.Address] = a
		}
		winner, err := market.SelectWinner(requestID, s.Strategy)
		if err != nil {
			return Selection{}, errors.New("insufficient eligible DDN offers within budget")
		}
		a := eligible[winner.ProviderID]
		if a == nil || a.Eligible() != nil {
			return Selection{}, errors.New("selected provider became unavailable")
		}
		selected[winner.ProviderID] = true
		out.Providers = append(out.Providers, a)
		total.Add(total, new(big.Int).SetUint64(winner.Price))
	}
	return out, nil
}
func has(ss []string, v string) bool {
	for _, s := range ss {
		if s == v {
			return true
		}
	}
	return false
}
