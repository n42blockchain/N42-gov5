package provider

import (
	"context"
	chain "github.com/n42blockchain/N42/common/types"
	d "github.com/n42blockchain/N42/internal/ddn/types"
)

type Identity struct {
	DID          string     `json:"did"`
	Model        string     `json:"model"`
	ModelVersion string     `json:"model_version"`
	ModelHash    chain.Hash `json:"model_hash"`
	Family       string     `json:"family"`
	Tasks        []string   `json:"tasks"`
	Schemas      []string   `json:"schemas"`
}

// DecisionProvider must honor cancellation and report a pinned model identity.
type DecisionProvider interface {
	Identity() Identity
	Decide(context.Context, d.DecisionRequest, string) (d.DecisionResult, error)
}
