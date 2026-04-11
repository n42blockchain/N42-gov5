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
// AIBlockOptimizer: scoring-based block assembler that reorders pending
// transactions to maximise proposer value while honouring fairness
// constraints. Computes scoredTx entries from effective tip, gas
// efficiency and original index for stable sort, detects arbitrage and
// liquidation patterns (DetectMEV, liquidationThreshold = 100 ETH) and
// flags potential sandwich attacks via FairnessGuard. Returns optimised
// ordering plus a slice of MEVOpportunity descriptors.

package mev

import (
	"math"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
)

// liquidationThreshold is the minimum value (100 ETH in wei) for a transfer
// to be flagged as a potential liquidation event.
var liquidationThreshold = new(uint256.Int).Mul(
	uint256.NewInt(100),
	uint256.NewInt(1_000_000_000_000_000_000),
)

// MEVOpportunity represents a detected MEV opportunity.
type MEVOpportunity struct {
	Type           string // "arbitrage", "liquidation", "sandwich"
	TxHashes       []types.Hash
	EstimatedValue *uint256.Int
	Risk           float64
}

// scoredTx pairs a transaction with its computed priority score and
// original index for stable sorting.
type scoredTx struct {
	tx    *transaction.Transaction
	score float64
	index int // original index for stability
}

// AIBlockOptimizer uses scoring models to optimize transaction ordering
// for maximum block value while maintaining fairness constraints.
// It scores transactions based on effective tip, gas efficiency, and
// nonce ordering, then reorders them to maximize total block revenue.
// When fairness mode is enabled, it additionally flags potentially
// harmful orderings such as sandwich attacks.
type AIBlockOptimizer struct {
	mu                sync.RWMutex
	fairnessMode      bool
	optimizationLevel int
	fallbackOnError   bool
	gasPredictor      *GasPredictor
	blocksOptimized   atomic.Uint64
}

// NewAIBlockOptimizer creates a new AIBlockOptimizer.
//   - fairnessMode: when true, enables sandwich attack detection via FairnessGuard.
//   - optLevel: optimization aggressiveness (0 = minimal reordering, higher = more aggressive).
//   - fallbackOnError: when true, returns the original ordering on any internal error or panic.
//   - gasPredictor: optional GasPredictor for gas-aware scoring (may be nil).
func NewAIBlockOptimizer(fairnessMode bool, optLevel int, fallbackOnError bool, gasPredictor *GasPredictor) *AIBlockOptimizer {
	return &AIBlockOptimizer{
		fairnessMode:      fairnessMode,
		optimizationLevel: optLevel,
		fallbackOnError:   fallbackOnError,
		gasPredictor:      gasPredictor,
	}
}

// OptimizeOrdering scores and reorders transactions to maximize total block value.
// Scoring factors include effective tip, gas efficiency (value per gas unit),
// and nonce ordering preservation for same-sender transactions.
//
// The returned slice is a new copy; the input slice is never modified.
// If fallbackOnError is true, the original ordering is returned on any
// panic or error during optimization.
func (opt *AIBlockOptimizer) OptimizeOrdering(txs []*transaction.Transaction, baseFee *uint256.Int) (result []*transaction.Transaction) {
	if len(txs) == 0 {
		return nil
	}

	// Fallback recovery if configured.
	if opt.fallbackOnError {
		defer func() {
			if r := recover(); r != nil {
				// Return a copy of the original ordering.
				result = make([]*transaction.Transaction, len(txs))
				copy(result, txs)
			}
		}()
	}

	if baseFee == nil {
		baseFee = new(uint256.Int)
	}

	// Build a scored copy for sorting.
	scored := make([]scoredTx, len(txs))
	for i, tx := range txs {
		scored[i] = scoredTx{
			tx:    tx,
			score: opt.scoreTx(tx, baseFee),
			index: i,
		}
	}

	// Sort by score descending, with original index as tiebreaker for stability.
	sort.SliceStable(scored, func(i, j int) bool {
		if math.Abs(scored[i].score-scored[j].score) > 1e-9 {
			return scored[i].score > scored[j].score
		}
		return scored[i].index < scored[j].index
	})

	// Preserve nonce ordering for same-sender transactions.
	// Group by sender, then sort each group by nonce, maintaining
	// the relative position of the first transaction in each group.
	if opt.optimizationLevel > 0 {
		scored = opt.preserveNonceOrder(scored)
	}

	result = make([]*transaction.Transaction, len(scored))
	for i, s := range scored {
		result[i] = s.tx
	}

	opt.blocksOptimized.Add(1)
	return result
}

