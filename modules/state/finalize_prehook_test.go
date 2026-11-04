// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

// prehookOutcome is everything a block leaves from its root on: the root
// computer's input, the published post-state, the store's read order, the
// state after the root, the last-resolved address and CommitBlock's rows.
type prehookOutcome struct {
	accts    map[types.Address]*account.StateAccount
	stor     map[types.Address]map[types.Hash]*uint256.Int
	post     *PostState
	reads    []types.Address
	after    string
	lastAddr types.Address
	rows     []string
	cut      bool
	detached int
}

// runPrehookEdgeBlock is an import-shaped block with the balance-credit
// edge cases: many senders (loaded, dirty), tiny (1 wei) credits to
// recipients that are never loaded (detached at the root), recipients
// credited in several transactions, zero credits to absent accounts (empty:
// wiped in the post-state), a coinbase fee credit every transaction, a
// credited recipient later self-destructed, a contract with storage
// self-destructed, a credit un-folded by a revert on a live object (its
// pending increase folds at the root), a credit loaded by the block end, and
// a reward to a loaded account right before the root (the state's
// last-resolved address is dirty).
func runPrehookEdgeBlock(t *testing.T, nSenders, nRecipients, nTxs int) prehookOutcome {
	t.Helper()
	store := map[types.Address]*account.StateAccount{}
	stor := map[types.Address]map[types.Hash]uint256.Int{}
	senders := make([]types.Address, nSenders)
	for i := range senders {
		senders[i] = testAddr("ph-sender", i)
		a := account.NewAccount()
		a.Balance.SetUint64(1e18)
		a.Nonce = uint64(i % 3)
		store[senders[i]] = &a
	}
	recipients := make([]types.Address, nRecipients)
	for i := range recipients {
		recipients[i] = testAddr("ph-recipient", i)
		if i%10 == 0 {
			continue // absent
		}
		a := account.NewAccount()
		a.Balance.SetUint64(uint64(i))
		store[recipients[i]] = &a
	}
	coinbase := testAddr("ph-coinbase", 0)
	cb := account.NewAccount()
	cb.Balance.SetUint64(5)
	store[coinbase] = &cb
	contract := testAddr("ph-contract", 0)
	ca := account.NewAccount()
	ca.Balance.SetUint64(100)
	ca.CodeHash = types.Hash{0xc0, 0xde}
	store[contract] = &ca
	stor[contract] = map[types.Hash]uint256.Int{{1}: *uint256.NewInt(7), {2}: *uint256.NewInt(8)}
	zeroed := make([]types.Address, 0, 8)
	for i := 0; i < 8; i++ {
		zeroed = append(zeroed, testAddr("ph-zero", i)) // absent, credited 0
	}

	var out prehookOutcome
	reader := ffReader{accts: store, stor: stor, reads: &out.reads}
	pf := NewAccountPrefetch(reader)
	ibs := New(pf)
	ibs.SetAccountPrefetch(pf)
	ibs.SetDetachedPendingCredits(true)
	rc := &capturingRootComputer{}
	ibs.SetRootComputer(rc)
	rules := &params.Rules{IsSpuriousDragon: true, IsByzantium: true, IsLondon: true}
	for i := 0; i < nTxs; i++ {
		s := senders[i%nSenders]
		ibs.SubBalance(s, uint256.NewInt(uint64(21000*(1+i%5))))
		ibs.SetNonce(s, ibs.GetNonce(s)+1)
		ibs.AddBalance(recipients[(i*7919)%nRecipients], uint256.NewInt(1)) // tiny, repeated
		ibs.AddBalance(coinbase, uint256.NewInt(uint64(i%2)))               // fee credit, sometimes zero
		if i%997 == 0 {
			ibs.AddBalance(zeroed[(i/997)%len(zeroed)], new(uint256.Int))
		}
		if i%101 == 0 {
			key := types.Hash{3}
			ibs.SetState(contract, &key, *uint256.NewInt(uint64(i + 1)))
		}
		if i == nTxs/2 {
			// A credit folded inside a call, the call reverted: the object
			// stays live with its increase pending again.
			r := recipients[3]
			snap := ibs.Snapshot()
			ibs.AddBalance(r, uint256.NewInt(1))
			ibs.GetBalance(r)
			ibs.RevertToSnapshot(snap)
		}
		if err := ibs.FinalizeTx(rules, NewNoopWriter()); err != nil {
			t.Fatal(err)
		}
	}
	// A credited recipient and the contract self-destructed.
	ibs.Selfdestruct(recipients[11])
	ibs.Selfdestruct(contract)
	if err := ibs.FinalizeTx(rules, NewNoopWriter()); err != nil {
		t.Fatal(err)
	}
	seed := map[types.Address]*account.StateAccount{}
	for _, a := range ibs.PendingBalanceIncreases() {
		v := store[a]
		if v != nil {
			c := new(account.StateAccount)
			c.Copy(v)
			v = c
		}
		seed[a] = v
	}
	pf.Seed(seed)
	// The block end: a credit loaded, a system address touched.
	ibs.GetBalance(recipients[13])
	ibs.AddBalance(SystemAddress, new(uint256.Int))
	if err := ibs.FinalizeTx(rules, NewNoopWriter()); err != nil {
		t.Fatal(err)
	}
	// The rewards: the coinbase and a loaded sender, last.
	ibs.AddBalance(coinbase, uint256.NewInt(2))
	ibs.AddBalance(senders[nSenders-1], uint256.NewInt(1))
	ibs.GetBalance(senders[nSenders-1])
	ibs.SetRootPostStateHook(func(p *PostState) { out.post = p })
	ibs.IntermediateRoot()
	rp := ibs.LastRootPrepPhases()
	out.cut = rp.Cut
	out.detached, _ = LastRootCredits()
	out.accts, out.stor = rc.accts, rc.stor
	out.after = ibs.FinalizeDigestForTest()
	out.lastAddr = ibs.lastAddr
	w := &recordingWriter{}
	if err := ibs.CommitBlock(rules, w); err != nil {
		t.Fatal(err)
	}
	out.rows = w.log
	return out
}

