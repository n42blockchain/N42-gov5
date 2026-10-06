package transformer

import (
	"bytes"
	"context"
	"errors"
	"math"
	"strings"
	"sync"
	"testing"
	"time"
)

func tiny(t *testing.T) Artifact {
	t.Helper()
	a, err := Initialize("tiny", "1", "test.classify", "label-v1", []string{"BLUE", "RED"}, Config{Hidden: 4, Heads: 2, Layers: 1, FeedForward: 8, MaxSequence: 16}, 900000, 42)
	if err != nil {
		t.Fatal(err)
	}
	return a
}
func TestBackwardMatchesFiniteDifferences(t *testing.T) {
	a := tiny(t)
	ctx := context.Background()
	logits, ps, err := graph(ctx, a, "red")
	if err != nil {
		t.Fatal(err)
	}
	loss := crossEntropy(logits, 1)
	back(loss)
	for i, p := range a.Parameters {
		index := len(p.Values) / 2
		if p.Name == "token" {
			index = int('r')*a.Config.Hidden + 1
		}
		original := p.Values[index]
		const eps = 1e-5
		a.Parameters[i].Values[index] = original + eps
		plus, _, _ := graph(ctx, a, "red")
		hi := crossEntropy(plus, 1).v[0]
		a.Parameters[i].Values[index] = original - eps
		minus, _, _ := graph(ctx, a, "red")
		lo := crossEntropy(minus, 1).v[0]
		a.Parameters[i].Values[index] = original
		numeric := (hi - lo) / (2 * eps)
		analytic := ps[i].g[index]
		if math.Abs(numeric-analytic) > 2e-5*(1+math.Abs(numeric)) {
			t.Fatalf("%s: backward %.8f numerical %.8f", p.Name, analytic, numeric)
		}
	}
}
func TestTrainAllLayersAndReload(t *testing.T) {
	a := tiny(t)
	a.MinConfidencePPM = 600000
	data := []Example{{"RED", "red"}, {"BLUE", "blue"}, {"RED", "red red"}, {"BLUE", "blue blue"}}
	trained, report, err := Train(context.Background(), a, data, TrainConfig{Epochs: 45, LearningRate: 0.015, ClipNorm: 1})
	if err != nil {
		t.Fatal(err)
	}
	if report.FinalLoss >= report.InitialLoss*0.25 {
		t.Fatalf("model failed to learn: %+v", report)
	}
	if report.Steps != 180 {
		t.Fatal(report)
	}
	for i, p := range trained.Parameters {
		changed := false
		for j, v := range p.Values {
			changed = changed || v != a.Parameters[i].Values[j]
		}
		if !changed {
			t.Fatalf("parameter %s was not trained", p.Name)
		}
	}
	b, _ := trained.Bytes()
	loaded, err := Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	m, err := New(loaded)
	if err != nil {
		t.Fatal(err)
	}
	h, _ := trained.Hash()
	if m.Hash() != h {
		t.Fatal("weight hash changed")
	}
	for input, label := range map[string]string{"red": "RED", "blue": "BLUE", "red!": "RED", "blue!": "BLUE"} {
		r, err := m.Predict(context.Background(), input)
		if err != nil || r.Label != label || r.Validate() != nil {
			t.Fatal(input, r, err)
		}
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := m.Predict(context.Background(), "red"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	// Repeat training from identical seed/statistics, including optimizer state.
	repeated, _, err := Train(context.Background(), a, data, TrainConfig{Epochs: 45, LearningRate: 0.015, ClipNorm: 1})
	if err != nil {
		t.Fatal(err)
	}
	rh, _ := repeated.Hash()
	if rh != h {
		t.Fatal("training not reproducible")
	}
}
func TestTransformerValidationAndCancellation(t *testing.T) {
	a := tiny(t)
	m, _ := New(a)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.Predict(ctx, "red"); err == nil {
		t.Fatal("ignored cancellation")
	}
	if _, err := m.Predict(context.Background(), "long input exceeding sequence"); err == nil {
		t.Fatal("silently truncated input")
	}
	a.Parameters[0].Values[0] = math.NaN()
	if a.Validate() == nil {
		t.Fatal("nonfinite weight accepted")
	}
	a = tiny(t)
	a.Parameters[1].Cols++
	if a.Validate() == nil {
		t.Fatal("bad dimensions accepted")
	}
	a = tiny(t)
	a.MinConfidencePPM = 1000000
	m, _ = New(a)
	r, err := m.Predict(context.Background(), "red")
	if err != nil || r.Label != "ABSTAIN" || !r.NeedEscalation {
		t.Fatal(r, err)
	}
	if _, _, err := Train(ctx, a, []Example{{"RED", "red"}, {"BLUE", "blue"}}, TrainConfig{Epochs: 1, LearningRate: 0.001, ClipNorm: 1}); err == nil {
		t.Fatal("cancelled training accepted")
	}
}

func TestInferenceDeadline(t *testing.T) {
	a, err := Initialize("bounded", "1", "test", "v1", []string{"A", "B"}, Config{Hidden: 128, Heads: 8, Layers: 4, FeedForward: 256, MaxSequence: 256}, 800000, 42)
	if err != nil {
		t.Fatal(err)
	}
	m, err := New(a)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, err = m.Predict(ctx, strings.Repeat("a", 255)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline ignored: %v", err)
	}
	if time.Since(started) > 2*time.Second {
		t.Fatal("cancelled inference kept consuming CPU")
	}
}
