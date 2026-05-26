package commitment

import (
	"context"
	"encoding/binary"
	"fmt"
	"math/bits"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
)

// stripToRethShape rewrites a TrieStorage into reth's on-disk shape: delete the
// empty-path (keylen-32) account.root records AND drop the "+1" own-hash
// (rootHash) that N42's bootstrap writes on keylen-33+ records but reth never
// does (verified: real reth-migrated keylen-33+ records have 0 +1 hashes).
// Without this the no-root path is mis-simulated (SeekToAccount mistakes a
// keylen-33 record's spurious +1 hash for the storage root).
func stripToRethShape(t *testing.T, tx kv.RwTx) {
	t.Helper()
	// TrieStorage: drop keylen-32 account.root records + the +1 own-hash.
	dropPlus1(t, tx, "TrieStorage", true)
	// TrieAccount: reth omits the keylen-0 state-root record (N42 already does
	// too) and the +1 own-hash on account nodes; reshape to match.
	dropPlus1(t, tx, "TrieAccount", false)
}

func dropPlus1(t *testing.T, tx kv.RwTx, table string, delEmptyPathStorageRoot bool) {
	t.Helper()
	c, err := tx.Cursor(table)
	if err != nil {
		t.Fatal(err)
	}
	type kvp struct{ k, v []byte }
	var del [][]byte
	var rewrite []kvp
	for k, v, e := c.First(); k != nil; k, v, e = c.Next() {
		if e != nil {
			t.Fatal(e)
		}
		if delEmptyPathStorageRoot && len(k) == 32 {
			del = append(del, append([]byte(nil), k...))
			continue
		}
		if len(v) >= 6 {
			hasTree := binary.BigEndian.Uint16(v[2:4])
			hasHash := binary.BigEndian.Uint16(v[4:6])
			// reth omits keylen-33 (1-nibble storage path) leaf-only nodes:
			// hasTree==0 && hasHash==0 (state-only, no cached hash). Proven by
			// the full-tree diff (12.9M onlyN42, all keylenPath=1). Delete to
			// reproduce reth's on-disk shape.
			if delEmptyPathStorageRoot && len(k) == 33 && hasTree == 0 && hasHash == 0 {
				del = append(del, append([]byte(nil), k...))
				continue
			}
			nh := (len(v) - 6) / 32
			if bits.OnesCount16(hasHash)+1 == nh {
				nv := append(append([]byte(nil), v[:6]...), v[38:]...)
				rewrite = append(rewrite, kvp{append([]byte(nil), k...), nv})
			}
		}
	}
	c.Close()
	for _, k := range del {
		if err := tx.Delete(table, k); err != nil {
			t.Fatal(err)
		}
	}
	for _, e := range rewrite {
		if err := tx.Delete(table, e.k); err != nil {
			t.Fatal(err)
		}
		if err := tx.Put(table, e.k, e.v); err != nil {
			t.Fatal(err)
		}
	}
}

func buildNStorageAcctsFrom(start, n, slots int) (map[types.Address]*account.StateAccount, map[types.Address]map[types.Hash]*uint256.Int, []types.Address) {
	accts := map[types.Address]*account.StateAccount{}
	stor := map[types.Address]map[types.Hash]*uint256.Int{}
	addrs := []types.Address{}
	for i := start; i < start+n; i++ {
		var a types.Address
		a[0] = byte(i)
		a[19] = byte(i + 1)
		acct := &account.StateAccount{Initialised: true, Nonce: uint64(i + 1)}
		acct.Balance.SetUint64(uint64(i)*1000 + 1)
		acct.CodeHash = types.BytesHash([]byte{byte(i), 'c'})
		accts[a] = acct
		addrs = append(addrs, a)
		m := map[types.Hash]*uint256.Int{}
		for j := 0; j < slots; j++ {
			var s types.Hash
			s[0] = byte(j)
			s[31] = byte(j*13 + i)
			m[s] = uint256.NewInt(uint64(i*1000 + j + 100))
		}
		stor[a] = m
	}
	return accts, stor, addrs
}

