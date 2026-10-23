// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/parallel"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

// hashingRootComputer hashes the dirty set the root walk hands it, sorted:
// two folds that leave different post-states get different roots.
type hashingRootComputer struct{ rows []string }

func (c *hashingRootComputer) ComputeRoot(accts map[types.Address]*account.StateAccount, sto map[types.Address]map[types.Hash]*uint256.Int) (types.Hash, error) {
	c.rows = c.rows[:0]
	for a, acc := range accts {
		if acc == nil {
			c.rows = append(c.rows, fmt.Sprintf("a %x del", a))
			continue
		}
		c.rows = append(c.rows, fmt.Sprintf("a %x %d/%s %x %x", a, acc.Nonce, acc.Balance.String(), acc.CodeHash, acc.Root))
	}
	for a, m := range sto {
		for k, v := range m {
			c.rows = append(c.rows, fmt.Sprintf("s %x %x %s", a, k, v.String()))
		}
	}
	sort.Strings(c.rows)
	var buf []byte
	for _, r := range c.rows {
		buf = append(buf, r...)
		buf = append(buf, '\n')
	}
	return crypto.Keccak256Hash(buf), nil
}

// shardFoldBlock is one random block's final MVS over a base state.
type shardFoldBlock struct {
	mvs    *parallel.MVS
	numTxs int
	store  bulkMapReader
	known  bulkKnownCache
	// live: touched before the fold (a block-start system call); credited:
	// credited before the fold.
	live, credited types.Address
}

// newShardFoldBlock builds a random block of transfers (new and existing
// recipients, self-transfers, senders drained to empty accounts), contract
// calls writing storage, contract creations with code, self-destructs,
// recreations over a wipe and reverted transactions (only the sender's
// fee and nonce written). sparse blocks make the entries that need the
// per-entry calls (base reads, deletions, wipes) rare, so the fold's split
// pass runs on long runs; the others break the runs short.
func newShardFoldBlock(seed int64, numTxs int, sparse bool) *shardFoldBlock {
	rng := rand.New(rand.NewSource(seed))
	b := &shardFoldBlock{mvs: parallel.NewMVS(), numTxs: numTxs, store: bulkMapReader{}, known: bulkKnownCache{}}
	next := 0
	fresh := func() types.Address {
		next++
		return bulkTestAddr(int(seed)*10_000_000 + next)
	}
	existing := func(bal uint64, code bool) types.Address {
		a := fresh()
		acc := account.NewAccount()
		acc.Balance.SetUint64(bal)
		acc.Nonce = uint64(rng.Intn(7))
		if code {
			acc.CodeHash = crypto.Keccak256Hash([]byte{byte(next), 0x60})
		}
		b.store[a] = &acc
		if sparse && rng.Intn(20_000) != 0 || !sparse && rng.Intn(50) != 0 { // a few are left to the base reader
			b.known[a] = &acc
		}
		return a
	}
	full := func(a types.Address, tx int, bal uint64, nonce uint64, codeHash types.Hash) {
		acc := account.NewAccount()
		acc.Balance.SetUint64(bal)
		acc.Nonce = nonce
		acc.CodeHash = codeHash
		b.mvs.Write(parallel.LocationKey{Address: a, Field: parallel.FieldBalance}, tx, 0, acc.MarshalV2())
	}
	credit := func(a types.Address, tx int, v uint64) {
		b.mvs.WriteDelta(parallel.LocationKey{Address: a, Field: parallel.FieldBalance}, tx, 0, uint256.NewInt(v))
	}
	emptyHash := crypto.Keccak256Hash(nil)
	contracts := make([]types.Address, 8)
	for i := range contracts {
		contracts[i] = existing(1, true)
	}
	for tx := 0; tx < numTxs; tx++ {
		from := existing(1_000_000, false)
		r := rng.Intn(100)
		if sparse && r >= 68 && rng.Intn(400) != 0 {
			r = rng.Intn(68)
		}
		switch {
		case r < 60: // plain transfer
			to := fresh()
			if rng.Intn(3) != 0 {
				to = existing(uint64(rng.Intn(1000)), false)
			} else {
				b.known[to] = nil
			}
			full(from, tx, uint64(rng.Intn(1_000_000)), 1, emptyHash)
			credit(to, tx, uint64(rng.Intn(3))) // zero credits included
		case r < 65: // self-transfer: fee and nonce only
			full(from, tx, 999_000, 1, emptyHash)
		case r < 68: // the sender drained to an empty account
			full(from, tx, 0, 0, emptyHash)
		case r < 78: // a contract call writing storage
			c := contracts[rng.Intn(len(contracts))]
			full(from, tx, 900_000, 1, emptyHash)
			var slot types.Hash
			slot[0], slot[31] = byte(tx), byte(tx>>8)
			val := []byte{byte(rng.Intn(256)), 1}
			if rng.Intn(5) == 0 {
				val = nil // cleared
			}
			b.mvs.Write(parallel.LocationKey{Address: c, Field: parallel.FieldStorage, Slot: slot}, tx, 0, val)
			credit(c, tx, uint64(rng.Intn(2)))
		case r < 84: // a contract creation with code and storage
			c := fresh()
			b.known[c] = nil
			code := []byte{0x60, byte(tx), 0x60, byte(tx >> 8)}
			full(from, tx, 800_000, 1, emptyHash)
			full(c, tx, uint64(rng.Intn(10)), 1, crypto.Keccak256Hash(code))
			b.mvs.Write(parallel.LocationKey{Address: c, Field: parallel.FieldCode}, tx, 0, code)
			b.mvs.Write(parallel.LocationKey{Address: c, Field: parallel.FieldStorage, Slot: types.Hash{1}}, tx, 0, []byte{7})
		case r < 87: // a self-destruct (its storage discarded)
			c := existing(5, true)
			full(from, tx, 800_000, 1, emptyHash)
			b.mvs.Write(parallel.LocationKey{Address: c, Field: parallel.FieldBalance}, tx, 0, nil)
			b.mvs.Write(parallel.LocationKey{Address: c, Field: parallel.FieldStorage, Slot: types.Hash{2}}, tx, 0, []byte{9})
		case r < 89: // recreated over a wipe
			c := existing(5, true)
			code := []byte{0x61, byte(tx)}
			full(from, tx, 800_000, 1, emptyHash)
			b.mvs.Write(parallel.LocationKey{Address: c, Field: parallel.FieldStorageWipe}, tx, 0, []byte{1})
			full(c, tx, 5, 1, crypto.Keccak256Hash(code))
			b.mvs.Write(parallel.LocationKey{Address: c, Field: parallel.FieldCode}, tx, 0, code)
			b.mvs.Write(parallel.LocationKey{Address: c, Field: parallel.FieldStorage, Slot: types.Hash{3}}, tx, 0, []byte{4})
		default: // reverted: only the sender's fee and nonce
			full(from, tx, 990_000, 1, emptyHash)
		}
	}
	b.live = contracts[0]
	b.credited = contracts[1]
	return b
}

