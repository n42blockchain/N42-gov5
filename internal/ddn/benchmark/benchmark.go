// Package benchmark evaluates the n42-26 adjudicated corpus format locally.
package benchmark

import (
	"errors"
	"math"
	"sort"
	"unicode/utf8"

	"github.com/n42blockchain/N42/internal/ddn/native"
)

type Event struct {
	ID             string `json:"id"`
	Source         string `json:"source"`
	Text           string `json:"text"`
	ObservedAtMs   int64  `json:"observed_at_ms"`
	Truth          string `json:"truth,omitempty"`
	NeedEscalation string `json:"need_escalation,omitempty"`
	IncidentID     string `json:"incident_id,omitempty"`
	Truncated      bool   `json:"truncated,omitempty"`
}
type Prediction struct {
	ID             string  `json:"id"`
	Label          string  `json:"label"`
	NeedEscalation string  `json:"need_escalation"`
	LatencyMs      float64 `json:"latency_ms"`
	Model          string  `json:"model,omitempty"`
}
type Report struct {
	Events             int                 `json:"events"`
	Accuracy           float64             `json:"accuracy"`
	UnknownRate        float64             `json:"unknown_rate"`
	EscalationRate     float64             `json:"escalation_rate"`
	EscalationAccuracy float64             `json:"escalation_accuracy"`
	LatencyMs          map[string]float64  `json:"latency_ms"`
	Recall             map[string]*float64 `json:"recall"`
	FPR                map[string]*float64 `json:"fpr"`
	FNR                map[string]*float64 `json:"fnr"`
}

func ValidSource(s string) bool {
	switch s {
	case "node", "ci", "rpc", "network", "benchmark", "configuration", "peerdas", "block_stm", "qmdb":
		return true
	}
	return false
}
func validLabel(s string) bool {
	for _, l := range native.SystemLabels() {
		if s == l {
			return true
		}
	}
	return false
}
func validEscalation(s string) bool { return s == "YES" || s == "NO" }
func (e Event) Validate(labelled bool) error {
	if len(e.ID) < 1 || len(e.ID) > 128 || !utf8.ValidString(e.ID) || !ValidSource(e.Source) || len(e.Text) < 1 || len(e.Text) > 8192 || !utf8.ValidString(e.Text) || e.ObservedAtMs < 0 {
		return errors.New("invalid event")
	}
	if labelled && (!validLabel(e.Truth) || !validEscalation(e.NeedEscalation) || e.IncidentID == "") {
		return errors.New("missing adjudicated label or incident group")
	}
	return nil
}
func ratio(n, d int) *float64 {
	if d == 0 {
		return nil
	}
	v := float64(n) / float64(d)
	return &v
}
func Evaluate(events []Event, predictions []Prediction) (Report, error) {
	r := Report{Events: len(events), Recall: map[string]*float64{}, FPR: map[string]*float64{}, FNR: map[string]*float64{}, LatencyMs: map[string]float64{}}
	if len(events) == 0 || len(events) != len(predictions) {
		return r, errors.New("empty or mismatched corpus")
	}
	byID := map[string]Prediction{}
	for _, p := range predictions {
		if _, ok := byID[p.ID]; ok || !validLabel(p.Label) || !validEscalation(p.NeedEscalation) || p.LatencyMs < 0 || math.IsNaN(p.LatencyMs) || math.IsInf(p.LatencyMs, 0) {
			return r, errors.New("invalid prediction")
		}
		byID[p.ID] = p
	}
	seen := map[string]bool{}
	positive := map[string]int{}
	tp := map[string]int{}
	fp := map[string]int{}
	latencies := []float64{}
	for _, e := range events {
		if err := e.Validate(true); err != nil {
			return r, err
		}
		if seen[e.ID] {
			return r, errors.New("duplicate corpus id")
		}
		seen[e.ID] = true
		p, ok := byID[e.ID]
		if !ok {
			return r, errors.New("prediction set differs from corpus")
		}
		positive[e.Truth]++
		if p.Label == e.Truth {
			r.Accuracy++
			tp[e.Truth]++
		} else {
			fp[p.Label]++
		}
		if p.Label == "UNKNOWN" {
			r.UnknownRate++
		}
		if p.NeedEscalation == "YES" {
			r.EscalationRate++
		}
		if p.NeedEscalation == e.NeedEscalation {
			r.EscalationAccuracy++
		}
		latencies = append(latencies, p.LatencyMs)
	}
	n := float64(len(events))
	r.Accuracy /= n
	r.UnknownRate /= n
	r.EscalationRate /= n
	r.EscalationAccuracy /= n
	for _, l := range native.SystemLabels() {
		r.Recall[l] = ratio(tp[l], positive[l])
		r.FNR[l] = ratio(positive[l]-tp[l], positive[l])
		r.FPR[l] = ratio(fp[l], len(events)-positive[l])
	}
	sort.Float64s(latencies)
	for k, f := range map[string]float64{"p50": .5, "p95": .95, "p99": .99} {
		pos := float64(len(latencies)-1) * f
		lo, hi := int(math.Floor(pos)), int(math.Ceil(pos))
		r.LatencyMs[k] = latencies[lo] + (latencies[hi]-latencies[lo])*(pos-float64(lo))
	}
	return r, nil
}