// runNoRoot bootstraps, strips keylen-32 records, balance-changes addrs[0],
// and returns (incremental root, full-rebuild reference root).
func runNoRoot(t *testing.T, start, n, slots int) (types.Hash, types.Hash) {
	accts, stor, addrs := buildNStorageAcctsFrom(start, n, slots)
	db := memdb.NewTestDB(t)
	tx, _ := db.BeginRw(context.Background())
	defer tx.Rollback()
	trc := NewTrieRootComputer()
	trc.SetRwTx(tx)
	trc.SetIncremental(false)
	if _, err := trc.ComputeRoot(accts, stor); err != nil {
		t.Fatal(err)
	}
	stripToRethShape(t, tx)
	na := *accts[addrs[0]]
	na.Balance.SetUint64(7777777)
	dA := map[types.Address]*account.StateAccount{addrs[0]: &na}
	trc.SetIncremental(true)
	rInc, err := trc.ComputeRoot(dA, nil)
	if err != nil {
		t.Fatal(err)
	}
	refA, refS := applyDelta(accts, stor, dA, nil)
	rRef := fullRoot(t, refA, refS)
	return rInc, rRef
}

// buildDeep makes n accounts each with `slots` storage slots, with slot keys
// spread across the high bytes so that after keccak the storage trie is DEEP
// (3+ levels) — only then does reth-shape TrieStorage retain cached keylen-33+
// sub-branches (leaf-only nodes are omitted). Small tries (≤~40 slots) collapse
// to a near-empty TrieStorage and never exercise the cached-storage path.
func buildDeep(n, slots int) (map[types.Address]*account.StateAccount, map[types.Address]map[types.Hash]*uint256.Int, []types.Address) {
	accts := map[types.Address]*account.StateAccount{}
	stor := map[types.Address]map[types.Hash]*uint256.Int{}
	addrs := []types.Address{}
	for i := 0; i < n; i++ {
		var a types.Address
		a[0] = byte(i)
		a[19] = byte(i + 1)
		acct := &account.StateAccount{Initialised: true, Nonce: uint64(i + 1)}
		acct.Balance.SetUint64(uint64(i)*1000 + 1)
		acct.CodeHash = types.BytesHash([]byte{byte(i), 'c'})
		accts[a] = acct
		addrs = append(addrs, a)
		m := map[types.Hash]*uint256.Int{}
		for j := 0; j < slots; j++ {
			var s types.Hash
			binary.BigEndian.PutUint32(s[0:4], uint32(j))
			s[31] = byte(i)
			m[s] = uint256.NewInt(uint64(j) + 1)
		}
		stor[a] = m
	}
	return accts, stor, addrs
}

// runDeepMultiStor: deep storage tries (cached sub-branches present in
// reth-shape), then change slot j=0 in every `dirtyEvery`-th account, running
// ONE incremental loader (shared cursor + receiver across all dirty accounts) —
// the exact whole-tree combine a real block does. Returns (incremental, full).
func runDeepMultiStor(t *testing.T, n, slots, dirtyEvery int) (types.Hash, types.Hash) {
	accts, stor, addrs := buildDeep(n, slots)
	db := memdb.NewTestDB(t)
	tx, _ := db.BeginRw(context.Background())
	defer tx.Rollback()
	trc := NewTrieRootComputer()
	trc.SetRwTx(tx)
	trc.SetIncremental(false)
	if _, err := trc.ComputeRoot(accts, stor); err != nil {
		t.Fatal(err)
	}
	stripToRethShape(t, tx)
	dA := map[types.Address]*account.StateAccount{}
	dS := map[types.Address]map[types.Hash]*uint256.Int{}
	for i := 0; i < n; i += dirtyEvery {
		a := addrs[i]
		var s types.Hash
		binary.BigEndian.PutUint32(s[0:4], 0) // slot j=0
		s[31] = byte(i)
		na := *accts[a]
		dA[a] = &na
		dS[a] = map[types.Hash]*uint256.Int{s: uint256.NewInt(uint64(900000 + i))}
	}
	trc.SetIncremental(true)
	rInc, err := trc.ComputeRoot(dA, dS)
	if err != nil {
		t.Fatal(err)
	}
	refA, refS := applyDelta(accts, stor, dA, dS)
	rRef := fullRoot(t, refA, refS)
	return rInc, rRef
}

