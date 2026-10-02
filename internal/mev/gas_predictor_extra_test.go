package mev

import (
	"math"
	"testing"
)

// TestGasPredictor_PredictGasUsage covers the gas-usage EWMA branch, which
// is exercised separately from PredictBaseFee since each walks its own
// field of the observation history.
func TestGasPredictor_PredictGasUsage(t *testing.T) {
	gp := NewGasPredictor(4)

	if pred := gp.PredictGasUsage(); pred != nil {
		t.Fatal("expected nil prediction with no observations")
	}

	gp.AddObservation(GasObservation{GasUsed: 1000})
	gp.AddObservation(GasObservation{GasUsed: 2000})

	pred := gp.PredictGasUsage()
	if pred == nil {
		t.Fatal("expected non-nil prediction")
	}
	if pred.PredictedGasUsed < 1000 || pred.PredictedGasUsed > 2000 {
		t.Fatalf("predicted gas used = %d, want in [1000, 2000]", pred.PredictedGasUsed)
	}
	if pred.BasedOnBlocks != 2 {
		t.Fatalf("based on blocks = %d, want 2", pred.BasedOnBlocks)
	}
}

// TestGasPredictor_ObservationCountAndWindowTrim covers ObservationCount and
// the sliding-window trim path in AddObservation (len(history) > windowSize).
func TestGasPredictor_ObservationCountAndWindowTrim(t *testing.T) {
	gp := NewGasPredictor(3)

	if n := gp.ObservationCount(); n != 0 {
		t.Fatalf("ObservationCount() = %d, want 0", n)
	}

	for i := uint64(1); i <= 5; i++ {
		gp.AddObservation(GasObservation{BlockNumber: i})
	}

	if n := gp.ObservationCount(); n != 3 {
		t.Fatalf("ObservationCount() = %d, want 3 (window size)", n)
	}

	last := gp.LastObservation()
	if last == nil || last.BlockNumber != 5 {
		t.Fatalf("LastObservation() = %+v, want BlockNumber 5", last)
	}
}

// TestNewGasPredictor_DefaultWindow covers the windowSize<=0 default branch.
func TestNewGasPredictor_DefaultWindow(t *testing.T) {
	gp := NewGasPredictor(0)
	if gp.windowSize != defaultGasWindowSize {
		t.Fatalf("windowSize = %d, want default %d", gp.windowSize, defaultGasWindowSize)
	}

	gpNeg := NewGasPredictor(-5)
	if gpNeg.windowSize != defaultGasWindowSize {
		t.Fatalf("windowSize = %d, want default %d", gpNeg.windowSize, defaultGasWindowSize)
	}
}

// TestEwma_EmptyAndSingle covers the degenerate ewma branches directly.
func TestEwma_EmptyAndSingle(t *testing.T) {
	if v := ewma(nil, 0.3); v != 0 {
		t.Fatalf("ewma(nil) = %f, want 0", v)
	}
	if v := ewma([]float64{42}, 0.3); v != 42 {
		t.Fatalf("ewma(single) = %f, want 42", v)
	}
	v := ewma([]float64{10, 20}, 0.5)
	want := 0.5*20 + 0.5*10
	if math.Abs(v-want) > 1e-9 {
		t.Fatalf("ewma = %f, want %f", v, want)
	}
}
