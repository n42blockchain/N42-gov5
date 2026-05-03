// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// prewarm.go — main-thread synchronous AccessList prewarm. Race-free
// replacement for the prefetcher's removed storage LRU writes: same
// goroutine, same RoTx, unconditional Put via bufReader's MDBX-miss
// path.

package ethel

import (
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/state"
)

// prewarmStaticAL reads every distinct (address, slot) pair listed in
// the transactions' AccessLists through reader, populating the read
// LRU. Duplicate keys (common: ERC20 totalSupply, Uniswap pool slots
// touched by every swap) are deduped per block so a busy AL doesn't
// re-walk the read pipeline N times for the same slot.
//
// Errors are silently ignored — a prewarm miss only loses the LRU
// benefit; the EVM will retry the same read on its own and surface any
// real I/O error there.
func prewarmStaticAL(reader *state.BufferedPlainStateReader, txs []*transaction.Transaction) {
	if reader == nil {
		return
	}
	type slotKey struct {
		addr types.Address
		slot types.Hash
	}
	// Pre-size to skip 2-3 rehashes on a typical ~150-200-key block.
	seen := make(map[slotKey]struct{}, 256)
	for _, tx := range txs {
		for _, entry := range tx.AccessList() {
			for i := range entry.StorageKeys {
				k := slotKey{addr: entry.Address, slot: entry.StorageKeys[i]}
				if _, dup := seen[k]; dup {
					continue
				}
				seen[k] = struct{}{}
				_, _ = reader.ReadAccountStorage(k.addr, &k.slot)
			}
		}
	}
}
