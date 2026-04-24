// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package commitment

import (
	"context"
	"math/rand"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
)

// seedPlainStateBench is the benchmark-equivalent of seedPlainState from
// the concurrent-mpt test file (can't cross *testing.B vs *testing.T).
func seedPlainStateBench(b *testing.B, db kv.RwDB, accts map[types.Address]*account.StateAccount, stor map[types.Address]map[types.Hash]*uint256.Int) {
	b.Helper()
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		for a, acct := range accts {
			if acct == nil {
				continue
			}
			if err := tx.Put(modules.Account, a.Bytes(), acct.MarshalV2()); err != nil {
				return err
			}
		}
		for a, slots := range stor {
			for s, v := range slots {
				if v == nil || v.IsZero() {
					continue
				}
				key := modules.PlainGenerateCompositeStorageKey(a[:], s[:])
				bz := v.Bytes32()
				start := 0
				for start < 31 && bz[start] == 0 {
					start++
				}
				if err := tx.Put(modules.Storage, key, bz[start:]); err != nil {
					return err
				}
			}
		}
		return nil
	}); err != nil {
		b.Fatalf("seed: %v", err)
	}
}

// benchScenario controls the synthetic workload for MPT root computation.
type benchScenario struct {
	name      string
	nAcct     int // accounts pre-populated in PlainState
	nDirty    int // accounts dirty in this ComputeRoot call
	nStorPre  int // storage slots pre-populated per addr
	nStorDrty int // storage slots dirty per addr in this call
	storAddrs int // addresses with storage
}

// Benchmark_MPT_Sequential measures sequential MPTRootComputer.ComputeRoot
// on a warm state. Baseline for the concurrent benchmark.
func Benchmark_MPT_Sequential(b *testing.B) {
	scenarios := []benchScenario{
		{"acct-100-dirty-20", 100, 20, 0, 0, 0},
		{"acct-1000-dirty-200", 1000, 200, 0, 0, 0},
		{"mixed-500-dirty-100", 500, 100, 8, 2, 50},
		{"wide-2000-dirty-400", 2000, 400, 8, 2, 200},
	}
	for _, s := range scenarios {
		b.Run(s.name, func(b *testing.B) {
			benchRunSequential(b, s)
		})
	}
}

// Benchmark_MPT_Concurrent measures ConcurrentMPTRootComputer.ComputeRoot
// on the same scenarios. Speedup vs sequential = A / B where A = Sequential
// ns/op and B = Concurrent ns/op.
func Benchmark_MPT_Concurrent(b *testing.B) {
	scenarios := []benchScenario{
		{"acct-100-dirty-20", 100, 20, 0, 0, 0},
		{"acct-1000-dirty-200", 1000, 200, 0, 0, 0},
		{"mixed-500-dirty-100", 500, 100, 8, 2, 50},
		{"wide-2000-dirty-400", 2000, 400, 8, 2, 200},
	}
	for _, s := range scenarios {
		b.Run(s.name, func(b *testing.B) {
			benchRunConcurrent(b, s)
		})
	}
}