// preserveNonceOrder ensures that transactions from the same sender
// maintain ascending nonce order while preserving the score-based
// positioning as much as possible.
func (opt *AIBlockOptimizer) preserveNonceOrder(scored []scoredTx) []scoredTx {
	// Track positions per sender.
	type senderGroup struct {
		indices []int // positions in the scored slice
	}
	groups := make(map[types.Address]*senderGroup)

	for i, s := range scored {
		if s.tx == nil {
			continue
		}
		from := s.tx.From()
		if from == nil {
			continue
		}
		addr := *from
		g, ok := groups[addr]
		if !ok {
			g = &senderGroup{}
			groups[addr] = g
		}
		g.indices = append(g.indices, i)
	}

	// For each sender with multiple transactions, sort their positions
	// so that nonces are ascending within those positions.
	for _, g := range groups {
		if len(g.indices) <= 1 {
			continue
		}

		// Collect the transactions at these positions.
		txsInGroup := make([]*transaction.Transaction, len(g.indices))
		for i, idx := range g.indices {
			txsInGroup[i] = scored[idx].tx
		}

		// Sort by nonce.
		sort.Slice(txsInGroup, func(a, b int) bool {
			return txsInGroup[a].Nonce() < txsInGroup[b].Nonce()
		})

		// Place back in the same positions (which are already sorted by score).
		sort.Ints(g.indices)
		for i, idx := range g.indices {
			scored[idx].tx = txsInGroup[i]
		}
	}

	return scored
}

// DetectMEV scans a transaction set for MEV patterns and returns detected
// opportunities. Currently detects:
//   - Arbitrage: clusters of transactions targeting the same contract address,
//     which may indicate arbitrage activity.
//   - Liquidation: large-value transfers that may represent liquidation events.
func (opt *AIBlockOptimizer) DetectMEV(txs []*transaction.Transaction) []MEVOpportunity {
	if len(txs) == 0 {
		return nil
	}

	var opportunities []MEVOpportunity

	// Detect same-contract clusters (potential arbitrage).
	targetCounts := make(map[types.Address][]types.Hash)
	for _, tx := range txs {
		to := tx.To()
		if to == nil {
			continue
		}
		targetCounts[*to] = append(targetCounts[*to], tx.Hash())
	}

	for _, hashes := range targetCounts {
		if len(hashes) >= 3 {
			hashCopy := make([]types.Hash, len(hashes))
			copy(hashCopy, hashes)
			opportunities = append(opportunities, MEVOpportunity{
				Type:           "arbitrage",
				TxHashes:       hashCopy,
				EstimatedValue: new(uint256.Int),
				Risk:           0.5,
			})
		}
	}

	// Detect large value transfers (potential liquidation).
	for _, tx := range txs {
		val := tx.Value()
		if val != nil && val.Cmp(liquidationThreshold) >= 0 {
			opportunities = append(opportunities, MEVOpportunity{
				Type:           "liquidation",
				TxHashes:       []types.Hash{tx.Hash()},
				EstimatedValue: new(uint256.Int).Set(val),
				Risk:           0.3,
			})
		}
	}

	return opportunities
}

