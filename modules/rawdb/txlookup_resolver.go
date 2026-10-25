// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// txlookup_resolver.go lets a tier outside this package answer transaction
// lookups before the MDBX table is consulted.
//
// Every read path for "which block is this transaction in" funnels through
// ReadTxLookupEntry -- eth_getTransactionByHash, eth_getTransactionReceipt,
// the tracers, otterscan, graphql. When the newest blocks stop being written
// to the TxLookup table, all of those have to start asking somewhere else, and
// threading a lookup service through every RPC backend to say so would be a
// large change to code that has no other reason to move.
//
// So the resolver is installed once, by whatever owns the newer tiers, and is
// nil by default: with no resolver this package behaves exactly as it did.

package rawdb

import (
	"sync/atomic"

	"github.com/n42blockchain/N42/common/types"
)

// TxLookupResolver answers "which block holds this transaction" for blocks
// whose entries are not in the MDBX table.
type TxLookupResolver interface {
	// LookupTx returns the block number holding txHash. ok is false when this
	// resolver does not cover the transaction, which is not an error: the
	// caller falls through to the table.
	LookupTx(txHash types.Hash) (blockNumber uint64, ok bool)
}

// TxLookupResolverWithError lets a physically backed tier report corruption
// or I/O failure instead of making a missing answer look like a valid miss.
type TxLookupResolverWithError interface {
	LookupTxWithError(txHash types.Hash) (blockNumber uint64, ok bool, err error)
}

var txLookupResolver atomic.Pointer[TxLookupResolver]

// SetTxLookupResolver installs (or, with nil, removes) the resolver consulted
// before the MDBX table.
func SetTxLookupResolver(r TxLookupResolver) {
	if r == nil {
		txLookupResolver.Store(nil)
		return
	}
	txLookupResolver.Store(&r)
}

// TryInstallTxLookupResolver gives a database owner exclusive use of the
// process-wide resolver slot. A second owner must not silently replace it.
func TryInstallTxLookupResolver(r TxLookupResolver) (remove func() bool, ok bool) {
	if r == nil {
		return nil, false
	}
	p := &r
	if !txLookupResolver.CompareAndSwap(nil, p) {
		return nil, false
	}
	return func() bool {
		return txLookupResolver.CompareAndSwap(p, nil)
	}, true
}

// resolveTxLookup consults the installed resolver, if any.
//
// It runs BEFORE the table rather than after. The two cover disjoint block
// ranges in the steady state, so the order rarely matters -- but after a
// reorg a transaction can appear at a new height while a stale row for its old
// one is still in the table, and the newer tier is the one that knows.
func resolveTxLookup(txHash types.Hash) (uint64, bool, error) {
	p := txLookupResolver.Load()
	if p == nil {
		return 0, false, nil
	}
	if withError, ok := (*p).(TxLookupResolverWithError); ok {
		return withError.LookupTxWithError(txHash)
	}
	n, ok := (*p).LookupTx(txHash)
	return n, ok, nil
}