// benchBuildScenario generates pre-pop + dirty maps for one scenario.
// Uses a fixed-seed rand so both Sequential and Concurrent benches see
// identical data.
func benchBuildScenario(s benchScenario) (
	prePopAccts map[types.Address]*account.StateAccount,
	prePopStor map[types.Address]map[types.Hash]*uint256.Int,
	dirtyAccts map[types.Address]*account.StateAccount,
	dirtyStor map[types.Address]map[types.Hash]*uint256.Int,
) {
	rnd := rand.New(rand.NewSource(0xbeef))

	// Pre-populated state (seeded into PlainState before the benchmark loop).
	prePopAccts = make(map[types.Address]*account.StateAccount, s.nAcct)
	prePopStor = make(map[types.Address]map[types.Hash]*uint256.Int, s.storAddrs)

	addrs := make([]types.Address, 0, s.nAcct)
	for i := 0; i < s.nAcct; i++ {
		var a types.Address
		rnd.Read(a[:])
		addrs = append(addrs, a)
		acct := &account.StateAccount{
			Initialised: true,
			Nonce:       rnd.Uint64(),
		}
		acct.Balance.SetUint64(rnd.Uint64())
		prePopAccts[a] = acct
	}
	for i := 0; i < s.storAddrs && i < len(addrs); i++ {
		slots := make(map[types.Hash]*uint256.Int, s.nStorPre)
		for j := 0; j < s.nStorPre; j++ {
			var s types.Hash
			rnd.Read(s[:])
			slots[s] = uint256.NewInt(rnd.Uint64())
		}
		prePopStor[addrs[i]] = slots
	}

	// Dirty subset: a random subset of the accounts + some of their storage.
	dirtyAccts = make(map[types.Address]*account.StateAccount, s.nDirty)
	dirtyStor = make(map[types.Address]map[types.Hash]*uint256.Int, s.storAddrs)
	for i := 0; i < s.nDirty && i < len(addrs); i++ {
		pick := addrs[rnd.Intn(len(addrs))]
		acct := &account.StateAccount{
			Initialised: true,
			Nonce:       rnd.Uint64(),
		}
		acct.Balance.SetUint64(rnd.Uint64())
		dirtyAccts[pick] = acct
	}
	for i := 0; i < s.storAddrs && i < len(addrs); i++ {
		pick := addrs[rnd.Intn(len(addrs))]
		if _, ok := dirtyStor[pick]; !ok {
			dirtyStor[pick] = make(map[types.Hash]*uint256.Int, s.nStorDrty)
		}
		for j := 0; j < s.nStorDrty; j++ {
			var slot types.Hash
			rnd.Read(slot[:])
			dirtyStor[pick][slot] = uint256.NewInt(rnd.Uint64())
		}
	}
	return
}

func benchRunSequential(b *testing.B, s benchScenario) {
	prePopAccts, prePopStor, dirtyAccts, dirtyStor := benchBuildScenario(s)

	db := memdb.New(b.TempDir())
	defer db.Close()
	seedPlainStateBench(b, db, prePopAccts, prePopStor)

	tx, err := db.BeginRo(context.Background())
	if err != nil {
		b.Fatal(err)
	}
	defer tx.Rollback()

	m := NewMPTRootComputer()
	m.SetStateReader(NewPlainStateMPTReader(tx))

	// Warmup: first ComputeRoot touches every pre-pop account so the
	// in-memory branch store is populated. Without this the first
	// benchmark iteration hits a cold-start "empty branch data" error
	// on mixed workloads where dirty addresses share prefixes with
	// addresses only in PlainState.
	if _, err := m.ComputeRoot(prePopAccts, prePopStor); err != nil {
		b.Fatalf("warmup: %v", err)
	}

	b.ResetTimer()
	for i := 0; b.Loop(); i++ {
		_, err := m.ComputeRoot(dirtyAccts, dirtyStor)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func benchRunConcurrent(b *testing.B, s benchScenario) {
	prePopAccts, prePopStor, dirtyAccts, dirtyStor := benchBuildScenario(s)

	db := memdb.New(b.TempDir())
	defer db.Close()
	seedPlainStateBench(b, db, prePopAccts, prePopStor)

	templateTx, err := db.BeginRo(context.Background())
	if err != nil {
		b.Fatal(err)
	}
	defer templateTx.Rollback()

	m := NewConcurrentMPTRootComputer(db, b.TempDir(), log2.New())
	m.SetStateReader(NewPlainStateMPTReader(templateTx))

	// Warmup (see sequential comment).
	if _, err := m.ComputeRoot(prePopAccts, prePopStor); err != nil {
		b.Fatalf("warmup: %v", err)
	}

	b.ResetTimer()
	for i := 0; b.Loop(); i++ {
		_, err := m.ComputeRoot(dirtyAccts, dirtyStor)
		if err != nil {
			b.Fatal(err)
		}
	}
}
