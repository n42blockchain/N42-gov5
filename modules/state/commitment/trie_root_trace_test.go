package commitment

import (
	"context"
	"encoding/binary"
	"fmt"
	"math/bits"
	"testing"

	"github.com/n42blockchain/N42/modules"

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

// TestSequentialRethMix reproduces the eth-el block-157 scenario: after a first
// incremental block writes erigon-native nodes (keylen-32 root + "+1") for the
// accounts it touched, a SECOND incremental block runs on the resulting MIXED
// tree (reth-shape untouched + native touched). Block A mirrors 156; block B
// mirrors 157 and must still match a full rebuild.
func TestSequentialRethMix(t *testing.T) {
	n, slots := 8, 1024
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
	stripToRethShape(t, tx) // migrated reth-shape state

	mkChange := func(src map[types.Address]*account.StateAccount, idxs []int, base int) (map[types.Address]*account.StateAccount, map[types.Address]map[types.Hash]*uint256.Int) {
		dA := map[types.Address]*account.StateAccount{}
		dS := map[types.Address]map[types.Hash]*uint256.Int{}
		for _, i := range idxs {
			a := addrs[i]
			na := *src[a]
			na.Nonce++
			dA[a] = &na
			var s types.Hash
			binary.BigEndian.PutUint32(s[0:4], 0) // slot j=0
			s[31] = byte(i)
			dS[a] = map[types.Hash]*uint256.Int{s: uint256.NewInt(uint64(base + i))}
		}
		return dA, dS
	}

	trc.SetIncremental(true)

	// Block A (= 156): touch accounts 0,2,4 → their nodes become native.
	dA1, dS1 := mkChange(accts, []int{0, 2, 4}, 900000)
	rootA, err := trc.ComputeRoot(dA1, dS1)
	if err != nil {
		t.Fatal(err)
	}
	refA1, refS1 := applyDelta(accts, stor, dA1, dS1)
	if want := fullRoot(t, refA1, refS1); rootA != want {
		t.Fatalf("block A (156-like) MISMATCH inc=%x want=%x", rootA[:8], want[:8])
	}

	// Block B (= 157): runs on the MIXED tree. Overlap 2,4 (now native) + 6 (still reth-shape).
	dA2, dS2 := mkChange(refA1, []int{2, 4, 6}, 800000)
	rootB, err := trc.ComputeRoot(dA2, dS2)
	if err != nil {
		t.Fatal(err)
	}
	refA2, refS2 := applyDelta(refA1, refS1, dA2, dS2)
	if want := fullRoot(t, refA2, refS2); rootB != want {
		t.Errorf("block B (157-like, MIXED tree) MISMATCH inc=%x want=%x", rootB[:8], want[:8])
	}
}

// TestDeepStorageInsert reproduces block 157's bug: inserting NEW storage slots
// into a deep storage trie. eth-el showed the incremental storage root wrong for
// such inserts (despite the marker). Tests both native and reth-shape, and a few
// scales, since the account-insert analog needed the right size/position.
func TestDeepStorageInsert(t *testing.T) {
	run := func(t *testing.T, n, slots, nins int, reth bool) (types.Hash, types.Hash) {
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
		if reth {
			stripToRethShape(t, tx)
		}
		a := addrs[0]
		na := *accts[a]
		dA := map[types.Address]*account.StateAccount{a: &na}
		ins := map[types.Hash]*uint256.Int{}
		for j := slots; j < slots+nins; j++ { // NEW slot indices (not in original)
			var s types.Hash
			binary.BigEndian.PutUint32(s[0:4], uint32(j))
			s[31] = byte(0)
			ins[s] = uint256.NewInt(uint64(j) + 1)
		}
		dS := map[types.Address]map[types.Hash]*uint256.Int{a: ins}
		trc.SetIncremental(true)
		rInc, err := trc.ComputeRoot(dA, dS)
		if err != nil {
			t.Fatal(err)
		}
		refA, refS := applyDelta(accts, stor, dA, dS)
		return rInc, fullRoot(t, refA, refS)
	}
	type cfg struct {
		n, slots, nins int
		reth           bool
	}
	for _, cc := range []cfg{
		{2, 1024, 3, false}, {2, 1024, 3, true},
		{2, 4096, 3, false}, {2, 4096, 3, true},
		{4, 2048, 5, false}, {4, 2048, 5, true},
		{2, 8192, 8, false},
	} {
		cc := cc
		t.Run(fmt.Sprintf("n=%d_slots=%d_ins=%d_reth=%v", cc.n, cc.slots, cc.nins, cc.reth), func(t *testing.T) {
			rInc, rRef := run(t, cc.n, cc.slots, cc.nins, cc.reth)
			if rInc != rRef {
				t.Errorf("MISMATCH inc=%x ref=%x", rInc[:8], rRef[:8])
			}
		})
	}
}

// TestSequentialStorageInsert checks two consecutive incremental blocks that each
// insert new storage slots: block B reads the TrieOfStorage that block A persisted,
// so both roots must match a full descent.
func TestSequentialStorageInsert(t *testing.T) {
	n, slots := 1, 4096
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
	a := addrs[0]
	newSlots := func(from, k int) map[types.Hash]*uint256.Int {
		m := map[types.Hash]*uint256.Int{}
		for j := from; j < from+k; j++ {
			var s types.Hash
			binary.BigEndian.PutUint32(s[0:4], uint32(j))
			s[31] = 0
			m[s] = uint256.NewInt(uint64(j) + 1)
		}
		return m
	}
	trc.SetIncremental(true)

	// Block A: insert new slots → Phase-4 writes (duplicates without the fix).
	naA := *accts[a]
	dAA := map[types.Address]*account.StateAccount{a: &naA}
	dSA := map[types.Address]map[types.Hash]*uint256.Int{a: newSlots(slots, 8)}
	rootA, err := trc.ComputeRoot(dAA, dSA)
	if err != nil {
		t.Fatal(err)
	}
	refAA, refSA := applyDelta(accts, stor, dAA, dSA)
	if want := fullRoot(t, refAA, refSA); rootA != want {
		t.Fatalf("block A MISMATCH inc=%x want=%x", rootA[:8], want[:8])
	}

	// Block B: more inserts, on block A's persisted TrieOfStorage.
	naB := *refAA[a]
	dAB := map[types.Address]*account.StateAccount{a: &naB}
	dSB := map[types.Address]map[types.Hash]*uint256.Int{a: newSlots(slots+8, 8)}
	rootB, err := trc.ComputeRoot(dAB, dSB)
	if err != nil {
		t.Fatal(err)
	}
	refAB, refSB := applyDelta(refAA, refSA, dAB, dSB)
	if want := fullRoot(t, refAB, refSB); rootB != want {
		t.Errorf("block B (reads block A's TrieOfStorage) MISMATCH inc=%x want=%x", rootB[:8], want[:8])
	}
}

// TestRethShapeNoRootRecord reproduces block-157's exact condition: reth-migrated
// TrieOfStorage OMITS the keylen-32 empty-path storage "account.root" records.
// We build a normal (erigon-shape) trie, DELETE those records to mimic reth, then
// run an incremental insert. The loader must recompute each storage root from its
// top-level children + hashed leaves on demand (reth's algorithm) — no cached ""
// root node. If the no-root cursor path is incomplete, inc != full here.
func TestRethShapeNoRootRecord(t *testing.T) {
	for _, slots := range []int{24, 256, 4096} {
		t.Run(fmt.Sprintf("slots=%d", slots), func(t *testing.T) {
			accts, stor, addrs := buildDeep(3, slots)
			db := memdb.NewTestDB(t)
			tx, _ := db.BeginRw(context.Background())
			defer tx.Rollback()
			trc := NewTrieRootComputer()
			trc.SetRwTx(tx)
			trc.SetIncremental(false)
			if _, err := trc.ComputeRoot(accts, stor); err != nil {
				t.Fatal(err)
			}
			// Mimic reth: drop every keylen-32 (empty-path) storage-root record.
			c, _ := tx.Cursor(modules.TrieOfStorage)
			var del [][]byte
			for k, _, e := c.First(); k != nil; k, _, e = c.Next() {
				if e != nil {
					t.Fatal(e)
				}
				if len(k) == 32 {
					del = append(del, append([]byte(nil), k...))
				}
			}
			c.Close()
			for _, k := range del {
				if err := tx.Delete(modules.TrieOfStorage, k); err != nil {
					t.Fatal(err)
				}
			}
			t.Logf("deleted %d keylen-32 root records (reth shape)", len(del))

			a := addrs[0]
			trc.SetIncremental(true)
			ins := map[types.Hash]*uint256.Int{}
			for j := slots; j < slots+5; j++ {
				var s types.Hash
				binary.BigEndian.PutUint32(s[0:4], uint32(j))
				ins[s] = uint256.NewInt(uint64(j) + 1)
			}
			na := *accts[a]
			dA := map[types.Address]*account.StateAccount{a: &na}
			dS := map[types.Address]map[types.Hash]*uint256.Int{a: ins}
			rootInc, err := trc.ComputeRoot(dA, dS)
			if err != nil {
				t.Fatal(err)
			}
			refA, refS := applyDelta(accts, stor, dA, dS)
			if want := fullRoot(t, refA, refS); rootInc != want {
				t.Errorf("reth-shape (no keylen-32) MISMATCH inc=%x want=%x", rootInc[:8], want[:8])
			}
		})
	}
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

