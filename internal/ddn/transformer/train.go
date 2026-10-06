package transformer

import (
	"context"
	"errors"
	"math"
	"sort"
)

type Example struct {
	Label string `json:"label"`
	Text  string `json:"text"`
}
type TrainConfig struct {
	Epochs       int
	LearningRate float64
	WeightDecay  float64
	ClipNorm     float64
}
type TrainingReport struct {
	InitialLoss float64 `json:"initial_loss"`
	FinalLoss   float64 `json:"final_loss"`
	Steps       int     `json:"steps"`
}

// Train updates embeddings, attention, feed-forward, norms and classifier with
// reverse-mode gradients and AdamW. It never mutates the caller's artifact.
func Train(ctx context.Context, a Artifact, examples []Example, c TrainConfig) (Artifact, TrainingReport, error) {
	report := TrainingReport{}
	m, err := New(a)
	if err != nil {
		return a, report, err
	}
	a = m.artifact
	if len(examples) < 2 || len(examples) > 10000 || c.Epochs < 1 || c.Epochs > 1000 || c.LearningRate <= 0 || c.LearningRate > 1 || math.IsNaN(c.LearningRate) || c.WeightDecay < 0 || c.WeightDecay > 1 || math.IsNaN(c.WeightDecay) || c.ClipNorm <= 0 || math.IsNaN(c.ClipNorm) || math.IsInf(c.ClipNorm, 0) {
		return a, report, errors.New("invalid bounded transformer training configuration")
	}
	targets := make([]int, len(examples))
	for i, e := range examples {
		j := sort.SearchStrings(a.Labels, e.Label)
		if j == len(a.Labels) || a.Labels[j] != e.Label {
			return a, report, errors.New("unknown training label")
		}
		targets[i] = j
	}
	loss := func() (float64, error) {
		var sum float64
		for i, e := range examples {
			logits, _, err := graph(ctx, a, e.Text)
			if err != nil {
				return 0, err
			}
			sum += crossEntropy(logits, targets[i]).v[0]
		}
		return sum / float64(len(examples)), nil
	}
	report.InitialLoss, err = loss()
	if err != nil {
		return a, report, err
	}
	first, second := make([][]float64, len(a.Parameters)), make([][]float64, len(a.Parameters))
	for i, p := range a.Parameters {
		first[i] = make([]float64, len(p.Values))
		second[i] = make([]float64, len(p.Values))
	}
	for epoch := 0; epoch < c.Epochs; epoch++ {
		for i, e := range examples {
			if err := ctx.Err(); err != nil {
				return a, report, err
			}
			logits, ps, err := graph(ctx, a, e.Text)
			if err != nil {
				return a, report, err
			}
			back(crossEntropy(logits, targets[i]))
			if err := ctx.Err(); err != nil {
				return a, report, err
			}
			var squared float64
			for _, p := range ps {
				for _, g := range p.g {
					squared += g * g
				}
			}
			if math.IsNaN(squared) || math.IsInf(squared, 0) {
				return a, report, errors.New("non-finite transformer gradient")
			}
			scale := 1.0
			if n := math.Sqrt(squared); n > c.ClipNorm {
				scale = c.ClipNorm / n
			}
			report.Steps++
			bc1 := 1 - math.Pow(0.9, float64(report.Steps))
			bc2 := 1 - math.Pow(0.999, float64(report.Steps))
			for j, p := range ps {
				for k, g := range p.g {
					g *= scale
					first[j][k] = 0.9*first[j][k] + 0.1*g
					second[j][k] = 0.999*second[j][k] + 0.001*g*g
					w := a.Parameters[j].Values[k]
					a.Parameters[j].Values[k] = w - c.LearningRate*(first[j][k]/bc1/(math.Sqrt(second[j][k]/bc2)+1e-8)+c.WeightDecay*w)
				}
			}
		}
	}
	report.FinalLoss, err = loss()
	if err != nil {
		return a, report, err
	}
	return a, report, a.Validate()
}
