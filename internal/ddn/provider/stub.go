package provider

import (
	"context"

	d "github.com/n42blockchain/N42/internal/ddn/types"
)

// Stub is explicit test/shadow scaffolding, never an automatic model fallback.
type Stub struct {
	ID     Identity
	Result d.DecisionResult
	Err    error
}

func (s Stub) Identity() Identity { return s.ID }
func (s Stub) Decide(ctx context.Context, _ d.DecisionRequest, _ string) (d.DecisionResult, error) {
	if err := ctx.Err(); err != nil {
		return d.DecisionResult{}, err
	}
	return s.Result, s.Err
}
