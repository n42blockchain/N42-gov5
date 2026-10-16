// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package commitment

import (
	"bytes"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/lib/qmdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/state"
)

// TestBasePrefillEquivalence (P15): a child's reader -- the parent's
// post-state layered over the tree -- answers the same with no prefill, a
// prefill taken while the tree held the grandparent (read after the
// parent's root applied), and one taken on the parent: for an account the
// parent wrote, one it emptied (EIP-161 delete), one it self-destructed
// (and that one's storage), one it created, a slot it wrote, a slot it did
// not, an account it did not touch, and an address nobody has (a key the
// prefill does not hold: the miss path reads the tree). The parent's root
// is labelled for its block, and the mutation count tells the states apart.
func TestBasePrefillEquivalence(t *testing.T) {
	prevCfg := kv.ChaindataTablesCfg
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	t.Cleanup(func() { kv.ChaindataTablesCfg = prevCfg })
	db := memdb.NewTestDB(t)
	rc := NewQMDBRootComputer()

	addr := func(b byte) types.Address { return types.Address{0xa0, b} }
	var (
		wrote, emptied, destroyed, created, untouched, contract, nobody = addr(1), addr(2), addr(3), addr(4), addr(5), addr(6), addr(7)
	)
	slotW, slotU, slotD := types.Hash{1}, types.Hash{2}, types.Hash{3}

	// The grandparent's state.
	gp := map[types.Address]*account.StateAccount{
		wrote: mkAccount(1, 1000), emptied: mkAccount(0, 5), destroyed: mkAccount(3, 70),
		untouched: mkAccount(9, 900), contract: mkAccount(1, 1),
	}
	gpStor := map[types.Address]map[types.Hash]*uint256.Int{
		contract:  {slotW: uint256.NewInt(11), slotU: uint256.NewInt(12)},
		destroyed: {slotD: uint256.NewInt(13)},
	}
	if _, err := rc.ComputeRoot(gp, gpStor); err != nil {
		t.Fatal(err)
	}
	st0 := rc.AppliedState()
	if st0.Labelled || st0.Gen != 1 {
		t.Fatalf("unlabelled root: %+v", st0)
	}

	allKeys := []qmdb.Hash{}
	for _, a := range []types.Address{wrote, emptied, destroyed, created, untouched, contract} {
		allKeys = append(allKeys, qmdb.Hash(AccountKeyHash(a)))
	}
	allKeys = append(allKeys, qmdb.Hash(StorageKeyHash(contract, slotW)), qmdb.Hash(StorageKeyHash(contract, slotU)),
		qmdb.Hash(StorageKeyHash(destroyed, slotD)))
	fill := func() *BasePrefill {
		t.Helper()
		pf, err := rc.FillBasePrefill(db, [][]qmdb.Hash{allKeys[:4], allKeys[2:]}, 3, nil)
		if err != nil {
			t.Fatal(err)
		}
		if pf.Len() != len(allKeys) {
			t.Fatalf("prefilled %d keys, want %d", pf.Len(), len(allKeys))
		}
		return pf
	}
	pfGP := fill()

	// The parent: executed on the grandparent's tree.
	plain := &fakePlain{}
	ibs := state.New(NewQMDBStateReader(NewLookupSource(rc, nil), plain, QMDBReadOn))
	ibs.SetNonce(wrote, 2)
	ibs.SubBalance(wrote, uint256.NewInt(100))
	ibs.SubBalance(emptied, uint256.NewInt(5))
	ibs.Selfdestruct(destroyed)
	ibs.AddBalance(created, uint256.NewInt(42))
	ibs.SetState(contract, &slotW, *uint256.NewInt(99))
	ibs.SetRootComputer(rc)
	var post *state.PostState
	parentHash, gpHash := types.Hash{0xbb}, types.Hash{0xaa}
	ibs.SetRootPostStateHook(func(p *state.PostState) {
		post = p
		rc.LabelNextRoot(p, 2, parentHash, gpHash)
	})
	ibs.IntermediateRoot()
	if post == nil {
		t.Fatal("no post-state published")
	}
	st1 := rc.AppliedState()
	if !st1.Labelled || st1.Gen != st0.Gen+1 || st1.Number != 2 || st1.Hash != parentHash || st1.ParentHash != gpHash {
		t.Fatalf("parent root not labelled: %+v", st1)
	}
	pfP := fill()

	// The grandparent prefill really holds the old values: the layer must
	// shadow them.
	if v, _, _ := pfGP.get(qmdb.Hash(AccountKeyHash(wrote))); bytes.Equal(v, mustLookup(t, rc, AccountKeyHash(wrote))) {
		t.Fatal("the grandparent prefill holds the parent's value: the test proves nothing")
	}

	type answer struct {
		acct map[types.Address]*account.StateAccount
		slot map[string][]byte
	}
	read := func(pf *BasePrefill) answer {
		src := NewLookupSource(rc, nil)
		if pf != nil {
			src.SetPrefill(pf)
		}
		r := state.LayerPostStates([]*state.PostState{post}, NewQMDBStateReader(src, plain, QMDBReadOn))
		ans := answer{acct: map[types.Address]*account.StateAccount{}, slot: map[string][]byte{}}
		for _, a := range []types.Address{wrote, emptied, destroyed, created, untouched, contract, nobody} {
			v, err := r.ReadAccountData(a)
			if err != nil {
				t.Fatal(err)
			}
			ans.acct[a] = v
		}
		for _, s := range []struct {
			a types.Address
			k types.Hash
		}{{contract, slotW}, {contract, slotU}, {destroyed, slotD}, {nobody, slotW}} {
			k := s.k
			v, err := r.ReadAccountStorage(s.a, &k)
			if err != nil {
				t.Fatal(err)
			}
			ans.slot[s.a.Hex()+k.Hex()] = v
		}
		return ans
	}
	want := read(nil)
	if want.acct[wrote] == nil || want.acct[wrote].Nonce != 2 || want.acct[emptied] != nil || want.acct[destroyed] != nil ||
		want.acct[created] == nil || want.acct[nobody] != nil || want.slot[destroyed.Hex()+slotD.Hex()] != nil {
		t.Fatalf("unexpected reference answers: %+v", want)
	}
	for name, pf := range map[string]*BasePrefill{"grandparent": pfGP, "parent": pfP} {
		got := read(pf)
		for a, w := range want.acct {
			g := got.acct[a]
			if (g == nil) != (w == nil) || (g != nil && !bytes.Equal(g.MarshalV2(), w.MarshalV2())) {
				t.Fatalf("%s prefill: account %x reads %+v, want %+v", name, a[:2], g, w)
			}
		}
		for k, w := range want.slot {
			if !bytes.Equal(got.slot[k], w) {
				t.Fatalf("%s prefill: slot %s reads %x, want %x", name, k, got.slot[k], w)
			}
		}
		hits, misses := pf.Stats()
		if hits == 0 || misses == 0 {
			t.Fatalf("%s prefill: hits %d misses %d, want both", name, hits, misses)
		}
	}

	// A label for another dirty set is dropped; any other mutation is
	// unlabelled.
	rc.LabelNextRoot(post, 3, types.Hash{0xcc}, parentHash)
	if _, err := rc.ComputeRoot(map[types.Address]*account.StateAccount{untouched: mkAccount(10, 1)}, nil); err != nil {
		t.Fatal(err)
	}
	if st := rc.AppliedState(); st.Labelled || st.Gen != st1.Gen+1 {
		t.Fatalf("a label applied to another dirty set: %+v", st)
	}
	if err := rc.LoadFrom(emptyGetter{}); err == nil {
		if st := rc.AppliedState(); st.Labelled || st.Gen != st1.Gen+2 {
			t.Fatalf("reload not counted: %+v", st)
		}
	}
}

func mustLookup(t *testing.T, rc *QMDBRootComputer, k [32]byte) []byte {
	t.Helper()
	v, found, _ := rc.Lookup(qmdb.Hash(k), nil)
	if !found {
		t.Fatalf("key %x absent", k[:4])
	}
	return v
}

// emptyGetter is a store with nothing in it.
type emptyGetter struct{}

func (emptyGetter) GetOne(string, []byte) ([]byte, error) { return nil, nil }
