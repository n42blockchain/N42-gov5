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
	"sync"
)

const (
	// defaultGasWindowSize is the default number of blocks to consider
	// for gas prediction.
	defaultGasWindowSize = 32

	// defaultAlpha is the default exponential smoothing factor.
	// A lower alpha gives more weight to historical observations,
	// while a higher alpha responds faster to recent changes.
	defaultAlpha = 0.3
)

// GasObservation records gas metrics for one block.
type GasObservation struct {
	BlockNumber uint64
	BaseFee     uint64
	GasUsed     uint64
	GasLimit    uint64
	TxCount     int
	Timestamp   uint64
}

// GasPrediction contains predicted gas values for the next block.
type GasPrediction struct {
	PredictedBaseFee uint64
	PredictedGasUsed uint64
	Confidence       float64
	BasedOnBlocks    int
}

// GasPredictor predicts gas prices using exponentially weighted moving average.
// It maintains a sliding window of recent block observations and applies EWMA
// smoothing to produce stable predictions that adapt to changing network conditions.
type GasPredictor struct {
	mu         sync.RWMutex
	history    []GasObservation
	windowSize int
	alpha      float64 // exponential smoothing factor (0-1)
}

// NewGasPredictor creates a new GasPredictor with the given window size.
// If windowSize <= 0, it defaults to 32 blocks.
// The smoothing factor alpha is set to 0.3, which provides a balance
// between responsiveness and stability.
func NewGasPredictor(windowSize int) *GasPredictor {
	if windowSize <= 0 {
		windowSize = defaultGasWindowSize
	}
	return &GasPredictor{
		history:    make([]GasObservation, 0, windowSize),
		windowSize: windowSize,
		alpha:      defaultAlpha,
	}
}

// AddObservation appends a block observation to the history.
// If the history exceeds the window size, the oldest observation is removed.
func (gp *GasPredictor) AddObservation(obs GasObservation) {
	gp.mu.Lock()
	defer gp.mu.Unlock()

	gp.history = append(gp.history, obs)

	// Trim to window size, keeping the most recent observations.
	if len(gp.history) > gp.windowSize {
		excess := len(gp.history) - gp.windowSize
		copy(gp.history, gp.history[excess:])
		gp.history = gp.history[:gp.windowSize]
	}
}

// PredictBaseFee predicts the base fee for the next block using EWMA
// over the recent fee history. Returns nil if no observations are available.
// Confidence increases linearly with the number of observations up to the
// full window size.
func (gp *GasPredictor) PredictBaseFee() *GasPrediction {
	gp.mu.RLock()
	defer gp.mu.RUnlock()

	if len(gp.history) == 0 {
		return nil
	}

	values := make([]float64, len(gp.history))
	for i, obs := range gp.history {
		values[i] = float64(obs.BaseFee)
	}

	predicted := ewma(values, gp.alpha)
	confidence := float64(len(gp.history)) / float64(gp.windowSize)
	if confidence > 1.0 {
		confidence = 1.0
	}

	return &GasPrediction{
		PredictedBaseFee: uint64(predicted),
		Confidence:       confidence,
		BasedOnBlocks:    len(gp.history),
	}
}

// PredictGasUsage predicts gas usage for the next block using EWMA
// over the recent gas usage history. Returns nil if no observations are available.
func (gp *GasPredictor) PredictGasUsage() *GasPrediction {
	gp.mu.RLock()
	defer gp.mu.RUnlock()

	if len(gp.history) == 0 {
		return nil
	}

	values := make([]float64, len(gp.history))
	for i, obs := range gp.history {
		values[i] = float64(obs.GasUsed)
	}

	predicted := ewma(values, gp.alpha)
	confidence := float64(len(gp.history)) / float64(gp.windowSize)
	if confidence > 1.0 {
		confidence = 1.0
	}

	return &GasPrediction{
		PredictedGasUsed: uint64(predicted),
		Confidence:       confidence,
		BasedOnBlocks:    len(gp.history),
	}
}

// ObservationCount returns the current number of observations in the history.
func (gp *GasPredictor) ObservationCount() int {
	gp.mu.RLock()
	defer gp.mu.RUnlock()
	return len(gp.history)
}

// LastObservation returns the most recent observation, or nil if the history
// is empty.
func (gp *GasPredictor) LastObservation() *GasObservation {
	gp.mu.RLock()
	defer gp.mu.RUnlock()

	if len(gp.history) == 0 {
		return nil
	}

	obs := gp.history[len(gp.history)-1]
	return &obs
}

// AverageUtilization computes the average gas utilization (gasUsed/gasLimit)
// across the observation window. Returns 0 if no observations are available
// or if any gas limit is zero.
func (gp *GasPredictor) AverageUtilization() float64 {
	gp.mu.RLock()
	defer gp.mu.RUnlock()

	if len(gp.history) == 0 {
		return 0
	}

	var total float64
	var count int
	for _, obs := range gp.history {
		if obs.GasLimit == 0 {
			continue
		}
		total += float64(obs.GasUsed) / float64(obs.GasLimit)
		count++
	}

	if count == 0 {
		return 0
	}

	return total / float64(count)
}

// ewma calculates the exponentially weighted moving average over a slice
// of values. Alpha controls the decay: higher alpha (closer to 1) gives
// more weight to recent values; lower alpha (closer to 0) gives more
// weight to historical values.
//
// The EWMA is computed iteratively:
//
//	S_0 = values[0]
//	S_t = alpha * values[t] + (1 - alpha) * S_{t-1}
func ewma(values []float64, alpha float64) float64 {
	if len(values) == 0 {
		return 0
	}

	result := values[0]
	for i := 1; i < len(values); i++ {
		result = alpha*values[i] + (1-alpha)*result
	}
	return result
}