func comparePrehook(t *testing.T, tag string, want, got prehookOutcome) {
	t.Helper()
	check := func(what string, w, g any) {
		if !reflect.DeepEqual(w, g) {
			t.Fatalf("%s: %s differs", tag, what)
		}
	}
	check("root accounts", want.accts, got.accts)
	check("root storage", want.stor, got.stor)
	check("post-state", want.post, got.post)
	check("read order", want.reads, got.reads)
	check("state after the root", want.after, got.after)
	check("last-resolved address", want.lastAddr, got.lastAddr)
	check("commit rows", want.rows, got.rows)
	check("detached credits", want.detached, got.detached)
}

// N42_PREHOOK_CUT hands the root computer the same dirty set, publishes the
// same post-state, reads the store in the same order and leaves the same
// state and CommitBlock rows as with the flag off, in every mode, on the
// balance-credit edge cases, at the parallel walk's default threshold and
// lowered to every block.
func TestPrehookCutMatchesOff(t *testing.T) {
	defer SetFinalizeFastForTest(true)()
	shapes := []struct{ senders, recipients, txs int }{{2500, 6000, 8000}, {300, 4000, 3000}}
	for _, sh := range shapes {
		for _, min := range []int{1, finalizeFastMin} {
			tag := fmt.Sprintf("senders=%d recipients=%d min=%d", sh.senders, sh.recipients, min)
			func() {
				defer SetFinalizeFastMinForTest(min)()
				restore := SetPrehookCutForTest(0)
				want := runPrehookEdgeBlock(t, sh.senders, sh.recipients, sh.txs)
				restore()
				if want.cut {
					t.Fatalf("%s: cut taken with the flag off", tag)
				}
				if want.detached < detachMinPending {
					t.Fatalf("%s: %d detached credits, the shape must detach", tag, want.detached)
				}
				for _, mode := range []int{1, 2} {
					restore := SetPrehookCutForTest(mode)
					got := runPrehookEdgeBlock(t, sh.senders, sh.recipients, sh.txs)
					restore()
					comparePrehook(t, fmt.Sprintf("%s mode=%d", tag, mode), want, got)
					wantCut := len(want.accts)-want.detached >= min
					if got.cut != wantCut {
						t.Fatalf("%s mode=%d: cut=%v, want %v", tag, mode, got.cut, wantCut)
					}
				}
			}()
		}
	}
}

// The random blocks of TestFinalizeFastMatchesSingleLoop (transfers, zero
// credits, storage, creations, self-destructs and recreations, reverted
// calls, fee credits) give the same outcome with the cut on.
func TestPrehookCutRandomBlocks(t *testing.T) {
	defer SetFinalizeFastForTest(true)()
	defer SetFinalizeFastMinForTest(1)()
	sizes := []struct{ accts, ops int }{{60, 200}, {400, 2000}}
	if !testing.Short() {
		sizes = append(sizes, struct{ accts, ops int }{9000, 30000})
	}
	for _, sz := range sizes {
		for seed := int64(1); seed <= 6; seed++ {
			for _, detach := range []bool{false, true} {
				tag := fmt.Sprintf("accts=%d seed=%d detach=%v", sz.accts, seed, detach)
				restore := SetPrehookCutForTest(0)
				want := runFFBlock(t, seed, sz.accts, sz.ops, detach)
				restore()
				for _, mode := range []int{1, 2} {
					restore := SetPrehookCutForTest(mode)
					got := runFFBlock(t, seed, sz.accts, sz.ops, detach)
					restore()
					requireSameFFBlock(t, fmt.Sprintf("%s mode=%d", tag, mode), want, got)
				}
			}
		}
	}
}

