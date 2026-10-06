// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package parallel

import (
	"sync"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
)

// countingBase is a base state whose account reads are counted.
type countingBase struct {
	mu      sync.Mutex
	reads   map[types.Address]int
	present map[types.Address]*account.StateAccount
}

func newCountingBase() *countingBase {
	return &countingBase{reads: map[types.Address]int{}, present: map[types.Address]*account.StateAccount{}}
}

func (b *countingBase) ReadAccountData(address types.Address) (*account.StateAccount, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.reads[address]++
	acc, ok := b.present[address]
	if !ok {
		return nil, nil
	}
	cp := *acc
	return &cp, nil
}

func (b *countingBase) ReadAccountStorage(types.Address, *types.Hash) ([]byte, error) {
	return nil, nil
}
func (b *countingBase) ReadAccountCode(types.Address, types.Hash) ([]byte, error)  { return nil, nil }
func (b *countingBase) ReadAccountCodeSize(types.Address, types.Hash) (int, error) { return 0, nil }

func (b *countingBase) count(a types.Address) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.reads[a]
}

func readerOver(base *countingBase, cache *BaseCache) *ParallelStateReader {
	r := NewParallelStateReader(base, NewMVS(), NewReadWriteSet(0), 0)
	r.SetBaseCache(cache)
	return r
}

// The block's base state is read once per address, however many transactions
// touch it: each transaction has its own IntraBlockState, so without the cache
// a 163k-transfer block pays 163k reads for ~23k accounts.
func TestBaseCacheReadsEachAccountOnce(t *testing.T) {
	base := newCountingBase()
	addr := types.Address{0xA1}
	base.present[addr] = &account.StateAccount{Initialised: true, Nonce: 7, Balance: *uint256.NewInt(1000)}
	cache := NewBaseCache(8)

	for i := 0; i < 5; i++ {
		r := readerOver(base, cache)
		acc, err := r.ReadAccountData(addr)
		if err != nil {
			t.Fatal(err)
		}
		if acc == nil || acc.Nonce != 7 || acc.Balance.Uint64() != 1000 {
			t.Fatalf("read %d returned %+v", i, acc)
		}
	}
	if got := base.count(addr); got != 1 {
		t.Fatalf("base was read %d times, want 1", got)
	}
}

// An absent account is cached as absent: a block's transfers to fresh
// addresses would otherwise search the tree once per transaction.
func TestBaseCacheRemembersAbsence(t *testing.T) {
	base := newCountingBase()
	addr := types.Address{0xB2}
	cache := NewBaseCache(8)

	for i := 0; i < 3; i++ {
		acc, err := readerOver(base, cache).ReadAccountData(addr)
		if err != nil || acc != nil {
			t.Fatalf("read %d = %+v, %v; want nil, nil", i, acc, err)
		}
	}
	if got := base.count(addr); got != 1 {
		t.Fatalf("base was read %d times for an absent account, want 1", got)
	}
}

// What the cache hands back is a copy: a caller that mutates it cannot change
// what the next transaction reads.
func TestBaseCacheHandsOutCopies(t *testing.T) {
	base := newCountingBase()
	addr := types.Address{0xC3}
	base.present[addr] = &account.StateAccount{Initialised: true, Nonce: 1, Balance: *uint256.NewInt(5)}
	cache := NewBaseCache(8)

	first, err := readerOver(base, cache).ReadAccountData(addr)
	if err != nil {
		t.Fatal(err)
	}
	first.Nonce = 99
	first.Balance = *uint256.NewInt(0)

	second, err := readerOver(base, cache).ReadAccountData(addr)
	if err != nil {
		t.Fatal(err)
	}
	if second.Nonce != 1 || second.Balance.Uint64() != 5 {
		t.Fatalf("a mutated copy leaked into the cache: %+v", second)
	}
}

// Nothing changes for a reader without a cache: the base is read every time.
func TestWithoutBaseCacheEveryReadHitsTheBase(t *testing.T) {
	base := newCountingBase()
	addr := types.Address{0xD4}
	base.present[addr] = &account.StateAccount{Initialised: true}

	for i := 0; i < 4; i++ {
		if _, err := readerOver(base, nil).ReadAccountData(addr); err != nil {
			t.Fatal(err)
		}
	}
	if got := base.count(addr); got != 4 {
		t.Fatalf("base was read %d times without a cache, want 4", got)
	}
}
