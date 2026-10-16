// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"fmt"
	"reflect"
	"sort"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

// bulkKnown is a KnownAccounts over a map (a nil value: cached absent).
type bulkKnown map[types.Address]*account.StateAccount

func (k bulkKnown) Get(a types.Address) (*account.StateAccount, bool) {
	acc, ok := k[a]
	if !ok || acc == nil {
		return nil, ok
	}
	c := *acc
	return &c, true
}
func (k bulkKnown) Peek(a types.Address) (*account.StateAccount, bool) {
	acc, ok := k[a]
	return acc, ok
}

// bulkFixture is a block-shaped account fold: senders written, recipients
// written or only credited, some absent from the base state, some not in
// the known cache (base reads), deletions, a wiped (recreated) address, a
// live object, a pending credit and a remembered-absent address on full
// entries, and a seeded prefetch set.
type bulkFixture struct {
	store   map[types.Address]*account.StateAccount
	known   bulkKnown
	seed    map[types.Address]*account.StateAccount
	entries []AccountApply
	// pre-state shaping
	live, pending, nilAcc, wiped, deletedLive types.Address
}

func newBulkFixture(n int) *bulkFixture {
	f := &bulkFixture{
		store: map[types.Address]*account.StateAccount{},
		known: bulkKnown{},
		seed:  map[types.Address]*account.StateAccount{},
	}
	for i := 0; i < n; i++ {
		a := testAddr("bulk", i)
		inStore := i%9 != 0
		if inStore {
			acc := account.NewAccount()
			acc.Balance.SetUint64(uint64(1000 + i))
			acc.Nonce = uint64(i % 5)
			if i%23 == 0 {
				acc.CodeHash = types.Hash{byte(i), 1}
			}
			f.store[a] = &acc
		}
		switch {
		case i%31 == 0:
			// not cached: the base reader answers, serially
		case i%37 == 0:
			if inStore {
				f.seed[a] = f.store[a]
			} else {
				f.seed[a] = nil
			}
		default:
			if inStore {
				f.known[a] = f.store[a]
			} else {
				f.known[a] = nil
			}
		}
		e := AccountApply{Addr: a}
		switch i % 6 {
		case 0, 1, 2:
			e.Balance.SetUint64(uint64(500 + 3*i))
			e.Nonce = uint64(i%5 + 1)
		case 3:
			e.Balance.SetUint64(0) // written to zero
			e.Nonce = 0
		case 4:
			e.Delta = uint256.NewInt(uint64(i % 3)) // zero deltas included
		case 5:
			if i%4 == 1 {
				e.Delete = true
			} else {
				e.Delta = uint256.NewInt(7)
			}
		}
		f.entries = append(f.entries, e)
	}
	// Full entries whose pre-state takes the per-entry path.
	pick := func(from int) types.Address {
		for i := from; i < n; i++ {
			if f.entries[i].Delta == nil && !f.entries[i].Delete {
				if _, ok := f.known[f.entries[i].Addr]; ok {
					return f.entries[i].Addr
				}
			}
		}
		panic("no full entry")
	}
	f.live = pick(n / 5)
	f.pending = pick(n/5 + 7)
	f.wiped = pick(n/5 + 19)
	f.nilAcc = types.Address{0xee, 1}
	f.known[f.nilAcc] = nil
	f.entries = append(f.entries, AccountApply{Addr: f.nilAcc, Balance: *uint256.NewInt(3), Nonce: 1})
	for i := n / 2; i < n; i++ {
		if f.entries[i].Delete {
			if _, ok := f.store[f.entries[i].Addr]; ok {
				f.deletedLive = f.entries[i].Addr
				break
			}
		}
	}
	return f
}