// TestDeepMultiStor reproduces the whole-tree combine on reth-shape data:
// deep storage tries + multiple dirty storage accounts in one loader.
func TestDeepMultiStor(t *testing.T) {
	type cfg struct{ n, slots, every int }
	for _, cc := range []cfg{
		{2, 1024, 1}, {4, 1024, 1}, {4, 4096, 1}, {8, 2048, 2},
		{4, 4096, 2}, {16, 1024, 3}, {8, 8192, 1},
	} {
		cc := cc
		t.Run(fmt.Sprintf("n=%d_slots=%d_every=%d", cc.n, cc.slots, cc.every), func(t *testing.T) {
			rInc, rRef := runDeepMultiStor(t, cc.n, cc.slots, cc.every)
			if rInc != rRef {
				t.Errorf("MISMATCH inc=%x ref=%x", rInc[:], rRef[:])
			}
		})
	}
}

// runNoRootMut applies an arbitrary account/storage delta to a reth-shape tree
// and returns (incremental root, full-rebuild reference). mk builds the delta.
func runNoRootMut(t *testing.T, start, n, slots int,
	mk func(accts map[types.Address]*account.StateAccount, addrs []types.Address) (map[types.Address]*account.StateAccount, map[types.Address]map[types.Hash]*uint256.Int),
) (types.Hash, types.Hash) {
	accts, stor, addrs := buildDeep(n, slots)
	_ = start
	db := memdb.NewTestDB(t)
	tx, _ := db.BeginRw(context.Background())
	defer tx.Rollback()
	trc := NewTrieRootComputer()
	trc.SetRwTx(tx)
	trc.SetIncremental(false)
	if _, err := trc.ComputeRoot(accts, stor); err != nil {
		t.Fatal(err)
	}
	stripToRethShape(t, tx)
	dA, dS := mk(accts, addrs)
	trc.SetIncremental(true)
	rInc, err := trc.ComputeRoot(dA, dS)
	if err != nil {
		t.Fatal(err)
	}
	refA, refS := applyDelta(accts, stor, dA, dS)
	rRef := fullRoot(t, refA, refS)
	return rInc, rRef
}

