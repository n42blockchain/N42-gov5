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

// TestBasePrefillDeepEquivalence (P15b): a prefill read on the tree two
// blocks below the child's parent N -- in state N-2 -- answers the child's
// reads exactly as the tree does once N-1's and N's post-states are both
// layered over it (newest outermost), and so do prefills read at N-1 and
// at N under the same layers: an account and a slot N-1 wrote and N did
// not, an account both wrote, an account N-1 emptied (EIP-161 delete) and N
// re-created, a slot N-1 deleted and N wrote again, a slot N-1 deleted and
// N left, an account N-1 wrote and N emptied, a slot only N wrote,
// untouched keys and a key nobody has. Without N-1's layer the N-2 prefill
// answers differently: the equivalence depends on every layer.
func TestBasePrefillDeepEquivalence(t *testing.T) {
	prevCfg := kv.ChaindataTablesCfg
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	t.Cleanup(func() { kv.ChaindataTablesCfg = prevCfg })
	db := memdb.NewTestDB(t)
	rc := NewQMDBRootComputer()

	addr := func(b byte) types.Address { return types.Address{0xb0, b} }
	var (
		w1, w2, recr, del2, contract, untouched, nobody = addr(1), addr(2), addr(3), addr(4), addr(5), addr(6), addr(7)
	)
	s1, s2, s3, sR, sD := types.Hash{1}, types.Hash{2}, types.Hash{3}, types.Hash{4}, types.Hash{5}
	accts := []types.Address{w1, w2, recr, del2, contract, untouched, nobody}
	type slotRef struct {
		a types.Address
		k types.Hash
	}
	slots := []slotRef{{contract, s1}, {contract, s2}, {contract, s3}, {contract, sR}, {contract, sD}, {nobody, s1}}

	// N-2.
	if _, err := rc.ComputeRoot(map[types.Address]*account.StateAccount{
		w1: mkAccount(1, 100), w2: mkAccount(1, 200), recr: mkAccount(0, 40), del2: mkAccount(0, 5),
		contract: mkAccount(1, 1), untouched: mkAccount(9, 9),
	}, map[types.Address]map[types.Hash]*uint256.Int{
		contract: {s1: uint256.NewInt(11), s2: uint256.NewInt(12), s3: uint256.NewInt(13), sR: uint256.NewInt(14), sD: uint256.NewInt(15)},
	}); err != nil {
		t.Fatal(err)
	}
	stGP2 := rc.AppliedState()

	var keys []qmdb.Hash
	for _, a := range accts {
		keys = append(keys, qmdb.Hash(AccountKeyHash(a)))
	}
	for _, s := range slots {
		keys = append(keys, qmdb.Hash(StorageKeyHash(s.a, s.k)))
	}
	fill := func() *BasePrefill {
		t.Helper()
		pf, err := rc.FillBasePrefill(db, [][]qmdb.Hash{keys}, 2, nil)
		if err != nil {
			t.Fatal(err)
		}
		if pf.Len() != len(keys) {
			t.Fatalf("prefilled %d keys, want %d", pf.Len(), len(keys))
		}
		return pf
	}
	pf2 := fill()

	plain := &fakePlain{}
	root := func(number uint64, hash, parent types.Hash, exec func(ibs *state.IntraBlockState)) *state.PostState {
		t.Helper()
		ibs := state.New(NewQMDBStateReader(NewLookupSource(rc, nil), plain, QMDBReadOn))
		exec(ibs)
		ibs.SetRootComputer(rc)
		var post *state.PostState
		ibs.SetRootPostStateHook(func(p *state.PostState) {
			post = p
			rc.LabelNextRoot(p, number, hash, parent)
		})
		ibs.IntermediateRoot()
		if post == nil {
			t.Fatalf("block %d published no post-state", number)
		}
		return post
	}
	hGP2, hGP, hP := types.Hash{0xa2}, types.Hash{0xa1}, types.Hash{0xa0}
	// N-1, executed on the tree in state N-2.
	postGP := root(9, hGP, hGP2, func(ibs *state.IntraBlockState) {
		ibs.SetNonce(w1, 2)
		ibs.SetNonce(w2, 2)
		ibs.SubBalance(recr, uint256.NewInt(40))
		ibs.AddBalance(del2, uint256.NewInt(1))
		ibs.SetState(contract, &s1, *uint256.NewInt(99))
		ibs.SetState(contract, &sR, uint256.Int{})
		ibs.SetState(contract, &sD, uint256.Int{})
	})
	pf1 := fill()
	// N, executed on the tree in state N-1.
	postP := root(10, hP, hGP, func(ibs *state.IntraBlockState) {
		ibs.SetNonce(w2, 3)
		ibs.AddBalance(recr, uint256.NewInt(7))
		ibs.SubBalance(del2, uint256.NewInt(6))
		ibs.SetState(contract, &s2, *uint256.NewInt(77))
		ibs.SetState(contract, &sR, *uint256.NewInt(88))
	})
	pf0 := fill()

	// The mutations since N-2 are N-1's root and N's, labelled.
	states, ok := rc.AppliedSince(stGP2.Gen)
	if !ok || len(states) != 2 || !states[0].Labelled || states[0].Number != 9 || states[0].Hash != hGP ||
		!states[1].Labelled || states[1].Number != 10 || states[1].Hash != hP {
		t.Fatalf("applied since N-2: %+v ok=%v", states, ok)
	}

	type answer struct {
		acct map[types.Address][]byte
		slot map[slotRef][]byte
	}
	read := func(pf *BasePrefill, layers []*state.PostState) answer {
		t.Helper()
		src := NewLookupSource(rc, nil)
		if pf != nil {
			src.SetPrefill(pf)
		}
		r := state.LayerPostStates(layers, NewQMDBStateReader(src, plain, QMDBReadOn))
		ans := answer{acct: map[types.Address][]byte{}, slot: map[slotRef][]byte{}}
		for _, a := range accts {
			v, err := r.ReadAccountData(a)
			if err != nil {
				t.Fatal(err)
			}
			if v != nil {
				ans.acct[a] = v.MarshalV2()
			}
		}
		for _, s := range slots {
			k := s.k
			v, err := r.ReadAccountStorage(s.a, &k)
			if err != nil {
				t.Fatal(err)
			}
			ans.slot[s] = v
		}
		return ans
	}
	same := func(a, b answer) bool {
		for _, x := range accts {
			if !bytes.Equal(a.acct[x], b.acct[x]) {
				return false
			}
		}
		for _, s := range slots {
			if !bytes.Equal(a.slot[s], b.slot[s]) {
				return false
			}
		}
		return true
	}
	deep := []*state.PostState{postP, postGP}
	want := read(nil, deep)
	if !same(want, read(nil, []*state.PostState{postP})) {
		t.Fatalf("the reference depends on the layers over the tree in state N: %+v vs %+v", want, read(nil, []*state.PostState{postP}))
	}
	if want.acct[w1] == nil || want.acct[recr] == nil || want.acct[del2] != nil || want.acct[nobody] != nil ||
		want.slot[slotRef{contract, sD}] != nil || !bytes.Equal(want.slot[slotRef{contract, sR}], []byte{88}) ||
		!bytes.Equal(want.slot[slotRef{contract, s1}], []byte{99}) {
		t.Fatalf("unexpected reference answers: %+v", want)
	}
	for name, pf := range map[string]*BasePrefill{"N-2": pf2, "N-1": pf1, "N": pf0} {
		if got := read(pf, deep); !same(got, want) {
			t.Fatalf("%s prefill under N's and N-1's layers: %+v, want %+v", name, got, want)
		}
		if hits, _ := pf.Stats(); hits == 0 {
			t.Fatalf("%s prefill answered nothing", name)
		}
	}
	// A missing layer: the N-2 prefill under N's post-state alone reads
	// N-1's writes as they were before N-1.
	if same(read(pf2, []*state.PostState{postP}), want) {
		t.Fatal("the N-2 prefill without N-1's layer reads the same: the equivalence proves nothing")
	}
}

// TestAppliedSince: the applied-state log returns every mutation after a
// generation, and nothing once the log no longer reaches back.
func TestAppliedSince(t *testing.T) {
	rc := NewQMDBRootComputer()
	if st, ok := rc.AppliedSince(0); !ok || len(st) != 0 {
		t.Fatalf("empty: %+v %v", st, ok)
	}
	for i := 0; i < appliedLogLen+3; i++ {
		if _, err := rc.ComputeRoot(map[types.Address]*account.StateAccount{{0xc0, byte(i)}: mkAccount(1, 1)}, nil); err != nil {
			t.Fatal(err)
		}
	}
	cur := rc.AppliedState().Gen
	st, ok := rc.AppliedSince(cur - 2)
	if !ok || len(st) != 2 || st[0].Gen != cur-1 || st[1].Gen != cur {
		t.Fatalf("last two: %+v %v", st, ok)
	}
	if _, ok := rc.AppliedSince(cur - appliedLogLen - 1); ok {
		t.Fatal("a gap past the log reported complete")
	}
	if _, ok := rc.AppliedSince(cur + 1); ok {
		t.Fatal("a future generation reported complete")
	}
}
