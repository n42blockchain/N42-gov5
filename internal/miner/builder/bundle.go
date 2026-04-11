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
// Transaction bundle primitives for the local block builder. Models
// ordered, atomic transaction bundles (Flashbots-style) with
// acceptance deadlines, holiman/uint256 effective tip accounting and
// locking to protect concurrent inclusion decisions from multiple
// worker goroutines.

package builder

import (
	"errors"
	"sync"
	"time"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
)

var (
	ErrBundleExpired    = errors.New("bundle has expired")
	ErrBundleEmpty      = errors.New("bundle has no transactions")
	ErrBundleTooLarge   = errors.New("bundle exceeds max transactions")
	ErrBundleBlockRange = errors.New("bundle block number out of range")
)

const (
	// MaxBundleTxs is the maximum number of transactions in a bundle.
	MaxBundleTxs = 50

	// MaxPendingBundles is the maximum number of pending bundles per block.
	MaxPendingBundles = 256

	// BundleExpiry is how long a bundle lives without being included.
	BundleExpiry = 5 * time.Minute
)

// Bundle represents an atomic group of transactions that must be included
// together, in order, at the top of a block. Used for MEV extraction,
// backrunning, and sandwich protection.
type Bundle struct {
	Txs              []*transaction.Transaction // Transactions to execute atomically
	BlockNumber      uint64                     // Target block number
	MinTimestamp      uint64                     // Earliest block timestamp (0 = no constraint)
	MaxTimestamp      uint64                     // Latest block timestamp (0 = no constraint)
	RevertingTxHashes []types.Hash               // Tx hashes allowed to revert without failing bundle
	SubmittedAt       time.Time                  // When the bundle was received
	BundlePrice       *uint256.Int               // Total tip offered by bundle (computed)
}

// BundlePool manages pending transaction bundles for block construction.
type BundlePool struct {
	mu      sync.RWMutex
	bundles map[uint64][]*Bundle // blockNumber → pending bundles
}

// NewBundlePool creates a new bundle pool.
func NewBundlePool() *BundlePool {
	return &BundlePool{
		bundles: make(map[uint64][]*Bundle),
	}
}

// AddBundle validates and adds a bundle to the pool.
func (bp *BundlePool) AddBundle(bundle *Bundle) error {
	if len(bundle.Txs) == 0 {
		return ErrBundleEmpty
	}
	if len(bundle.Txs) > MaxBundleTxs {
		return ErrBundleTooLarge
	}
	if bundle.BlockNumber == 0 {
		return ErrBundleBlockRange
	}
	bundle.SubmittedAt = time.Now()

	// Compute total bundle tip.
	totalTip := new(uint256.Int)
	for _, tx := range bundle.Txs {
		tip := tx.GasTipCap()
		gas := new(uint256.Int).SetUint64(tx.Gas())
		txTip := new(uint256.Int).Mul(tip, gas)
		totalTip.Add(totalTip, txTip)
	}
	bundle.BundlePrice = totalTip

	bp.mu.Lock()
	defer bp.mu.Unlock()

	bundles := bp.bundles[bundle.BlockNumber]
	if len(bundles) >= MaxPendingBundles {
		// Evict lowest-priced bundle if new one pays more.
		minIdx := 0
		for i := 1; i < len(bundles); i++ {
			if bundles[i].BundlePrice.Cmp(bundles[minIdx].BundlePrice) < 0 {
				minIdx = i
			}
		}
		if bundle.BundlePrice.Cmp(bundles[minIdx].BundlePrice) <= 0 {
			return nil // New bundle not profitable enough
		}
		bundles[minIdx] = bundle
	} else {
		bp.bundles[bundle.BlockNumber] = append(bundles, bundle)
	}
	return nil
}

// GetBundles returns all valid bundles for a given block number and timestamp.
// Bundles are sorted by price (highest first).
func (bp *BundlePool) GetBundles(blockNumber uint64, blockTimestamp uint64) []*Bundle {
	bp.mu.RLock()
	defer bp.mu.RUnlock()

	candidates := bp.bundles[blockNumber]
	if len(candidates) == 0 {
		return nil
	}

	now := time.Now()
	var valid []*Bundle
	for _, b := range candidates {
		if now.Sub(b.SubmittedAt) > BundleExpiry {
			continue
		}
		if b.MinTimestamp > 0 && blockTimestamp < b.MinTimestamp {
			continue
		}
		if b.MaxTimestamp > 0 && blockTimestamp > b.MaxTimestamp {
			continue
		}
		valid = append(valid, b)
	}

	// Sort by bundle price descending.
	for i := 1; i < len(valid); i++ {
		for j := i; j > 0 && valid[j].BundlePrice.Cmp(valid[j-1].BundlePrice) > 0; j-- {
			valid[j], valid[j-1] = valid[j-1], valid[j]
		}
	}

	return valid
}

// PruneBundles removes bundles for blocks older than the given number.
func (bp *BundlePool) PruneBundles(currentBlock uint64) {
	bp.mu.Lock()
	defer bp.mu.Unlock()

	for blockNum := range bp.bundles {
		if blockNum < currentBlock {
			delete(bp.bundles, blockNum)
		}
	}
}

// revertingHash checks if a tx hash is in the reverting set.
func (b *Bundle) IsRevertAllowed(txHash types.Hash) bool {
	for _, h := range b.RevertingTxHashes {
		if h == txHash {
			return true
		}
	}
	return false
}
