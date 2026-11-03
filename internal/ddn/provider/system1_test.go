package provider

import (
	"context"
	"testing"

	d "github.com/n42blockchain/N42/internal/ddn/types"
)

func TestNativeSystem1SchemaAndSignedAnswerShape(t *testing.T) {
	p, err := NewNativeSystem1("did:n42:system1")
	if err != nil {
		t.Fatal(err)
	}
	request := d.DecisionRequest{Task: "node.anomaly", SchemaID: "system1-v1"}
	result, err := p.Decide(context.Background(), request, "qc mismatch")
	if err != nil {
		t.Fatal(err)
	}
	if err = result.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(p.Labels()) != 8 || len(result.Answers) != 3 || result.Answers[2].Kind != 3 || !result.NeedEscalation {
		t.Fatalf("%+v", result)
	}
	request.SchemaID = "health-v1"
	if _, err = p.Decide(context.Background(), request, "qc mismatch"); err == nil {
		t.Fatal("accepted wrong schema")
	}
	labels := p.Labels()
	labels[0] = "mutated"
	if p.Labels()[0] != "NORMAL" {
		t.Fatal("mutable labels")
	}
}
