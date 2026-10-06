package provider

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/internal/ddn/native"
	"github.com/n42blockchain/N42/internal/ddn/transformer"
	d "github.com/n42blockchain/N42/internal/ddn/types"
)

func TestNativeModelGuardAndIdentity(t *testing.T) {
	a, err := native.Train(context.Background(), "health", "1", "node.anomaly", "health-v1", []native.Example{{Label: "NORMAL", Text: "disk full healthy normal"}, {Label: "BAD", Text: "corrupted database panic"}}, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	p, err := NewNativeModel("did:n42:local", a)
	if err != nil {
		t.Fatal(err)
	}
	h, _ := a.Hash()
	if p.Identity().ModelHash == h {
		t.Fatal("identity omitted executable guard")
	}
	for _, input := range []string{"disk full", "disk full and quorum lost"} {
		r, err := p.Decide(context.Background(), d.DecisionRequest{Task: "node.anomaly", SchemaID: "health-v1"}, input)
		if err != nil || !r.NeedEscalation || r.Label == "NORMAL" {
			t.Fatal("model downgraded deterministic alarm", r, err)
		}
	}
	if _, err = p.Decide(context.Background(), d.DecisionRequest{Task: "other", SchemaID: "health-v1"}, "disk full"); err == nil {
		t.Fatal("wrong task accepted")
	}
	rules, err := NewNativeRules("did:n42:local")
	if err != nil {
		t.Fatal(err)
	}
	id := rules.Identity()
	id.Tasks[0] = "mutated"
	if rules.Identity().Tasks[0] != "node.anomaly" {
		t.Fatal("identity mutable")
	}
}
func TestTransformerGuard(t *testing.T) {
	a, err := transformer.Initialize("health", "1", "node.anomaly", "health-v1", []string{"NORMAL", "BAD"}, transformer.Config{Hidden: 4, Heads: 1, Layers: 1, FeedForward: 8, MaxSequence: 32}, 0, 7)
	if err != nil {
		t.Fatal(err)
	}
	p, err := NewTransformer("did:n42:local", a)
	if err != nil {
		t.Fatal(err)
	}
	r, err := p.Decide(context.Background(), d.DecisionRequest{Task: "node.anomaly", SchemaID: "health-v1"}, "finality stalled")
	if err != nil || r.Label != "CONSENSUS" || !r.NeedEscalation {
		t.Fatal(r, err)
	}
}
