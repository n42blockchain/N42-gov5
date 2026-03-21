// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package mev

import (
	"math"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
)

// makeDynFeeTx creates a DynamicFeeTx for testing with the given parameters.
func makeDynFeeTx(nonce uint64, from types.Address, to *types.Address, gasTipCap, gasFeeCap *uint256.Int, gas uint64, value *uint256.Int) *transaction.Transaction {
	return transaction.NewTx(&transaction.DynamicFeeTx{
		Nonce:     nonce,
		GasTipCap: gasTipCap,
		GasFeeCap: gasFeeCap,
		Gas:       gas,
		To:        to,
		From:      &from,
		Value:     value,
	})
}

// makeLegacyTx creates a LegacyTx for testing with the given parameters.
func makeLegacyTx(nonce uint64, from types.Address, to *types.Address, gasPrice *uint256.Int, gas uint64, value *uint256.Int) *transaction.Transaction {
	return transaction.NewTx(&transaction.LegacyTx{
		Nonce:    nonce,
		GasPrice: gasPrice,
		Gas:      gas,
		To:       to,
		From:     &from,
		Value:    value,
	})
}

func TestGasPredictor_EWMA(t *testing.T) {
	gp := NewGasPredictor(10)

	// No observations yet.
	if pred := gp.PredictBaseFee(); pred != nil {
		t.Fatal("expected nil prediction with no observations")
	}

	// Add some observations with increasing base fees.
	for i := uint64(1); i <= 5; i++ {
		gp.AddObservation(GasObservation{
			BlockNumber: i,
			BaseFee:     100 + i*10, // 110, 120, 130, 140, 150
			GasUsed:     15000000,
			GasLimit:    30000000,
		})
	}

	pred := gp.PredictBaseFee()
	if pred == nil {
		t.Fatal("expected non-nil prediction")
	}

	// EWMA with alpha=0.3 should give a value between 110 and 150,
	// weighted toward recent values.
	if pred.PredictedBaseFee < 110 || pred.PredictedBaseFee > 150 {
		t.Fatalf("predicted base fee = %d, want in [110, 150]", pred.PredictedBaseFee)
	}
	if pred.BasedOnBlocks != 5 {
		t.Fatalf("based on blocks = %d, want 5", pred.BasedOnBlocks)
	}

	// Confidence should be 5/10 = 0.5.
	if math.Abs(pred.Confidence-0.5) > 0.01 {
		t.Fatalf("confidence = %f, want 0.5", pred.Confidence)
	}
}

func TestGasPredictor_AverageUtilization(t *testing.T) {
	gp := NewGasPredictor(10)

	// No observations.
	if util := gp.AverageUtilization(); util != 0 {
		t.Fatalf("utilization = %f, want 0 with no observations", util)
	}

	// 50% utilization blocks.
	gp.AddObservation(GasObservation{BaseFee: 100, GasUsed: 15000000, GasLimit: 30000000})
	gp.AddObservation(GasObservation{BaseFee: 100, GasUsed: 15000000, GasLimit: 30000000})

	util := gp.AverageUtilization()
	if math.Abs(util-0.5) > 0.01 {
		t.Fatalf("utilization = %f, want 0.5", util)
	}

	// Add a 100% utilization block.
	gp.AddObservation(GasObservation{BaseFee: 100, GasUsed: 30000000, GasLimit: 30000000})

	util = gp.AverageUtilization()
	expected := (0.5 + 0.5 + 1.0) / 3.0
	if math.Abs(util-expected) > 0.01 {
		t.Fatalf("utilization = %f, want %f", util, expected)
	}
}

func TestAIBlockOptimizer_OptimizeOrdering(t *testing.T) {
	opt := NewAIBlockOptimizer(false, 0, false, nil)
	baseFee := uint256.NewInt(1000000000) // 1 Gwei

	addr1 := types.HexToAddress("0x01")
	target := types.HexToAddress("0xAA")

	// Low tip transaction.
	lowTx := makeDynFeeTx(0, addr1, &target, uint256.NewInt(1000000000), uint256.NewInt(2000000000), 21000, uint256.NewInt(0))
	// High tip transaction.
	highTx := makeDynFeeTx(1, addr1, &target, uint256.NewInt(5000000000), uint256.NewInt(10000000000), 21000, uint256.NewInt(0))

	result := opt.OptimizeOrdering([]*transaction.Transaction{lowTx, highTx}, baseFee)

	if len(result) != 2 {
		t.Fatalf("result len = %d, want 2", len(result))
	}

	// High tip tx should be first after optimization.
	if result[0].GasTipCap().Cmp(uint256.NewInt(5000000000)) != 0 {
		t.Fatal("expected high-tip tx to be ordered first")
	}

	// Stats should track the optimization.
	if opt.Stats() != 1 {
		t.Fatalf("blocks optimized = %d, want 1", opt.Stats())
	}

	// Nil input.
	result = opt.OptimizeOrdering(nil, baseFee)
	if result != nil {
		t.Fatal("expected nil result for nil input")
	}
}

