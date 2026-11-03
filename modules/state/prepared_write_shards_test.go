// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/params"
)

// prepShardsBlock builds the same block's state on a fresh IntraBlockState:
// senders, credited recipients, contracts with storage writes and touched
// empty accounts (removed under EIP-161). With pending set the credits of
// accounts the block never loads stay pending into the write, as in an
// import under the no-journal fold.
func prepShardsBlock(tx kv.Tx, rules *params.Rules, pending bool) *IntraBlockState {
	return prepShardsBlockOn(New(NewPlainStateReader(tx)), rules, pending)
}

func prepShardsBlockOn(ibs *IntraBlockState, rules *params.Rules, pending bool) *IntraBlockState {
	ibs.SetDetachedPendingCredits(pending)
	for i := 0; i < 3000; i++ {
		a := benchAddr(1, i)
		ibs.AddBalance(a, uint256.NewInt(uint64(1e9+i)))
		ibs.SetNonce(a, uint64(i%50))
	}
	for i := 0; i < 4000; i++ {
		ibs.AddBalance(benchAddr(2, i), uint256.NewInt(uint64(i+1)))
	}
	for i := 0; i < 64; i++ {
		c := benchAddr(3, i)
		ibs.AddBalance(c, uint256.NewInt(1))
		ibs.SetNonce(c, 1)
		for s := 0; s < 5; s++ {
			ibs.SetState(c, &types.Hash{byte(s), byte(i)}, *uint256.NewInt(uint64(s*i + 1)))
		}
	}
	for i := 0; i < 50; i++ {
		ibs.AddBalance(benchAddr(4, i), uint256.NewInt(0))
	}
	ibs.FinalizeTx(rules, NewNoopWriter())
	return ibs
}

// The sharded prepared write is the serial one: the same calls in the same
// order, the same rows, changesets and counts.
func TestPrepareStateWriteShardsMatchSerial(t *testing.T) {
	for _, tc := range []struct {
		name                string
		pending, deferLoads bool
	}{
		{"live", false, true},
		{"pending", true, true},
		{"pendingLoadedLive", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer SetFoldNoJournalForTest(tc.pending)()
			defer SetPrepDeferLoadsForTest(tc.deferLoads)()
			testPrepareStateWriteShardsMatchSerial(t, tc.pending)
		})
	}
}

func testPrepareStateWriteShardsMatchSerial(t *testing.T, pending bool) {
	testPrepareStateWriteShardsMatchSerialOn(t, pending, false)
}

// newPrefetchState is a state over tx whose reader is a prefetch layer
// seeded with every third credited recipient (stored or absent) and backed
// by a known-account cache for every fifth: the rest miss and read the
// store.
func newPrefetchState(t *testing.T, tx kv.Tx) *IntraBlockState {
	base := NewPlainStateReader(tx)
	pf := NewAccountPrefetch(base)
	seed := make(map[types.Address]*account.StateAccount)
	known := mapKnownAccounts{}
	for i := 0; i < 4000; i++ {
		a := benchAddr(2, i)
		if i%3 != 0 && i%5 != 0 {
			continue
		}
		acc, err := base.ReadAccountData(a)
		if err != nil {
			t.Fatal(err)
		}
		if i%3 == 0 {
			seed[a] = acc
		} else {
			known[a] = acc
		}
	}
	pf.Seed(seed)
	pf.SetKnown(known)
	ibs := New(pf)
	ibs.SetAccountPrefetch(pf)
	return ibs
}

type mapKnownAccounts map[types.Address]*account.StateAccount

func (m mapKnownAccounts) Peek(a types.Address) (*account.StateAccount, bool) {
	v, ok := m[a]
	return v, ok
}

func (m mapKnownAccounts) Get(a types.Address) (*account.StateAccount, bool) {
	v, ok := m[a]
	if !ok || v == nil {
		return nil, ok
	}
	c := new(account.StateAccount)
	c.Copy(v)
	return c, true
}

// The parallel pending loads give the serial loads' prepared write, with
// prefetch hits, known-cache hits and store misses mixed.
func TestPrepareStateWriteParLoadMatchSerial(t *testing.T) {
	defer SetFoldNoJournalForTest(true)()
	defer SetPrepDeferLoadsForTest(true)()
	for _, par := range []int{0, 3, 8} {
		t.Run(fmt.Sprintf("par=%d", par), func(t *testing.T) {
			defer SetPrepLoadParForTest(par)()
			testPrepareStateWriteShardsMatchSerialOn(t, true, true)
		})
	}
}

