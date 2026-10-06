// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Covers StatePrefetcher.SetPredictor's field wiring, PrefetchPredictor's
// BatchPredictSlots (multi-contract, one lock acquisition), and the
// errStopWalk sentinel's Error() string.

package internal

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

func TestSetPredictorWiresField(t *testing.T) {
	p := NewStatePrefetcher(&params.ChainConfig{})
	if p.predictor != nil {
		t.Fatalf("new StatePrefetcher has a non-nil predictor before SetPredictor")
	}
	pred := NewPrefetchPredictor(16)
	p.SetPredictor(pred)
	if p.predictor != pred {
		t.Fatalf("SetPredictor() did not wire the predictor field")
	}
}

func TestBatchPredictSlotsReturnsPerContractTopSlots(t *testing.T) {
	pred := NewPrefetchPredictor(8)
	c1 := types.HexToAddress("0x1")
	c2 := types.HexToAddress("0x2")
	c3 := types.HexToAddress("0x3") // never accessed

	for i := 0; i < 3; i++ {
		pred.RecordSlotAccess(c1, types.HexToHash("0xaa"))
	}
	pred.RecordSlotAccess(c2, types.HexToHash("0xbb"))

	got := pred.BatchPredictSlots([]types.Address{c1, c2, c3}, 5)
	if len(got[c1]) != 1 || got[c1][0] != types.HexToHash("0xaa") {
		t.Fatalf("BatchPredictSlots()[c1] = %v, want [0xaa]", got[c1])
	}
	if len(got[c2]) != 1 || got[c2][0] != types.HexToHash("0xbb") {
		t.Fatalf("BatchPredictSlots()[c2] = %v, want [0xbb]", got[c2])
	}
	if _, ok := got[c3]; ok {
		t.Fatalf("BatchPredictSlots() included an untracked contract: %v", got[c3])
	}

	if got := pred.BatchPredictSlots(nil, 5); len(got) != 0 {
		t.Fatalf("BatchPredictSlots(nil) = %v, want empty map", got)
	}
}

func TestStopWalkErrorMessage(t *testing.T) {
	var err error = stopWalk{}
	if err.Error() != "stop walk" {
		t.Fatalf("stopWalk.Error() = %q, want %q", err.Error(), "stop walk")
	}
	if errStopWalk.Error() != "stop walk" {
		t.Fatalf("errStopWalk.Error() = %q, want %q", errStopWalk.Error(), "stop walk")
	}
}