func TestAIBlockOptimizer_FairnessGuard(t *testing.T) {
	opt := NewAIBlockOptimizer(true, 0, false, nil)

	addr1 := types.HexToAddress("0x01")
	addr2 := types.HexToAddress("0x02")
	target := types.HexToAddress("0xAA")

	// Create a sandwich pattern: addr1 -> addr2 -> addr1 all targeting the same contract.
	// Use different gas prices so each tx gets a unique hash (hash excludes From).
	tx1 := makeLegacyTx(0, addr1, &target, uint256.NewInt(1000000000), 21000, uint256.NewInt(0))
	tx2 := makeLegacyTx(0, addr2, &target, uint256.NewInt(2000000000), 21000, uint256.NewInt(0))
	tx3 := makeLegacyTx(1, addr1, &target, uint256.NewInt(3000000000), 21000, uint256.NewInt(0))

	safe, flagged := opt.FairnessGuard([]*transaction.Transaction{tx1, tx2, tx3})
	if safe {
		t.Fatal("expected sandwich pattern to be flagged")
	}
	if len(flagged) != 3 {
		t.Fatalf("flagged = %d, want 3", len(flagged))
	}

	// No fairness mode: always safe.
	optNoFairness := NewAIBlockOptimizer(false, 0, false, nil)
	safe, flagged = optNoFairness.FairnessGuard([]*transaction.Transaction{tx1, tx2, tx3})
	if !safe {
		t.Fatal("expected safe when fairness mode is disabled")
	}
	if len(flagged) != 0 {
		t.Fatalf("flagged = %d, want 0 when fairness disabled", len(flagged))
	}

	// Too few transactions.
	safe, _ = opt.FairnessGuard([]*transaction.Transaction{tx1, tx2})
	if !safe {
		t.Fatal("expected safe with fewer than 3 transactions")
	}
}

func TestAIBlockOptimizer_DetectMEV(t *testing.T) {
	opt := NewAIBlockOptimizer(false, 0, false, nil)

	target := types.HexToAddress("0xDEX")
	addr1 := types.HexToAddress("0x01")

	// Create 3 transactions to the same target (potential arbitrage).
	txs := make([]*transaction.Transaction, 3)
	for i := 0; i < 3; i++ {
		txs[i] = makeLegacyTx(uint64(i), addr1, &target, uint256.NewInt(1000000000), 21000, uint256.NewInt(0))
	}

	opportunities := opt.DetectMEV(txs)
	found := false
	for _, opp := range opportunities {
		if opp.Type == "arbitrage" {
			found = true
			if len(opp.TxHashes) < 3 {
				t.Fatalf("arbitrage tx hashes = %d, want >= 3", len(opp.TxHashes))
			}
		}
	}
	if !found {
		t.Fatal("expected arbitrage opportunity to be detected")
	}

	// Large value transfer (liquidation detection).
	liquidationValue := new(uint256.Int).Mul(uint256.NewInt(200), uint256.NewInt(1_000_000_000_000_000_000))
	largeTx := makeLegacyTx(0, addr1, &target, uint256.NewInt(1000000000), 21000, liquidationValue)
	opps := opt.DetectMEV([]*transaction.Transaction{largeTx})

	foundLiquidation := false
	for _, opp := range opps {
		if opp.Type == "liquidation" {
			foundLiquidation = true
		}
	}
	if !foundLiquidation {
		t.Fatal("expected liquidation opportunity to be detected for large value transfer")
	}

	// Empty input.
	if opps := opt.DetectMEV(nil); opps != nil {
		t.Fatal("expected nil for empty input")
	}
}

func TestAIBlockOptimizer_Fallback(t *testing.T) {
	opt := NewAIBlockOptimizer(false, 0, true, nil) // fallbackOnError = true

	// Create transactions that could potentially cause issues.
	addr := types.HexToAddress("0x01")
	target := types.HexToAddress("0xAA")

	txs := []*transaction.Transaction{
		makeLegacyTx(0, addr, &target, uint256.NewInt(1000000000), 21000, uint256.NewInt(0)),
		makeLegacyTx(1, addr, &target, uint256.NewInt(2000000000), 21000, uint256.NewInt(0)),
	}

	// Normal case: should optimize correctly.
	result := opt.OptimizeOrdering(txs, uint256.NewInt(500000000))
	if len(result) != 2 {
		t.Fatalf("fallback result len = %d, want 2", len(result))
	}
}

func TestGasPredictor_Empty(t *testing.T) {
	gp := NewGasPredictor(10)

	// PredictBaseFee with no observations should return nil.
	pred := gp.PredictBaseFee()
	if pred != nil {
		t.Fatalf("expected nil prediction with no observations, got %+v", pred)
	}
}