// build makes a state over the fixture with the pre-state applied (what a
// block's system calls and Pass 0 leave before the fold) and a snapshot id
// taken before the fold.
func (f *bulkFixture) build() (*IntraBlockState, *AccountPrefetch, int) {
	pf := NewAccountPrefetch(mapStateReader{accts: f.store})
	pf.Seed(f.seed)
	pf.SetKnown(f.known)
	ibs := New(pf)
	ibs.SetAccountPrefetch(pf)
	ibs.Reserve(len(f.entries), len(f.entries))
	ibs.GetBalance(f.live)                        // a live object
	ibs.AddBalance(f.pending, uint256.NewInt(11)) // a pending credit
	ibs.GetBalance(f.nilAcc)                      // remembered absent
	ibs.CreateAccount(f.wiped, true)              // Pass 0's wipe
	ibs.GetNonce(f.deletedLive)                   // a deletion of a live object
	return ibs, pf, ibs.Snapshot()
}

type journalView struct {
	kind    journalKind
	dirties bool
	addr    types.Address
	key     types.Hash
	val     uint256.Int
	num     uint64
	ref     string
}

func (f *bulkFixture) snapshotState(ibs *IntraBlockState, pf *AccountPrefetch) map[string]any {
	out := map[string]any{}
	objs := map[types.Address]string{}
	for a, o := range ibs.stateObjects {
		objs[a] = fmt.Sprintf("%+v|%+v|%v%v%v%v|%x", o.data, o.original, o.dirtyCode, o.selfdestructed, o.deleted, o.created, o.code)
	}
	out["objects"] = objs
	out["dirty"] = ibs.stateObjectsDirty
	out["nil"] = ibs.nilAccounts
	bi := map[types.Address]string{}
	for a, b := range ibs.balanceInc {
		bi[a] = fmt.Sprintf("%s/%d/%v", b.increase.String(), b.count, b.transferred)
	}
	out["balanceInc"] = bi
	out["wipes"] = ibs.storageWipes
	var j []journalView
	for _, r := range ibs.journal.entries {
		j = append(j, journalView{r.kind, r.dirties, r.addr, r.key, r.val, r.num, fmt.Sprintf("%T", r.ref)})
	}
	out["journal"] = j
	out["jdirties"] = ibs.journal.dirties
	out["hits"] = pf.Hits()
	out["last"] = ibs.lastAddr
	out["err"] = fmt.Sprint(ibs.savedErr)
	return out
}

func committed(t *testing.T, ibs *IntraBlockState) []string {
	t.Helper()
	w := &recordingWriter{}
	rules := &params.Rules{IsSpuriousDragon: true}
	if err := ibs.FinalizeTx(rules, NewNoopWriter()); err != nil {
		t.Fatal(err)
	}
	if err := ibs.CommitBlock(rules, w); err != nil {
		t.Fatal(err)
	}
	log := append([]string(nil), w.log...)
	sort.Strings(log) // CommitBlock's order is its own; the rows must match
	return log
}

func compareStates(t *testing.T, what string, want, got map[string]any) {
	t.Helper()
	for k, w := range want {
		if !reflect.DeepEqual(w, got[k]) {
			t.Fatalf("%s: %s differs\nwant %v\n got %v", what, k, w, got[k])
		}
	}
}

// ApplyAccounts must leave exactly what the per-entry calls leave: objects,
// dirties, journal records in order, pending credits, wipes, prefetch hits,
// the state after a revert across the fold, and the committed rows.
func TestApplyAccountsMatchesPerEntry(t *testing.T) {
	f := newBulkFixture(6000)

	ref, refPf, refSnap := f.build()
	for i := range f.entries {
		ref.applyAccountEntry(&f.entries[i])
	}
	bulk, bulkPf, bulkSnap := f.build()
	bulk.ApplyAccounts(f.entries, 8)

	want, got := f.snapshotState(ref, refPf), f.snapshotState(bulk, bulkPf)
	compareStates(t, "after apply", want, got)
	// The bulk path must actually have prepared most full entries, and
	// declined the shaped ones.
	probe, probePf, _ := f.build()
	prep := make([]preparedAccount, len(f.entries))
	if n := probe.prepareAccounts(probePf, f.entries, prep); n < len(f.entries)/3 {
		t.Fatalf("only %d of %d entries prepared", n, len(f.entries))
	}
	for i, e := range f.entries {
		switch e.Addr {
		case f.live, f.pending, f.nilAcc, f.wiped:
			if prep[i].obj != nil {
				t.Fatalf("entry %x prepared despite its pre-state", e.Addr)
			}
		}
	}

	// The journal is the same: reverting across the fold leaves the same
	// state on both.
	ref2, ref2Pf, _ := f.build()
	bulk2, bulk2Pf, _ := f.build()
	for i := range f.entries {
		ref2.applyAccountEntry(&f.entries[i])
	}
	bulk2.ApplyAccounts(f.entries, 8)
	ref2.RevertToSnapshot(refSnap)
	bulk2.RevertToSnapshot(bulkSnap)
	compareStates(t, "after revert", f.snapshotState(ref2, ref2Pf), f.snapshotState(bulk2, bulk2Pf))

	if w, g := committed(t, ref), committed(t, bulk); !reflect.DeepEqual(w, g) {
		t.Fatalf("committed rows differ\nwant %v\n got %v", w, g)
	}
}

