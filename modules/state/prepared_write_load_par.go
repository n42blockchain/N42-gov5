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

package state

import (
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/envswitch"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

// Parallel pending loads for the sharded prepared write (r58a).
//
// With the loads deferred (prepDeferLoads) the serial part of a sharded
// write's Load step is: list and sort the ~23k pending credited addresses,
// then read each one's account -- in the benchmark a hit in the
// AccountPrefetch layer (the seeded map or the parallel run's known-account
// cache), copied out -- and queue it for its shard. ~16 ms of the ~33 ms
// prepare. None of the reads touch the store: the cost is the sort, the
// map probes (cache misses on ~23k-entry maps) and the copies.
//
// N42_STATE_WRITE_PREP_LOAD_PAR=<n> (default 0: serial) splits that work
// over n goroutines by shard. Each shard's addresses are its contiguous
// first-byte range, so the shard lists sorted on their own and concatenated
// in shard order are exactly the sorted pending list. The workers only
// read: the IntraBlockState's maps (nothing writes them during the phase)
// and the prefetch layer's seeded map and known cache (KnownAccounts is
// safe for concurrent use). Whatever needs a write or the store -- a live
// object's fold (getStateObject), a prefetch miss (read through the full
// reader chain, whose MDBX cursors are not goroutine-safe), the snapshot
// insert, the hit counter -- runs afterwards on the calling goroutine, in
// the sorted pending order, as the serial loop does. The credited loads,
// and so every row downstream, come out identical.
//
// A single sorted pass with one cursor was not chosen: the reads are map
// hits, not store reads, so there is no seek cost for key order to save.
//
// The path is only taken when the state's reader is the prefetch layer
// itself (no recorder above it); otherwise the serial loop runs.
var prepLoadPar = parsePrepLoadPar(envswitch.Raw("N42_STATE_WRITE_PREP_LOAD_PAR", envswitch.Perf, "Parallel pending loads for the sharded prepared write (r58a)."))

func parsePrepLoadPar(v string) int {
	if v == "" {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 2 {
		return 0
	}
	if n > 256 {
		return 256
	}
	return n
}

// SetPrepLoadParForTest sets the parallel load worker count (0 or 1:
// serial) and returns the restore.
func SetPrepLoadParForTest(n int) func() {
	prev := prepLoadPar
	prepLoadPar = n
	return func() { prepLoadPar = prev }
}

// lookupConcurrent is ReadAccountData's seeded/known answer without the hit
// count, safe to call from several goroutines while nothing seeds the
// layer: a copy of the account (nil: absent) and whether either cache had
// it. A miss must be read through ReadAccountData.
func (p *AccountPrefetch) lookupConcurrent(address types.Address) (*account.StateAccount, bool) {
	if p.accts != nil {
		if a, ok := p.accts[address]; ok {
			if a == nil {
				return nil, true
			}
			c := new(account.StateAccount)
			c.Copy(a)
			return c, true
		}
	}
	if p.known != nil {
		if a, ok := p.known.Get(address); ok {
			return a, true
		}
	}
	return nil, false
}

// parLoadEntry is one pending address's classification by a load worker.
type parLoadEntry struct {
	addr   types.Address
	live   bool // already a live object: folded serially by getStateObject
	absent bool // a known-absent account (nilAccounts)
	hit    bool // acc came from the prefetch layer
	acc    *account.StateAccount
	bi     *BalanceIncrease
}

// commitBlockShardsParLoad is commitBlockShards with the deferred pending
// loads read across prepLoadPar goroutines (see prepLoadPar).
func (sdb *IntraBlockState) commitBlockShardsParLoad(chainRules *params.Rules, shards int, ph *PrepPhases, t0 time.Time) ([]*StateWriteLog, error) {
	addrBuckets := make([][]types.Address, shards)
	pending := 0
	for addr, bi := range sdb.balanceInc {
		if bi != nil && !bi.transferred {
			k := int(addr[0]) * shards / 256
			addrBuckets[k] = append(addrBuckets[k], addr)
			pending++
		}
	}
	buckets := make([][]parLoadEntry, shards)
	pf := sdb.accountPrefetch
	workers := prepLoadPar
	if workers > shards {
		workers = shards
	}
	var (
		next atomic.Int32
		hits atomic.Int64
		wg   sync.WaitGroup
	)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var h int64
			for {
				k := int(next.Add(1)) - 1
				if k >= shards {
					break
				}
				addrs := addrBuckets[k]
				sortAddresses(addrs)
				b := make([]parLoadEntry, len(addrs))
				buckets[k] = b
				for i, addr := range addrs {
					e := &b[i]
					e.addr, e.bi = addr, sdb.balanceInc[addr]
					if _, live := sdb.stateObjects[e.addr]; live {
						e.live = true
						continue
					}
					if _, absent := sdb.nilAccounts[e.addr]; absent {
						e.absent = true
						continue
					}
					if a, ok := pf.lookupConcurrent(e.addr); ok {
						e.acc, e.hit = a, true
						h++
					}
				}
			}
			hits.Add(h)
		}()
	}
	wg.Wait()
	pf.hits += int(hits.Load())

	// The serial loop's effects, in the sorted pending order.
	deferred := make([][]creditedLoad, shards)
	canSnap := sdb.snap != nil && sdb.snap.CanWrite()
	for k, b := range buckets {
		d := make([]creditedLoad, 0, len(b))
		for i := range b {
			e := &b[i]
			if e.live {
				sdb.getStateObject(e.addr)
				continue
			}
			ph.Loaded++
			var acc *account.StateAccount
			if !e.absent {
				acc = e.acc
				if !e.hit {
					a, err := sdb.stateReader.ReadAccountData(e.addr)
					if err != nil {
						return nil, err
					}
					acc = a
				}
				if acc != nil && canSnap {
					sdb.snap.AddAccount(e.addr, acc)
				}
			}
			d = append(d, creditedLoad{addr: e.addr, acc: acc, bi: e.bi})
		}
		deferred[k] = d
	}
	ph.Pending = pending
	return sdb.commitBlockShardsLoaded(chainRules, shards, ph, t0, deferred)
}