func TestGasPredictor_SingleObservation(t *testing.T) {
	gp := NewGasPredictor(10)

	gp.AddObservation(GasObservation{
		BlockNumber: 1,
		BaseFee:     42000,
		GasUsed:     15000000,
		GasLimit:    30000000,
	})

	pred := gp.PredictBaseFee()
	if pred == nil {
		t.Fatal("expected non-nil prediction after single observation")
	}

	// With a single observation, EWMA equals the observation value.
	if pred.PredictedBaseFee != 42000 {
		t.Fatalf("predicted base fee = %d, want 42000", pred.PredictedBaseFee)
	}

	// Confidence should be low: 1/10 = 0.1.
	if math.Abs(pred.Confidence-0.1) > 0.01 {
		t.Fatalf("confidence = %f, want 0.1", pred.Confidence)
	}

	if pred.BasedOnBlocks != 1 {
		t.Fatalf("based on blocks = %d, want 1", pred.BasedOnBlocks)
	}
}

func TestGasPredictor_LastObservation(t *testing.T) {
	gp := NewGasPredictor(10)

	// Empty history should return nil.
	if obs := gp.LastObservation(); obs != nil {
		t.Fatal("expected nil LastObservation with empty history")
	}

	gp.AddObservation(GasObservation{BlockNumber: 1, BaseFee: 100})
	gp.AddObservation(GasObservation{BlockNumber: 2, BaseFee: 200})
	gp.AddObservation(GasObservation{BlockNumber: 3, BaseFee: 300})

	last := gp.LastObservation()
	if last == nil {
		t.Fatal("expected non-nil LastObservation")
	}
	if last.BlockNumber != 3 {
		t.Fatalf("last observation block = %d, want 3", last.BlockNumber)
	}
	if last.BaseFee != 300 {
		t.Fatalf("last observation base fee = %d, want 300", last.BaseFee)
	}
}

func TestAIBlockOptimizer_EmptyTxList(t *testing.T) {
	opt := NewAIBlockOptimizer(false, 0, false, nil)
	baseFee := uint256.NewInt(1000000000)

	// Nil input should return nil without panic.
	result := opt.OptimizeOrdering(nil, baseFee)
	if result != nil {
		t.Fatalf("expected nil result for nil input, got len %d", len(result))
	}

	// Empty slice should return nil without panic.
	result = opt.OptimizeOrdering([]*transaction.Transaction{}, baseFee)
	if result != nil {
		t.Fatalf("expected nil result for empty input, got len %d", len(result))
	}
}

func TestAIBlockOptimizer_ScoreBundle(t *testing.T) {
	opt := NewAIBlockOptimizer(false, 0, false, nil)
	baseFee := uint256.NewInt(1000000000) // 1 Gwei

	addr := types.HexToAddress("0x01")
	target := types.HexToAddress("0xAA")

	txs := []*transaction.Transaction{
		makeDynFeeTx(0, addr, &target, uint256.NewInt(2000000000), uint256.NewInt(5000000000), 21000, uint256.NewInt(0)),
		makeDynFeeTx(1, addr, &target, uint256.NewInt(3000000000), uint256.NewInt(8000000000), 21000, uint256.NewInt(0)),
	}

	score := opt.ScoreBundle(txs, baseFee)
	if score <= 0 {
		t.Fatalf("expected positive bundle score, got %f", score)
	}

	// Empty bundle should score 0.
	emptyScore := opt.ScoreBundle(nil, baseFee)
	if emptyScore != 0 {
		t.Fatalf("expected 0 score for empty bundle, got %f", emptyScore)
	}
}

func TestAIBlockOptimizer_Stats(t *testing.T) {
	opt := NewAIBlockOptimizer(false, 0, false, nil)
	baseFee := uint256.NewInt(1000000000)

	addr := types.HexToAddress("0x01")
	target := types.HexToAddress("0xAA")

	tx := makeLegacyTx(0, addr, &target, uint256.NewInt(2000000000), 21000, uint256.NewInt(0))

	if opt.Stats() != 0 {
		t.Fatalf("initial stats = %d, want 0", opt.Stats())
	}

	opt.OptimizeOrdering([]*transaction.Transaction{tx}, baseFee)
	if opt.Stats() != 1 {
		t.Fatalf("stats after 1 optimization = %d, want 1", opt.Stats())
	}

	opt.OptimizeOrdering([]*transaction.Transaction{tx}, baseFee)
	opt.OptimizeOrdering([]*transaction.Transaction{tx}, baseFee)
	if opt.Stats() != 3 {
		t.Fatalf("stats after 3 optimizations = %d, want 3", opt.Stats())
	}
}

func TestAIBlockOptimizer_NilBaseFee(t *testing.T) {
	opt := NewAIBlockOptimizer(false, 0, false, nil)

	addr := types.HexToAddress("0x01")
	target := types.HexToAddress("0xAA")

	txs := []*transaction.Transaction{
		makeLegacyTx(0, addr, &target, uint256.NewInt(2000000000), 21000, uint256.NewInt(0)),
		makeDynFeeTx(1, addr, &target, uint256.NewInt(1000000000), uint256.NewInt(3000000000), 21000, uint256.NewInt(0)),
	}

	// Should not panic with nil baseFee; internally uses zero.
	result := opt.OptimizeOrdering(txs, nil)
	if len(result) != 2 {
		t.Fatalf("result len = %d, want 2", len(result))
	}
}