// ScoreBundle calculates the total effective tip value of a transaction bundle
// relative to the given base fee. This is used to compare competing bundles
// for block inclusion priority.
func (opt *AIBlockOptimizer) ScoreBundle(txs []*transaction.Transaction, baseFee *uint256.Int) float64 {
	if len(txs) == 0 {
		return 0
	}

	if baseFee == nil {
		baseFee = new(uint256.Int)
	}

	var total float64
	for _, tx := range txs {
		tip := tx.EffectiveGasTipValue(baseFee)
		if tip == nil {
			continue
		}
		// Total tip = effectiveTip * gasLimit.
		tipValue := new(uint256.Int).Mul(tip, uint256.NewInt(tx.Gas()))
		if tipValue.IsUint64() {
			total += float64(tipValue.Uint64())
		} else {
			// For extremely large values, use a capped representation
			total += float64(math.MaxUint64)
		}
	}

	return total
}

// FairnessGuard detects potentially harmful transaction orderings.
// When fairness mode is enabled, it flags consecutive transactions targeting
// the same address that could indicate sandwich attacks (e.g., front-run +
// victim + back-run patterns).
//
// Returns safe=true if no issues are found, along with a list of flagged
// transaction hashes. When fairness mode is disabled, always returns safe=true.
func (opt *AIBlockOptimizer) FairnessGuard(txs []*transaction.Transaction) (safe bool, flagged []types.Hash) {
	opt.mu.RLock()
	fairness := opt.fairnessMode
	opt.mu.RUnlock()

	if !fairness {
		return true, nil
	}

	if len(txs) < 3 {
		return true, nil
	}

	// Detect consecutive same-target transactions (sandwich pattern).
	// A sandwich typically looks like: tx_A -> victim -> tx_A where
	// tx_A targets the same contract before and after the victim.
	flaggedSet := make(map[types.Hash]struct{})

	for i := 0; i < len(txs)-2; i++ {
		toA := txs[i].To()
		toC := txs[i+2].To()

		if toA == nil || toC == nil {
			continue
		}

		// Check if txs[i] and txs[i+2] target the same address,
		// with a different transaction in between — potential sandwich.
		if *toA == *toC {
			fromA := txs[i].From()
			fromC := txs[i+2].From()

			// Same sender wrapping another transaction is suspicious.
			if fromA != nil && fromC != nil && *fromA == *fromC {
				flaggedSet[txs[i].Hash()] = struct{}{}
				flaggedSet[txs[i+1].Hash()] = struct{}{}
				flaggedSet[txs[i+2].Hash()] = struct{}{}
			}
		}
	}

	if len(flaggedSet) == 0 {
		return true, nil
	}

	flagged = make([]types.Hash, 0, len(flaggedSet))
	for h := range flaggedSet {
		flagged = append(flagged, h)
	}

	return false, flagged
}

// Stats returns the total number of blocks optimized by this optimizer.
func (opt *AIBlockOptimizer) Stats() (blocksOptimized uint64) {
	return opt.blocksOptimized.Load()
}

// scoreTx computes a priority score for a single transaction.
// The score is based on the effective tip and gas efficiency:
//
//	effectiveTip = min(gasTipCap, gasFeeCap - baseFee)
//	gasEfficiency = effectiveTip / gas (value per gas unit)
//	score = float64(effectiveTip) * (1 + gasEfficiency)
//
// This formula rewards transactions that offer high tips with efficient
// gas usage, producing better block packing.
func (opt *AIBlockOptimizer) scoreTx(tx *transaction.Transaction, baseFee *uint256.Int) float64 {
	tip := tx.EffectiveGasTipValue(baseFee)
	if tip == nil || tip.IsZero() {
		return 0
	}

	effectiveTip := tip.Float64()
	gas := tx.Gas()
	if gas == 0 {
		return effectiveTip
	}

	gasEfficiency := effectiveTip / float64(gas)

	// Clamp gasEfficiency to avoid extreme scores from dust transactions.
	if math.IsInf(gasEfficiency, 0) || math.IsNaN(gasEfficiency) {
		gasEfficiency = 0
	}

	return effectiveTip * (1 + gasEfficiency)
}