func testPrepareStateWriteShardsMatchSerialOn(t *testing.T, pending, withPrefetch bool) {
	defer SetHistoryIndexDeferredForTest(true)()
	db := memdb.NewTestDB(t)
	rules := params.MainnetChainConfig.Rules(1)
	// Every other recipient has a stored account: a pending credit is
	// folded onto it or creates the account.
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		w := NewPlainStateWriterNoHistory(tx)
		for i := 0; i < 4000; i += 2 {
			a := account.StateAccount{Nonce: uint64(i % 7), Initialised: true}
			a.Balance.SetUint64(uint64(5 + i))
			a.CodeHash = emptyCodeHashH
			if err := w.UpdateAccountData(benchAddr(2, i), &a, &a); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	prepare := func(shards int) *PreparedStateWrite {
		defer SetPrepShardsForTest(shards)()
		var p *PreparedStateWrite
		if err := db.View(context.Background(), func(tx kv.Tx) error {
			var err error
			ibs := prepShardsBlock(tx, rules, pending)
			if withPrefetch {
				ibs = prepShardsBlockOn(newPrefetchState(t, tx), rules, pending)
			}
			p, err = PrepareStateWrite(ibs, rules, 1000)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return p
	}
	serial := prepare(1)
	if !serial.fast || len(serial.rows.rows) == 0 || len(serial.acct) == 0 || len(serial.stor) == 0 {
		t.Fatalf("serial write not a fast one with rows: fast %v rows %d acct %d stor %d",
			serial.fast, len(serial.rows.rows), len(serial.acct), len(serial.stor))
	}
	for _, shards := range []int{2, 8, 13, 256} {
		got := prepare(shards)
		if ph := got.Phases(); ph.Commit == 0 || ph.Encode == 0 || pending != (ph.Loaded > 4000) {
			t.Fatalf("shards %d: phases not recorded: %+v", shards, ph)
		}
		got.phases = PrepPhases{}
		if !reflect.DeepEqual(serial, got) {
			t.Fatalf("shards %d: prepared write differs (fast %v/%v rows %d/%d acct %d/%d stor %d/%d counts %d,%d/%d,%d)",
				shards, serial.fast, got.fast, len(serial.rows.rows), len(got.rows.rows), len(serial.acct), len(got.acct),
				len(serial.stor), len(got.stor), serial.nAccts, serial.nSlots, got.nAccts, got.nSlots)
		}
	}
}

// The recorded log in shard order is CommitBlock's log.
func TestCommitBlockShardsLog(t *testing.T) {
	for _, pending := range []bool{false, true} {
		t.Run(fmt.Sprintf("pending=%v", pending), func(t *testing.T) {
			defer SetFoldNoJournalForTest(pending)()
			testCommitBlockShardsLog(t, pending)
		})
	}
}

func testCommitBlockShardsLog(t *testing.T, pending bool) {
	db := memdb.NewTestDB(t)
	rules := params.MainnetChainConfig.Rules(1)
	var serial, sharded *StateWriteLog
	_ = db.View(context.Background(), func(tx kv.Tx) error {
		serial = &StateWriteLog{}
		if err := prepShardsBlock(tx, rules, pending).CommitBlock(rules, serial); err != nil {
			t.Fatal(err)
		}
		logs, err := prepShardsBlock(tx, rules, pending).commitBlockShards(rules, 8, &PrepPhases{})
		if err != nil {
			t.Fatal(err)
		}
		sharded = &StateWriteLog{}
		for _, l := range logs {
			sharded.appendLog(l)
		}
		return nil
	})
	deletes := 0
	for _, op := range serial.ops {
		if op.kind == opDeleteAccount {
			deletes++
		}
	}
	if len(serial.ops) == 0 || len(serial.slots) == 0 || deletes == 0 {
		t.Fatalf("serial log: %d ops, %d slots, %d deletes", len(serial.ops), len(serial.slots), deletes)
	}
	if !reflect.DeepEqual(serial.ops, sharded.ops) || !reflect.DeepEqual(serial.accts, sharded.accts) ||
		!reflect.DeepEqual(serial.slots, sharded.slots) || !reflect.DeepEqual(serial.codes, sharded.codes) ||
		!reflect.DeepEqual(serial.creates, sharded.creates) {
		t.Fatalf("sharded log differs: ops %d/%d accts %d/%d slots %d/%d", len(serial.ops), len(sharded.ops),
			len(serial.accts), len(sharded.accts), len(serial.slots), len(sharded.slots))
	}
}