// TestNoRootAccountMut covers the account-trie structural changes a real block
// does but the storage-only repro missed: DELETE (SELFDESTRUCT → branch
// collapse), INSERT (new account → branch split), and value-only changes — all
// on reth-shape, with multiple dirty accounts in one incremental loader.
func TestNoRootAccountMut(t *testing.T) {
	mkDelete := func(idx ...int) func(map[types.Address]*account.StateAccount, []types.Address) (map[types.Address]*account.StateAccount, map[types.Address]map[types.Hash]*uint256.Int) {
		return func(accts map[types.Address]*account.StateAccount, addrs []types.Address) (map[types.Address]*account.StateAccount, map[types.Address]map[types.Hash]*uint256.Int) {
			dA := map[types.Address]*account.StateAccount{}
			for _, i := range idx {
				dA[addrs[i]] = nil // delete
			}
			return dA, nil
		}
	}
	mkValue := func(idx ...int) func(map[types.Address]*account.StateAccount, []types.Address) (map[types.Address]*account.StateAccount, map[types.Address]map[types.Hash]*uint256.Int) {
		return func(accts map[types.Address]*account.StateAccount, addrs []types.Address) (map[types.Address]*account.StateAccount, map[types.Address]map[types.Hash]*uint256.Int) {
			dA := map[types.Address]*account.StateAccount{}
			for _, i := range idx {
				na := *accts[addrs[i]]
				na.Balance.SetUint64(uint64(7777000 + i))
				na.Nonce += 5
				dA[addrs[i]] = &na
			}
			return dA, nil
		}
	}
	mkInsert := func(cnt int) func(map[types.Address]*account.StateAccount, []types.Address) (map[types.Address]*account.StateAccount, map[types.Address]map[types.Hash]*uint256.Int) {
		return func(accts map[types.Address]*account.StateAccount, addrs []types.Address) (map[types.Address]*account.StateAccount, map[types.Address]map[types.Hash]*uint256.Int) {
			dA := map[types.Address]*account.StateAccount{}
			for i := 0; i < cnt; i++ {
				var a types.Address
				a[0] = 0xaa
				a[10] = byte(i)
				a[19] = byte(i*7 + 1)
				nacct := &account.StateAccount{Initialised: true, Nonce: uint64(i + 1)}
				nacct.Balance.SetUint64(uint64(500000 + i))
				dA[a] = nacct
			}
			return dA, nil
		}
	}
	cases := []struct {
		name        string
		n, slots    int
		mk          func(map[types.Address]*account.StateAccount, []types.Address) (map[types.Address]*account.StateAccount, map[types.Address]map[types.Hash]*uint256.Int)
	}{
		{"delete-1of8", 8, 1024, mkDelete(3)},
		{"delete-3of16", 16, 512, mkDelete(2, 7, 11)},
		{"delete-2of4-deep", 4, 4096, mkDelete(1, 2)},
		{"value-3of16", 16, 256, mkValue(0, 5, 9)},
		{"insert-4", 8, 512, mkInsert(4)},
		{"mixed-del-ins", 8, 1024, func(accts map[types.Address]*account.StateAccount, addrs []types.Address) (map[types.Address]*account.StateAccount, map[types.Address]map[types.Hash]*uint256.Int) {
			dA, _ := mkDelete(2, 5)(accts, addrs)
			ins, _ := mkInsert(3)(accts, addrs)
			for k, v := range ins {
				dA[k] = v
			}
			return dA, nil
		}},
	}
	for _, cc := range cases {
		cc := cc
		t.Run(cc.name, func(t *testing.T) {
			rInc, rRef := runNoRootMut(t, 0, cc.n, cc.slots, cc.mk)
			if rInc != rRef {
				t.Errorf("MISMATCH inc=%x ref=%x", rInc[:], rRef[:])
			}
		})
	}
}

// buildManyEOA makes n storage-less accounts with UNIQUE addresses (uint32 i
// encoded across a[0:4]) so the account trie is genuinely DEEP (5+ levels for
// n≥100k) after keccak — reproducing the real chain's account-trie depth that
// the ≤16-account synthetic can't. A few accounts get storage so branch
// collapse/split touches storage-bearing leaves too.
func buildManyEOA(n, withStorageEvery, slots int) (map[types.Address]*account.StateAccount, map[types.Address]map[types.Hash]*uint256.Int, []types.Address) {
	accts := map[types.Address]*account.StateAccount{}
	stor := map[types.Address]map[types.Hash]*uint256.Int{}
	addrs := make([]types.Address, 0, n)
	for i := 0; i < n; i++ {
		var a types.Address
		binary.BigEndian.PutUint32(a[0:4], uint32(i))
		acct := &account.StateAccount{Initialised: true, Nonce: uint64(i + 1)}
		acct.Balance.SetUint64(uint64(i)*7 + 3)
		accts[a] = acct
		addrs = append(addrs, a)
		if withStorageEvery > 0 && i%withStorageEvery == 0 && slots > 0 {
			m := map[types.Hash]*uint256.Int{}
			for j := 0; j < slots; j++ {
				var s types.Hash
				binary.BigEndian.PutUint32(s[0:4], uint32(j))
				s[31] = byte(i)
				m[s] = uint256.NewInt(uint64(j) + 1)
			}
			stor[a] = m
		}
	}
	return accts, stor, addrs
}

