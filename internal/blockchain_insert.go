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
//
// Block chain insert pipeline. Implements InsertChain,
// insertBlockLocked and reorg handling: verifies each candidate
// block with BlockValidator, runs the StateProcessor against a
// cloned IntraBlockState, writes receipts, emits chain events and
// rewinds on fork switch.

package internal

import (
	"os"
	"strconv"
	"time"

	"github.com/n42blockchain/N42/common/block"
)

// slowBlockThreshold is the wall-clock cost above which the per-block phase
// breakdowns ("blockimport phases", "blockwrite phases") are logged at Info
// instead of Debug.
//
// These lines exist to explain an anomaly, but the threshold they used — 5 ms —
// sat below the median block, so every block logged one and the pair became
// 26% of all log bytes on this fleet, drowning the anomalies they were meant to
// surface. At the 30M/2000ms baseline a block write is p50 7.1 ms / p99 14.2 ms
// / max 46 ms, so 50 ms reports nothing in steady state and fires exactly when
// something is actually slow. Debug still carries every block for profiling.
//
// N42_SLOW_BLOCK_MS overrides it; 0 restores logging every block at Info.
var slowBlockThreshold = slowBlockThresholdFromEnv()

func slowBlockThresholdFromEnv() time.Duration {
	if v := os.Getenv("N42_SLOW_BLOCK_MS"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
			return time.Duration(n) * time.Millisecond
		}
	}
	return 50 * time.Millisecond
}

// insertIterator assists during chain import by providing validated blocks
// one at a time from a contiguous chain.
type insertIterator struct {
	chain   []block.IBlock
	results <-chan error
	errors  []error
	index   int
	validator Validator

	// Per-block phase timings for the "blockimport phases" line — OBSERVABILITY
	// ONLY. next() is where an importing node pays for header verification (the
	// wait on the parallel VerifyHeaders result, which includes the HotStuff BLS
	// seal check) and for ValidateBody (which recomputes the transaction root
	// over every transaction in the block). Both sit outside every existing
	// timer, so a slow import used to show up as unattributed wall clock.
	lastVerifyWait time.Duration
	lastBodyCheck  time.Duration
}

// newInsertIterator creates a new iterator for the given contiguous chain.
func newInsertIterator(chain []block.IBlock, results <-chan error, validator Validator) *insertIterator {
	return &insertIterator{
		chain:     chain,
		results:   results,
		errors:    make([]error, 0, len(chain)),
		index:     -1,
		validator: validator,
	}
}

// next returns the next block and any validation error.
// Returns (nil, nil) when the end is reached.
func (it *insertIterator) next() (block.IBlock, error) {
	it.lastVerifyWait, it.lastBodyCheck = 0, 0
	if it.index+1 >= len(it.chain) {
		it.index = len(it.chain)
		return nil, nil
	}
	it.index++
	if len(it.errors) <= it.index {
		tVerify := time.Now()
		it.errors = append(it.errors, <-it.results)
		it.lastVerifyWait = time.Since(tVerify)
	}
	if it.errors[it.index] != nil {
		return it.chain[it.index], it.errors[it.index]
	}
	tBody := time.Now()
	err := it.validator.ValidateBody(it.chain[it.index])
	it.lastBodyCheck = time.Since(tBody)
	return it.chain[it.index], err
}

// peek returns the next block without advancing the iterator.
func (it *insertIterator) peek() (block.IBlock, error) {
	if it.index+1 >= len(it.chain) {
		return nil, nil
	}
	if len(it.errors) <= it.index+1 {
		it.errors = append(it.errors, <-it.results)
	}
	if it.errors[it.index+1] != nil {
		return it.chain[it.index+1], it.errors[it.index+1]
	}
	return it.chain[it.index+1], nil
}

// previous returns the previous header, or nil.
func (it *insertIterator) previous() block.IHeader {
	if it.index < 1 {
		return nil
	}
	return it.chain[it.index-1].Header()
}

// current returns the current header, or nil.
func (it *insertIterator) current() block.IHeader {
	if it.index == -1 || it.index >= len(it.chain) {
		return nil
	}
	return it.chain[it.index].Header()
}

// first returns the first block in the chain.
func (it *insertIterator) first() block.IBlock {
	if len(it.chain) == 0 {
		return nil
	}
	return it.chain[0]
}

// remaining returns the number of remaining blocks.
func (it *insertIterator) remaining() int {
	return len(it.chain) - it.index
}

// processed returns the number of processed blocks.
func (it *insertIterator) processed() int {
	return it.index + 1
}
