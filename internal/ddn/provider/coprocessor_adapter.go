package provider

import (
	"context"
	"errors"
	chain "github.com/n42blockchain/N42/common/types"
	d "github.com/n42blockchain/N42/internal/ddn/types"
	"github.com/n42blockchain/N42/internal/distributed/coprocessor"
	"strings"
)

// CoprocessorAdapter rechecks the existing provider registry before dispatch.
// It does not turn a model's decision into a coprocessor computation proof.
type CoprocessorAdapter struct {
	Registry      *coprocessor.ProviderRegistry
	Address       chain.Address
	Backend       DecisionProvider
	MinReputation uint64
}

func (a *CoprocessorAdapter) Identity() Identity { return a.Backend.Identity() }
func (a *CoprocessorAdapter) Eligible() error {
	if a.Registry == nil || a.Backend == nil {
		return errors.New("DDN provider adapter not configured")
	}
	p, ok := a.Registry.GetSnapshot(a.Address)
	if !ok || p.Status != coprocessor.ProviderActive || !p.HasCapability(coprocessor.CapAI) || p.Reputation < a.MinReputation {
		return errors.New("DDN provider not eligible")
	}
	if a.Backend.Identity().DID != "did:n42:"+strings.ToLower(a.Address.Hex()) {
		return errors.New("DDN provider DID does not match registry address")
	}
	return nil
}
func (a *CoprocessorAdapter) Decide(ctx context.Context, r d.DecisionRequest, input string) (d.DecisionResult, error) {
	if err := a.Eligible(); err != nil {
		return d.DecisionResult{}, err
	}
	return a.Backend.Decide(ctx, r, input)
}