// TestDeepAccountTrie reproduces account INSERT/DELETE/value changes on a DEEP
// (real-chain-like) account trie in reth-shape — the structural restructuring
// (branch split/collapse) the storage-only real-data repro never exercised.
func TestDeepAccountTrie(t *testing.T) {
	run := func(t *testing.T, n, every, slots int, mk func(accts map[types.Address]*account.StateAccount, addrs []types.Address) (map[types.Address]*account.StateAccount, map[types.Address]map[types.Hash]*uint256.Int)) {
		accts, stor, addrs := buildManyEOA(n, every, slots)
		db := memdb.NewTestDB(t)
		tx, _ := db.BeginRw(context.Background())
		defer tx.Rollback()
		trc := NewTrieRootComputer()
		trc.SetRwTx(tx)
		trc.SetIncremental(false)
		if _, err := trc.ComputeRoot(accts, stor); err != nil {
			t.Fatal(err)
		}
		stripToRethShape(t, tx)
		dA, dS := mk(accts, addrs)
		trc.SetIncremental(true)
		rInc, err := trc.ComputeRoot(dA, dS)
		if err != nil {
			t.Fatal(err)
		}
		refA, refS := applyDelta(accts, stor, dA, dS)
		rRef := fullRoot(t, refA, refS)
		if rInc != rRef {
			t.Errorf("MISMATCH inc=%x ref=%x", rInc[:], rRef[:])
		}
	}
	t.Run("delete-scattered-100k", func(t *testing.T) {
		run(t, 100000, 0, 0, func(accts map[types.Address]*account.StateAccount, addrs []types.Address) (map[types.Address]*account.StateAccount, map[types.Address]map[types.Hash]*uint256.Int) {
			dA := map[types.Address]*account.StateAccount{}
			for _, i := range []int{1, 7, 99, 12345, 54321, 99999, 256, 4096, 65535} {
				dA[addrs[i]] = nil
			}
			return dA, nil
		})
	})
	t.Run("insert-scattered-100k", func(t *testing.T) {
		run(t, 100000, 0, 0, func(accts map[types.Address]*account.StateAccount, addrs []types.Address) (map[types.Address]*account.StateAccount, map[types.Address]map[types.Hash]*uint256.Int) {
			dA := map[types.Address]*account.StateAccount{}
			for i := 0; i < 12; i++ {
				var a types.Address
				binary.BigEndian.PutUint32(a[0:4], uint32(2000000+i*111))
				na := &account.StateAccount{Initialised: true, Nonce: uint64(i + 1)}
				na.Balance.SetUint64(uint64(900000 + i))
				dA[a] = na
			}
			return dA, nil
		})
	})
	t.Run("mixed-del-ins-val-storage-50k", func(t *testing.T) {
		run(t, 50000, 500, 200, func(accts map[types.Address]*account.StateAccount, addrs []types.Address) (map[types.Address]*account.StateAccount, map[types.Address]map[types.Hash]*uint256.Int) {
			dA := map[types.Address]*account.StateAccount{}
			dS := map[types.Address]map[types.Hash]*uint256.Int{}
			for _, i := range []int{3, 500, 1000, 25000, 49999} { // some have storage (i%500==0)
				dA[addrs[i]] = nil
			}
			for _, i := range []int{7, 8, 9, 1500} {
				na := *accts[addrs[i]]
				na.Balance.SetUint64(uint64(123000 + i))
				dA[addrs[i]] = &na
			}
			for i := 0; i < 5; i++ {
				var a types.Address
				binary.BigEndian.PutUint32(a[0:4], uint32(3000000+i))
				na := &account.StateAccount{Initialised: true, Nonce: 1}
				na.Balance.SetUint64(uint64(i + 1))
				dA[a] = na
			}
			return dA, dS
		})
	})
}

// TestNoRootSweep validates the no-root (reth-shape) incremental path across
// account counts and storage depths.
func TestNoRootSweep(t *testing.T) {
	type cfg struct{ start, n, slots int }
	for _, cc := range []cfg{
		{0, 1, 24}, {1, 1, 24}, {0, 2, 24}, {0, 3, 24}, {0, 5, 24}, {0, 8, 24},
		{0, 2, 40}, {0, 5, 40}, {0, 8, 8}, {0, 16, 12},
	} {
		cc := cc
		t.Run(fmt.Sprintf("start=%d_n=%d_slots=%d", cc.start, cc.n, cc.slots), func(t *testing.T) {
			rInc, rRef := runNoRoot(t, cc.start, cc.n, cc.slots)
			if rInc != rRef {
				t.Errorf("MISMATCH inc=%x ref=%x", rInc[:], rRef[:])
			}
		})
	}
}

