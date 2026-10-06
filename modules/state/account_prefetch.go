// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
)

// AccountPrefetch is a reader layer that serves accounts a caller fetched
// ahead of time, in parallel, and falls through to base for everything else.
//
// The parallel processor leaves a block's delta-credited recipients (an
// account only ever credited during the block is never read by a worker --
// the balance delta goes to the multi-version store) as pending balance
// increases on the IntraBlockState. The block end folds them: FinalizeTx
// and IntermediateRoot walk the pending addresses in sorted order and
// getStateObject reads each one from the store, serially -- ~23k QMDB
// lookups a full block, 85 ms of a follower's import and the same on the
// leader's build (round 35zzh's profile). The processor now reads them
// across its workers first and seeds this layer; the fold's reads, still
// in the same order through the same chain (the mobile read-log recorder
// sits above this layer and logs exactly what it logged before), become
// map hits.
//
// A seeded nil means the account is absent. Values are copied on the way
// out so a caller that mutates the returned account cannot poison a later
// read.
type AccountPrefetch struct {
	base  StateReader
	accts map[types.Address]*account.StateAccount
	hits  int
}

// NewAccountPrefetch wraps base. Until Seed is called every read falls
// through.
func NewAccountPrefetch(base StateReader) *AccountPrefetch {
	return &AccountPrefetch{base: base}
}

// Seed installs the prefetched accounts, replacing any earlier set.
func (p *AccountPrefetch) Seed(accts map[types.Address]*account.StateAccount) {
	p.accts = accts
	p.hits = 0
}

// Hits reports how many reads the seeded set answered since the last Seed.
func (p *AccountPrefetch) Hits() int { return p.hits }

// Base returns the wrapped reader.
func (p *AccountPrefetch) Base() StateReader { return p.base }

func (p *AccountPrefetch) ReadAccountData(address types.Address) (*account.StateAccount, error) {
	if p.accts != nil {
		if a, ok := p.accts[address]; ok {
			p.hits++
			if a == nil {
				return nil, nil
			}
			c := new(account.StateAccount)
			c.Copy(a)
			return c, nil
		}
	}
	return p.base.ReadAccountData(address)
}

func (p *AccountPrefetch) ReadAccountStorage(address types.Address, key *types.Hash) ([]byte, error) {
	return p.base.ReadAccountStorage(address, key)
}

func (p *AccountPrefetch) ReadAccountCode(address types.Address, codeHash types.Hash) ([]byte, error) {
	return p.base.ReadAccountCode(address, codeHash)
}

func (p *AccountPrefetch) ReadAccountCodeSize(address types.Address, codeHash types.Hash) (int, error) {
	return p.base.ReadAccountCodeSize(address, codeHash)
}

// SetAccountPrefetch records the prefetch layer under this state's reader
// so the parallel processor can seed it without walking the reader chain.
func (sdb *IntraBlockState) SetAccountPrefetch(p *AccountPrefetch) { sdb.accountPrefetch = p }

// AccountPrefetch returns what SetAccountPrefetch recorded, or nil.
func (sdb *IntraBlockState) AccountPrefetch() *AccountPrefetch { return sdb.accountPrefetch }

// PendingBalanceIncreases lists the addresses whose balance increases are
// still unfolded and whose accounts this state has not read yet -- the
// reads the block-end fold would make from the store. Order is not
// significant; the fold itself iterates sorted.
func (sdb *IntraBlockState) PendingBalanceIncreases() []types.Address {
	if len(sdb.balanceInc) == 0 {
		return nil
	}
	out := make([]types.Address, 0, len(sdb.balanceInc))
	for addr, bi := range sdb.balanceInc {
		if bi == nil || bi.transferred {
			continue
		}
		if _, cached := sdb.stateObjects[addr]; cached {
			continue
		}
		if _, absent := sdb.nilAccounts[addr]; absent {
			continue
		}
		out = append(out, addr)
	}
	return out
}
