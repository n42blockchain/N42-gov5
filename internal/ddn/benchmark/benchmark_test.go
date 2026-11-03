package benchmark

import (
	"math"
	"testing"
)

func TestMetricsAndStrictMatching(t *testing.T) {
	events := []Event{{ID: "a", Source: "node", Text: "qc mismatch", Truth: "CONSENSUS", NeedEscalation: "YES", IncidentID: "i"}, {ID: "b", Source: "node", Text: "normal", Truth: "NORMAL", NeedEscalation: "NO", IncidentID: "j"}}
	preds := []Prediction{{ID: "a", Label: "CONSENSUS", NeedEscalation: "YES", LatencyMs: 1}, {ID: "b", Label: "UNKNOWN", NeedEscalation: "YES", LatencyMs: 3}}
	r, err := Evaluate(events, preds)
	if err != nil {
		t.Fatal(err)
	}
	if r.Accuracy != .5 || r.UnknownRate != .5 || r.EscalationRate != 1 || r.EscalationAccuracy != .5 || r.LatencyMs["p50"] != 2 || r.Recall["STORAGE"] != nil || *r.FPR["UNKNOWN"] != .5 || *r.FNR["NORMAL"] != 1 {
		t.Fatalf("%+v", r)
	}
	preds[1].ID = "a"
	if _, err = Evaluate(events, preds); err == nil {
		t.Fatal("duplicate accepted")
	}
	preds[1].ID = "other"
	if _, err = Evaluate(events, preds); err == nil {
		t.Fatal("extra id accepted")
	}
	preds[1].ID = "b"
	preds[1].LatencyMs = math.NaN()
	if _, err = Evaluate(events, preds); err == nil {
		t.Fatal("NaN accepted")
	}
}
