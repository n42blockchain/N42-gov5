package provider

import (
	"context"
	"errors"

	"github.com/n42blockchain/N42/internal/ddn/transformer"
	d "github.com/n42blockchain/N42/internal/ddn/types"
)

type Transformer struct {
	identity Identity
	model    *transformer.Model
}

func NewTransformer(did string, a transformer.Artifact) (*Transformer, error) {
	if did == "" {
		return nil, errors.New("transformer provider DID required")
	}
	m, err := transformer.New(a)
	if err != nil {
		return nil, err
	}
	name, version, task, schema := m.Metadata()
	return &Transformer{identity: Identity{DID: did, Model: name, ModelVersion: version, ModelHash: executionHash(m.Hash(), task), Family: "native-byte-transformer", Tasks: []string{task}, Schemas: []string{schema}}, model: m}, nil
}
func (t *Transformer) Identity() Identity {
	id := t.identity
	id.Tasks = append([]string{}, id.Tasks...)
	id.Schemas = append([]string{}, id.Schemas...)
	return id
}
func (t *Transformer) Decide(ctx context.Context, r d.DecisionRequest, input string) (d.DecisionResult, error) {
	if r.Task != t.identity.Tasks[0] || r.SchemaID != t.identity.Schemas[0] {
		return d.DecisionResult{}, errors.New("transformer task/schema mismatch")
	}
	if result, matched, err := healthGuard(ctx, r.Task, input); matched || err != nil {
		return result, err
	}
	return t.model.Predict(ctx, input)
}

func (t *Transformer) Labels() []string { return t.model.Labels() }
