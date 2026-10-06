package provider

import (
	"context"
	"errors"

	chain "github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"

	"github.com/n42blockchain/N42/internal/ddn/native"
	d "github.com/n42blockchain/N42/internal/ddn/types"
)

// Native executes repository-owned algorithms locally; no network client or
// external model runtime is constructed. Identity pins executable rules/weights.
type Native struct {
	identity   Identity
	classifier *native.Classifier
}

func NewNativeRules(did string) (*Native, error) {
	if did == "" {
		return nil, errors.New("native provider DID required")
	}
	return &Native{identity: Identity{DID: did, Model: "N42-health-rules", ModelVersion: "1", ModelHash: native.RulesHash(), Family: "native-health-rules", Tasks: []string{"node.anomaly"}, Schemas: []string{"health-v1"}}}, nil
}
func NewNativeModel(did string, a native.Artifact) (*Native, error) {
	if did == "" {
		return nil, errors.New("native provider DID required")
	}
	c, err := native.New(a)
	if err != nil {
		return nil, err
	}
	name, version, task, schema := c.Metadata()
	return &Native{identity: Identity{DID: did, Model: name, ModelVersion: version, ModelHash: executionHash(c.Hash(), task), Family: "native-multinomial-nb", Tasks: []string{task}, Schemas: []string{schema}}, classifier: c}, nil
}
func (n *Native) Identity() Identity {
	id := n.identity
	id.Tasks = append([]string{}, id.Tasks...)
	id.Schemas = append([]string{}, id.Schemas...)
	return id
}
func (n *Native) Decide(ctx context.Context, r d.DecisionRequest, input string) (d.DecisionResult, error) {
	id := n.identity
	if r.Task != id.Tasks[0] || r.SchemaID != id.Schemas[0] {
		return d.DecisionResult{}, errors.New("native task/schema mismatch")
	}
	if n.classifier == nil {
		return native.Health(ctx, input)
	}
	if result, matched, err := healthGuard(ctx, r.Task, input); matched || err != nil {
		return result, err
	}
	return n.classifier.Predict(ctx, input)
}

// Bind the health safety guard as well as learned weights to the served identity.
func executionHash(weights chain.Hash, task string) chain.Hash {
	if task != "node.anomaly" {
		return weights
	}
	rules := native.RulesHash()
	return crypto.Keccak256Hash([]byte("N42-native-health-guard-v1"), weights[:], rules[:])
}
func healthGuard(ctx context.Context, task, input string) (d.DecisionResult, bool, error) {
	if task != "node.anomaly" {
		return d.DecisionResult{}, false, nil
	}
	return native.HealthSignal(ctx, input)
}

func (n *Native) Labels() []string {
	if n.classifier != nil {
		return n.classifier.Labels()
	}
	return []string{}
}
