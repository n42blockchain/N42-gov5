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

package parallel

import "github.com/n42blockchain/N42/common/transaction"

// PredictedExecutor wraps Executor with dependency prediction.
// Before execution, it reorders transactions to cluster likely-conflicting
// ones together, then maps results back to original order.
type PredictedExecutor struct {
	inner      *Executor
	predictor  *DependencyPredictor
	orderMap   []int // orderMap[predicted_i] = original index
	reverseMap []int // reverseMap[original_i] = predicted index
}

// NewPredictedExecutor creates an executor with dependency prediction.
// If predictor is nil or numTxs <= 4, falls back to plain executor (identity order).
func NewPredictedExecutor(numTxs, workers int, execFn TxExecuteFunc, predictor *DependencyPredictor, txs []*transaction.Transaction) *PredictedExecutor {
	pe := &PredictedExecutor{
		predictor: predictor,
	}

	// Build the reorder map. If predictor is nil or the batch is too small,
	// use identity mapping (no reordering).
	if predictor != nil && numTxs > 4 && len(txs) == numTxs {
		pe.orderMap = predictor.PredictOrder(txs)
	}

	if len(pe.orderMap) != numTxs {
		// Identity mapping — no reordering.
		pe.orderMap = make([]int, numTxs)
		for i := range pe.orderMap {
			pe.orderMap[i] = i
		}
	}

	// Build reverse map: reverseMap[original] = predicted position.
	pe.reverseMap = make([]int, numTxs)
	for predicted, original := range pe.orderMap {
		pe.reverseMap[original] = predicted
	}

	// Wrap execFn so that the inner executor sees predicted indices
	// but calls the original execFn with the original index.
	wrappedExecFn := func(predictedIndex int, rw *ReadWriteSet) error {
		originalIndex := pe.orderMap[predictedIndex]
		return execFn(originalIndex, rw)
	}

	pe.inner = NewExecutor(numTxs, workers, wrappedExecFn)
	return pe
}

// Run executes with prediction-based reordering and returns results
// in ORIGINAL transaction order.
func (pe *PredictedExecutor) Run() []TxResult {
	reorderedResults := pe.inner.Run()
	if reorderedResults == nil {
		return nil
	}

	// Map results back to original order.
	results := make([]TxResult, len(reorderedResults))
	for predicted, original := range pe.orderMap {
		results[original] = reorderedResults[predicted]
	}
	return results
}

// MVS returns the multi-version store from the inner executor.
func (pe *PredictedExecutor) MVS() *MVS {
	return pe.inner.MVS()
}

// Stats returns execution statistics from the inner executor.
func (pe *PredictedExecutor) Stats() (executions, aborts int64) {
	return pe.inner.Stats()
}