// shardFoldResult is what a fold leaves for the import: the dirty set after
// the block's finalize, the root over it (the root's dirty-set walk with
// detached credits, as on the import path) and the committed rows.
type shardFoldResult struct {
	dirty, rootRows, rows []string
	root                  types.Hash
}

func (b *shardFoldBlock) fold(t *testing.T, shard int) shardFoldResult {
	t.Helper()
	prev := collectShardWorkers
	collectShardWorkers = shard
	defer func() { collectShardWorkers = prev }()
	mk := func() *state.IntraBlockState {
		pf := state.NewAccountPrefetch(b.store)
		pf.SetKnown(b.known)
		ibs := state.New(pf)
		ibs.SetAccountPrefetch(pf)
		ibs.SetDetachedPendingCredits(true)
		ibs.GetBalance(b.live)
		ibs.AddBalance(b.credited, uint256.NewInt(5))
		if err := applyMVSToIBS(b.mvs, b.numTxs, ibs); err != nil {
			t.Fatal(err)
		}
		return ibs
	}
	rules := &params.Rules{IsSpuriousDragon: true}
	var res shardFoldResult

	ibs := mk()
	// The block-end system call's finalize (no-op writer), then the root.
	if err := ibs.FinalizeTx(rules, state.NewNoopWriter()); err != nil {
		t.Fatal(err)
	}
	for _, a := range ibs.DirtyAddresses() {
		res.dirty = append(res.dirty, a.Hex())
	}
	sort.Strings(res.dirty)
	rc := &hashingRootComputer{}
	ibs.SetRootComputer(rc)
	res.root = ibs.IntermediateRoot()
	res.rootRows = append([]string(nil), rc.rows...)

	ibs2 := mk()
	if err := ibs2.FinalizeTx(rules, state.NewNoopWriter()); err != nil {
		t.Fatal(err)
	}
	w := &recWriter{}
	if err := ibs2.CommitBlock(rules, w); err != nil {
		t.Fatal(err)
	}
	sort.Strings(w.log)
	res.rows = w.log
	return res
}

// The fold with N42_COLLECT_SHARD at 8 and 16 leaves the same dirty set,
// root and committed rows as the serial fold, over many random blocks.
func TestApplyMVSShardedMatchesSerial(t *testing.T) {
	blocks := 12
	if testing.Short() {
		blocks = 4
	}
	for i := 0; i < blocks; i++ {
		numTxs := 9000 + 3000*(i%4)
		b := newShardFoldBlock(int64(i+1), numTxs, i%2 == 1)
		want := b.fold(t, 0)
		if len(want.rows) < numTxs {
			t.Fatalf("block %d: only %d rows committed", i, len(want.rows))
		}
		for _, shard := range []int{8, 16} {
			got := b.fold(t, shard)
			if want.root != got.root {
				t.Fatalf("block %d shard %d: root %x, serial %x", i, shard, got.root, want.root)
			}
			if !reflect.DeepEqual(want.dirty, got.dirty) {
				t.Fatalf("block %d shard %d: dirty sets differ (%d vs %d)", i, shard, len(got.dirty), len(want.dirty))
			}
			if !reflect.DeepEqual(want.rootRows, got.rootRows) {
				t.Fatalf("block %d shard %d: root dirty sets differ", i, shard)
			}
			if !reflect.DeepEqual(want.rows, got.rows) {
				t.Fatalf("block %d shard %d: committed rows differ (%d vs %d)", i, shard, len(got.rows), len(want.rows))
			}
		}
	}
}

func TestParseCollectShard(t *testing.T) {
	for v, want := range map[string]int{"": 0, "0": 0, "x": 0, "-3": 0, "1": 1, "16": 16, "1000": 64} {
		if got := parseCollectShard(v); got != want {
			t.Fatalf("parseCollectShard(%q) = %d, want %d", v, got, want)
		}
	}
}
