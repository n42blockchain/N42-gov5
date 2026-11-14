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
// Pending-credit prefetch for the parallel path.

package internal

import (
	"context"
	"fmt"
	"sync"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

// prefetchPendingCredits reads the state's pending balance-increase
// addresses across goroutines, each on its own read transaction through the
// same reader stack the workers used, and seeds the prefetch layer with the
// result. Small sets are not worth the transactions and read through the
// fold as before.
// span is the caller's QMDB read span (nil: no QMDB reads); the workers'
// transactions are opened under its permit.
func (p *StateProcessor) prefetchPendingCredits(ibs *state.IntraBlockState, pf *state.AccountPrefetch, span *commitment.ReadSpan, useQMDB, ahead bool, mode commitment.QMDBReadMode, postLayers []*state.PostState, basePF *commitment.BasePrefill) (int, error) {
	if pf.HasKnown() {
		// The run's base-read cache answers the fold (SetKnown): every
		// credited account was read by the transaction that credited it,
		// so the cache holds them all, and a miss reads through as before.
		return 0, nil
	}
	addrs := ibs.PendingBalanceIncreases()
	if len(addrs) < 256 {
		return 0, nil
	}
	workers := parallelWorkers()
	if workers > 16 {
		workers = 16
	}
	if workers > len(addrs)/64 {
		workers = len(addrs) / 64
	}
	if workers < 1 {
		workers = 1
	}
	results := make([]*account.StateAccount, len(addrs))
	errs := make([]error, workers)
	var wg sync.WaitGroup
	per := (len(addrs) + workers - 1) / workers
	for w := 0; w < workers; w++ {
		lo, hi := w*per, (w+1)*per
		if hi > len(addrs) {
			hi = len(addrs)
		}
		if lo >= hi {
			break
		}
		wg.Add(1)
		go func(w, lo, hi int) {
			defer wg.Done()
			var tx kv.Tx
			var err error
			if span != nil {
				tx, err = span.BeginRo(context.Background())
			} else {
				tx, err = p.bc.ChainDB.BeginRo(context.Background())
			}
			if err != nil {
				errs[w] = fmt.Errorf("prefetch worker %d: open read transaction: %w", w, err)
				return
			}
			defer tx.Rollback()
			base := p.parallelBaseReader(tx, useQMDB, ahead, mode, postLayers, basePF)
			for i := lo; i < hi; i++ {
				a, err := base.ReadAccountData(addrs[i])
				if err != nil {
					errs[w] = fmt.Errorf("prefetch worker %d: %w", w, err)
					return
				}
				results[i] = a
			}
		}(w, lo, hi)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return 0, err
		}
	}
	accts := make(map[types.Address]*account.StateAccount, len(addrs))
	for i, addr := range addrs {
		accts[addr] = results[i]
	}
	pf.Seed(accts)
	return len(addrs), nil
}
