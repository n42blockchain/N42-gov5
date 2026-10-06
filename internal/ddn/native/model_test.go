package native

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
)

func training() []Example {
	return []Example{{"NORMAL", "healthy synchronized peers connected"}, {"NORMAL", "healthy peers synchronized"}, {"STORAGE", "disk database corrupted full"}, {"STORAGE", "disk full database corrupted"}}
}
func TestTrainInferRoundTrip(t *testing.T) {
	a, err := Train(context.Background(), "health", "1", "node.anomaly", "health-v1", training(), 700000, 500000)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := a.Bytes()
	loaded, err := Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	h, _ := a.Hash()
	h2, _ := loaded.Hash()
	if h != h2 {
		t.Fatal("hash changed on reload")
	}
	c, err := New(loaded)
	if err != nil {
		t.Fatal(err)
	}
	for input, label := range map[string]string{"healthy connected synchronized": "NORMAL", "corrupted database full": "STORAGE", "alien unicorn": "UNKNOWN"} {
		r, err := c.Predict(context.Background(), input)
		if err != nil || r.Label != label || r.Validate() != nil {
			t.Fatalf("%s: %+v %v", input, r, err)
		}
	}
	// Own the artifact and predictions; callers cannot alter the served model.
	a.Counts[0][0] = 100000
	loaded.Labels[0] = "tampered"
	if c.Labels()[0] != "NORMAL" {
		t.Fatal("model aliases caller")
	}
	a2, _ := Train(context.Background(), "health", "1", "node.anomaly", "health-v1", training(), 700000, 500000)
	b2, _ := a2.Bytes()
	if !bytes.Equal(b, b2) {
		t.Fatal("training not deterministic")
	}
}
func TestBoundsAndAbstention(t *testing.T) {
	a, _ := Train(context.Background(), "health", "1", "t", "s", training(), 1000000, 500000)
	c, _ := New(a)
	r, err := c.Predict(context.Background(), "healthy full")
	if err != nil || r.Label != "ABSTAIN" || !r.NeedEscalation {
		t.Fatal(r, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = c.Predict(ctx, "healthy"); err == nil {
		t.Fatal("ignored cancellation")
	}
	a.Counts[0][0] = MaxCount + 1
	if _, err = New(a); err == nil {
		t.Fatal("unbounded count accepted")
	}
	if _, err = Decode(bytes.NewBufferString(`{"bogus":1}`)); err == nil {
		t.Fatal("unknown model fields accepted")
	}
	a, _ = Train(context.Background(), "health", "1", "t", "s", training(), 0, 1)
	a.Vocabulary[0] = "UPPER"
	if a.Validate() == nil {
		t.Fatal("incompatible vocabulary accepted")
	}
}
func TestRulesSafety(t *testing.T) {
	for input, label := range map[string]string{"finality stalled": "CONSENSUS", "DISK FULL": "STORAGE", "no peers": "NETWORK", "vm panic": "EXECUTION", "healthy": "UNKNOWN", "notfinality stalledish": "UNKNOWN", "disk full and quorum lost": "UNKNOWN"} {
		r, err := Health(context.Background(), input)
		if err != nil || r.Label != label || !r.NeedEscalation || r.ConfidencePPM != 0 {
			t.Fatal(input, r, err)
		}
	}
	if RulesHash() == ([32]byte{}) {
		t.Fatal("empty rules hash")
	}
	b, _ := json.Marshal(healthRules)
	if len(b) == 0 {
		t.Fatal("missing rules")
	}
}
