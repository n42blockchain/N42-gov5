package gateway

import (
	"context"

	chain "github.com/n42blockchain/N42/common/types"
	d "github.com/n42blockchain/N42/internal/ddn/types"
)

// API is registered under n42; methods become n42_ddnSubmit/GetReceipt.
// Node registration marks it authenticated to bound caller access.
type API struct{ Gateway *Gateway }

func (a *API) DdnSubmit(ctx context.Context, r d.DecisionRequest, input string) (chain.Hash, error) {
	if err := ctx.Err(); err != nil {
		return chain.Hash{}, err
	}
	return a.Gateway.Submit(r, input)
}
func (a *API) DdnGetReceipt(ctx context.Context, id chain.Hash) (*Record, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return a.Gateway.GetReceipt(id)
}