// With the hoisted detach (the fleet's configuration) the cut leaves the
// same finalize digests, root dirty set, post-states and committed rows.
func TestPrehookCutWithHoist(t *testing.T) {
	defer SetFinalizeFastForTest(true)()
	fixtures := map[string]*bulkFixture{
		"bulk":      newBulkFixture(20000),
		"long-runs": newRandomShardFixture(7, 20000, 0.0002),
	}
	for name, f := range fixtures {
		for _, min := range []int{1, finalizeFastMin} {
			func() {
				defer SetFinalizeFastMinForTest(min)()
				restore := SetPrehookCutForTest(0)
				want := runHoistBlock(t, f, true, false, 16)
				restore()
				if !want.hoisted {
					t.Fatalf("%s: hoist not taken", name)
				}
				for _, mode := range []int{1, 2} {
					restore := SetPrehookCutForTest(mode)
					got := runHoistBlock(t, f, true, false, 16)
					restore()
					compareNJ(t, fmt.Sprintf("%s min=%d mode=%d", name, min, mode), want.out, got.out)
					if !got.hoisted || got.detached != want.detached {
						t.Fatalf("%s mode=%d: hoisted=%v detached=%d, want %d", name, mode, got.hoisted, got.detached, want.detached)
					}
				}
			}()
		}
	}
}

// resolveDirtyObjects makes exactly the single pass's getStateObject calls:
// the same objects, reads, state and last-resolved object, over a set with
// the last-resolved address, live objects owing a pending increase, a known
// absent account with a credit, accounts read from the store and absent
// ones.
func TestResolveDirtyObjectsMatchesSerial(t *testing.T) {
	type res struct {
		objs     []string
		reads    []types.Address
		digest   string
		lastAddr types.Address
		lastBal  string
		journal  int
	}
	build := func(parallel bool, lastIdx int) res {
		store := map[types.Address]*account.StateAccount{}
		addrs := make([]types.Address, 0, 3000)
		for i := 0; i < 3000; i++ {
			a := testAddr("rdo", i)
			addrs = append(addrs, a)
			if i%17 == 5 {
				continue // absent
			}
			acc := account.NewAccount()
			acc.Balance.SetUint64(uint64(1000 + i))
			store[a] = &acc
		}
		var r res
		ibs := New(ffReader{accts: store, reads: &r.reads})
		for i, a := range addrs {
			switch i % 6 {
			case 0, 1: // live
				ibs.GetBalance(a)
			case 2: // live, then a pending increase (un-folded by a revert)
				ibs.GetBalance(a)
				snap := ibs.Snapshot()
				ibs.AddBalance(testAddr("rdo-x", i), uint256.NewInt(1))
				ibs.RevertToSnapshot(snap)
				ibs.AddBalance(a, uint256.NewInt(3))
			case 3: // credited, never loaded
				ibs.AddBalance(a, uint256.NewInt(1))
			}
		}
		// Known absent with a credit.
		na := testAddr("rdo-nil", 0)
		ibs.GetBalance(na)
		ibs.AddBalance(na, uint256.NewInt(4))
		addrs = append(addrs, na)
		sortAddresses(addrs)
		ibs.getStateObject(addrs[lastIdx]) // the last-resolved address
		r.reads = r.reads[:0]
		objs := make([]*stateObject, len(addrs))
		if parallel {
			ibs.resolveDirtyObjects(addrs, objs, 8)
		} else {
			for i, a := range addrs {
				objs[i] = ibs.getStateObject(a)
			}
		}
		for _, o := range objs {
			if o == nil {
				r.objs = append(r.objs, "nil")
				continue
			}
			if ibs.stateObjects[o.address] != o {
				r.objs = append(r.objs, "not live")
				continue
			}
			r.objs = append(r.objs, fmt.Sprintf("%x %s", o.address, o.data.Balance.String()))
		}
		r.digest = ibs.FinalizeDigestForTest()
		r.lastAddr = ibs.lastAddr
		if ibs.lastObj != nil {
			r.lastBal = ibs.lastObj.data.Balance.String()
			if ibs.stateObjects[ibs.lastAddr] != ibs.lastObj {
				r.lastBal = "stale"
			}
		}
		r.journal = len(ibs.journal.entries)
		return r
	}
	for _, lastIdx := range []int{0, 1, 2, 3, 5, 1500, 2999, 3000} {
		want, got := build(false, lastIdx), build(true, lastIdx)
		if !reflect.DeepEqual(want, got) {
			t.Fatalf("lastIdx=%d: parallel resolution differs from the serial pass", lastIdx)
		}
	}
}
