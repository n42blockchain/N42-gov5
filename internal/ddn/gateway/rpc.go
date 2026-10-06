package gateway

import (
	"context"

	chain "github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/ddn/provider"
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

// Info advertises the actual executable identity; hashes are never inferred
// from the request or a user-supplied model name.
type Info struct {
	Provider   provider.Identity `json:"provider"`
	Quorum     uint32            `json:"quorum"`
	ShadowMode bool              `json:"shadow_mode"`
	Labels     []string          `json:"labels"`
}

func (g *Gateway) Info() Info {
	i := Info{Provider: g.provider.Identity(), Quorum: g.cfg.MaxQuorum, ShadowMode: true, Labels: []string{}}
	if p, ok := g.provider.(interface{ Labels() []string }); ok {
		i.Labels = p.Labels()
	}
	return i
}
func (a *API) DdnInfo(ctx context.Context) (Info, error) {
	if err := ctx.Err(); err != nil {
		return Info{}, err
	}
	return a.Gateway.Info(), nil
}