// Without a prefetch layer as the reader (a recorder above it) every entry
// takes the per-entry calls; the result still matches.
func TestApplyAccountsWrappedReaderFallsBack(t *testing.T) {
	f := newBulkFixture(3000)
	mk := func() *IntraBlockState {
		pf := NewAccountPrefetch(mapStateReader{accts: f.store})
		pf.SetKnown(f.known)
		ibs := New(&countingReader{accts: f.store})
		ibs.SetAccountPrefetch(pf)
		return ibs
	}
	ref, bulk := mk(), mk()
	for i := range f.entries {
		ref.applyAccountEntry(&f.entries[i])
	}
	bulk.ApplyAccounts(f.entries, 8)
	if r, b := ref.stateReader.(*countingReader).reads, bulk.stateReader.(*countingReader).reads; r != b {
		t.Fatalf("reader reads %d vs %d", r, b)
	}
	if w, g := committed(t, ref), committed(t, bulk); !reflect.DeepEqual(w, g) {
		t.Fatalf("committed rows differ")
	}
}

func benchApplyAccounts(b *testing.B, bulk, pooled bool) {
	const n = 160_000
	prevPooling := stateObjectPooling
	stateObjectPooling = pooled
	defer func() { stateObjectPooling = prevPooling }()
	store := map[types.Address]*account.StateAccount{}
	known := bulkKnown{}
	entries := make([]AccountApply, n)
	for i := 0; i < n; i++ {
		a := testAddr("bench", i)
		acc := account.NewAccount()
		acc.Balance.SetUint64(uint64(i + 1))
		store[a] = &acc
		known[a] = &acc
		entries[i] = AccountApply{Addr: a, Balance: *uint256.NewInt(uint64(i)), Nonce: 1}
		if i%8 == 7 {
			entries[i] = AccountApply{Addr: a, Delta: uint256.NewInt(3)}
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for it := 0; it < b.N; it++ {
		b.StopTimer()
		pf := NewAccountPrefetch(mapStateReader{accts: store})
		pf.SetKnown(known)
		ibs := New(pf)
		ibs.SetAccountPrefetch(pf)
		ibs.Reserve(n, n/8)
		b.StartTimer()
		if bulk {
			ibs.ApplyAccounts(entries, 8)
		} else {
			for i := range entries {
				ibs.applyAccountEntry(&entries[i])
			}
		}
		b.StopTimer()
		ibs.Reset() // the objects go back to the pool, as between blocks
		b.StartTimer()
	}
}

// Pooled: the objects come back through sync.Pool, which a single Reset
// fills on one P -- the bulk pass's workers then steal from it.
func BenchmarkApplyAccountsPerEntry(b *testing.B)       { benchApplyAccounts(b, false, false) }
func BenchmarkApplyAccountsBulk(b *testing.B)           { benchApplyAccounts(b, true, false) }
func BenchmarkApplyAccountsPerEntryPooled(b *testing.B) { benchApplyAccounts(b, false, true) }
func BenchmarkApplyAccountsBulkPooled(b *testing.B)     { benchApplyAccounts(b, true, true) }
