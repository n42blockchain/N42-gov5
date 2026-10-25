// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"fmt"
	"math/rand"
	"sort"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/params"
)

// ffReader is a store with storage that records every account read in
// order (the witness recorder's view) and enumerates storage (so a wipe
// captures its pre-block slots).
type ffReader struct {
	accts map[types.Address]*account.StateAccount
	stor  map[types.Address]map[types.Hash]uint256.Int
	reads *[]types.Address
}

func (r ffReader) ReadAccountData(a types.Address) (*account.StateAccount, error) {
	*r.reads = append(*r.reads, a)
	if v := r.accts[a]; v != nil {
		c := new(account.StateAccount)
		c.Copy(v)
		return c, nil
	}
	return nil, nil
}

func (r ffReader) ReadAccountStorage(a types.Address, k *types.Hash) ([]byte, error) {
	if v, ok := r.stor[a][*k]; ok {
		return v.Bytes(), nil
	}
	return nil, nil
}
func (ffReader) ReadAccountCode(types.Address, types.Hash) ([]byte, error)  { return nil, nil }
func (ffReader) ReadAccountCodeSize(types.Address, types.Hash) (int, error) { return 0, nil }
func (r ffReader) ForEachStorage(a types.Address, f func(types.Hash, []byte) bool) error {
	for k, v := range r.stor[a] {
		if !f(k, v.Bytes()) {
			break
		}
	}
	return nil
}

// ffBlock is one random block's outcome: the root computer's dirty set,
// the post-state, the reader's read order and CommitBlock's rows.
type ffBlock struct {
	accts map[types.Address]*account.StateAccount
	stor  map[types.Address]map[types.Hash]*uint256.Int
	post  *PostState
	reads []types.Address
	rows  []string
}

// runFFBlock builds a random block from seed over a fresh state: plain
// transfers to existing and absent recipients (pending credits), zero
// credits, contract storage writes (set, overwrite, clear), contract
// creations with code, self-destructs (of a contract with storage, of a
// fresh account, recreated afterwards), reverted calls, fee credits, a
// transaction boundary every few operations, and the block-end system call
// finalization; then the root.
func runFFBlock(t *testing.T, seed int64, nAccts, nOps int, detach bool) *ffBlock {
	rng := rand.New(rand.NewSource(seed))
	addr := func(i int) types.Address {
		return types.BytesToAddress(crypto.Keccak256([]byte(fmt.Sprintf("ff-%d-%d", seed, i))))
	}
	store := map[types.Address]*account.StateAccount{}
	stor := map[types.Address]map[types.Hash]uint256.Int{}
	for i := 0; i < nAccts; i++ {
		if i%9 == 0 {
			continue // absent
		}
		a := account.NewAccount()
		a.Balance.SetUint64(uint64(1e12 + i))
		a.Nonce = uint64(i % 5)
		if i%13 == 1 {
			a.CodeHash = types.BytesToHash(crypto.Keccak256([]byte{byte(i)}))
			slots := map[types.Hash]uint256.Int{}
			for k := 0; k < 4; k++ {
				slots[types.BytesToHash([]byte{byte(k + 1)})] = *uint256.NewInt(uint64(k + 7))
			}
			stor[addr(i)] = slots
		}
		store[addr(i)] = &a
	}
	out := &ffBlock{}
	reader := ffReader{accts: store, stor: stor, reads: &out.reads}
	var ibs *IntraBlockState
	if detach {
		pf := NewAccountPrefetch(reader)
		ibs = New(pf)
		ibs.SetAccountPrefetch(pf)
		ibs.SetDetachedPendingCredits(true)
	} else {
		ibs = New(reader)
	}
	rc := &capturingRootComputer{}
	ibs.SetRootComputer(rc)
	rules := &params.Rules{IsSpuriousDragon: true, IsByzantium: true, IsLondon: true}
	pick := func() types.Address { return addr(rng.Intn(nAccts)) }
	slot := func() types.Hash { return types.BytesToHash([]byte{byte(1 + rng.Intn(6))}) }
	for op := 0; op < nOps; op++ {
		a := pick()
		switch k := rng.Intn(100); {
		case k < 55: // transfer
			from := pick()
			ibs.SubBalance(from, uint256.NewInt(21000))
			ibs.SetNonce(from, ibs.GetNonce(from)+1)
			ibs.AddBalance(a, uint256.NewInt(uint64(rng.Intn(3))*uint64(rng.Intn(1000))))
		case k < 70: // storage
			var v uint256.Int
			if rng.Intn(4) != 0 {
				v.SetUint64(uint64(rng.Intn(50)))
			}
			key := slot()
			ibs.SetState(a, &key, v)
		case k < 75: // create with code
			c := addr(nAccts + op)
			ibs.CreateAccount(c, true)
			ibs.SetCode(c, []byte{0x60, byte(op), 0x00})
			key := slot()
			ibs.SetState(c, &key, *uint256.NewInt(uint64(op + 1)))
			ibs.AddBalance(c, uint256.NewInt(5))
		case k < 80: // self-destruct, sometimes recreated
			ibs.Selfdestruct(a)
			if rng.Intn(3) == 0 {
				ibs.CreateAccount(a, true)
				key := slot()
				ibs.SetState(a, &key, *uint256.NewInt(9))
			}
		case k < 90: // reverted call
			snap := ibs.Snapshot()
			key := slot()
			ibs.SetState(a, &key, *uint256.NewInt(77))
			ibs.AddBalance(pick(), uint256.NewInt(3))
			ibs.Selfdestruct(pick())
			ibs.RevertToSnapshot(snap)
		default: // fee credit
			ibs.AddBalance(addr(0), uint256.NewInt(uint64(rng.Intn(2))))
		}
		if op%7 == 6 {
			if err := ibs.FinalizeTx(rules, NewNoopWriter()); err != nil {
				t.Fatal(err)
			}
		}
	}
	if pf := ibs.AccountPrefetch(); pf != nil {
		seedAccts := map[types.Address]*account.StateAccount{}
		for _, a := range ibs.PendingBalanceIncreases() {
			v := store[a]
			if v != nil {
				c := new(account.StateAccount)
				c.Copy(v)
				v = c
			}
			seedAccts[a] = v
		}
		pf.Seed(seedAccts)
	}
	for k := 0; k < 2; k++ { // the block-end system calls
		if err := ibs.FinalizeTx(rules, NewNoopWriter()); err != nil {
			t.Fatal(err)
		}
	}
	ibs.AddBalance(addr(1), uint256.NewInt(2)) // the reward
	ibs.SetRootPostStateHook(func(p *PostState) { out.post = p })
	ibs.IntermediateRoot()
	out.accts, out.stor = rc.accts, rc.stor
	w := &recordingWriter{}
	if err := ibs.CommitBlock(rules, w); err != nil {
		t.Fatal(err)
	}
	out.rows = w.log
	return out
}

func requireSameFFBlock(t *testing.T, tag string, want, got *ffBlock) {
	t.Helper()
	if len(want.accts) != len(got.accts) {
		t.Fatalf("%s: dirty accounts %d, want %d", tag, len(got.accts), len(want.accts))
	}
	for a, w := range want.accts {
		h, ok := got.accts[a]
		if !ok || (w == nil) != (h == nil) || (w != nil && !w.Equals(h)) {
			t.Fatalf("%s: account %x: %+v, want %+v", tag, a, h, w)
		}
	}
	requireSameStorage(t, tag+" storage", want.stor, got.stor)
	requireSameStorage(t, tag+" post storage", want.post.storage, got.post.storage)
	if len(want.post.accounts) != len(got.post.accounts) {
		t.Fatalf("%s: post accounts %d, want %d", tag, len(got.post.accounts), len(want.post.accounts))
	}
	if fmt.Sprint(sortedAddresses(want.post.wiped)) != fmt.Sprint(sortedAddresses(got.post.wiped)) {
		t.Fatalf("%s: post wiped differ", tag)
	}
	if len(want.post.code) != len(got.post.code) {
		t.Fatalf("%s: post code %d, want %d", tag, len(got.post.code), len(want.post.code))
	}
	for h, c := range want.post.code {
		if string(got.post.code[h]) != string(c) {
			t.Fatalf("%s: post code %x differs", tag, h)
		}
	}
	if fmt.Sprint(want.reads) != fmt.Sprint(got.reads) {
		t.Fatalf("%s: read order differs (%d vs %d reads)", tag, len(got.reads), len(want.reads))
	}
	if fmt.Sprint(want.rows) != fmt.Sprint(got.rows) {
		t.Fatalf("%s: CommitBlock rows differ", tag)
	}
}

func requireSameStorage(t *testing.T, tag string, want, got map[types.Address]map[types.Hash]*uint256.Int) {
	t.Helper()
	if len(want) != len(got) {
		t.Fatalf("%s: %d accounts, want %d", tag, len(got), len(want))
	}
	for a, ws := range want {
		gs, ok := got[a]
		if !ok || len(gs) != len(ws) {
			t.Fatalf("%s: account %x slots %d, want %d", tag, a, len(gs), len(ws))
		}
		keys := make([]types.Hash, 0, len(ws))
		for k := range ws {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
		for _, k := range keys {
			if g := gs[k]; g == nil || !g.Eq(ws[k]) {
				t.Fatalf("%s: %x slot %x: %v, want %v", tag, a, k, g, ws[k])
			}
		}
	}
}

// TestFinalizeFastMatchesSingleLoop: N42_FINALIZE_FAST's parallel dirty
// walk hands the root computer the same dirty set, publishes the same
// post-state, reads the store in the same order and leaves the same
// CommitBlock rows as the single loop, over random blocks with and without
// detached credits, at the walk's threshold lowered to every block and at
// its default.
func TestFinalizeFastMatchesSingleLoop(t *testing.T) {
	prevMin := finalizeFastMin
	defer func() { finalizeFastMin = prevMin }()
	type size struct{ accts, ops int }
	sizes := []size{{60, 200}, {400, 2000}}
	if !testing.Short() {
		sizes = append(sizes, size{9000, 30000})
	}
	for _, sz := range sizes {
		for seed := int64(1); seed <= 6; seed++ {
			for _, detach := range []bool{false, true} {
				tag := fmt.Sprintf("accts=%d seed=%d detach=%v", sz.accts, seed, detach)
				restore := SetFinalizeFastForTest(false)
				want := runFFBlock(t, seed, sz.accts, sz.ops, detach)
				restore()
				for _, min := range []int{1, prevMin} {
					finalizeFastMin = min
					restore := SetFinalizeFastForTest(true)
					got := runFFBlock(t, seed, sz.accts, sz.ops, detach)
					restore()
					requireSameFFBlock(t, fmt.Sprintf("%s min=%d", tag, min), want, got)
				}
				finalizeFastMin = prevMin
			}
		}
	}
}

// BenchmarkRootPrep163k: the root walk's steps up to the post-state hook
// (RootPrepPhases) on the fleet's import shape -- 8,000 senders loaded and
// written, ~23k recipients only credited (pending, detached from a seeded
// prefetch), the two block-end system call finalizations, a reward --
// with N42_FINALIZE_FAST off and on. Each step is reported as ms/op.
func BenchmarkRootPrep163k(b *testing.B) {
	const nSenders, nRecipients, nTxs = 8000, 22857, 163200
	store := map[types.Address]*account.StateAccount{}
	senders := make([]types.Address, nSenders)
	recipients := make([]types.Address, nRecipients)
	for i := range senders {
		senders[i] = testAddr("bench-sender", i)
		a := account.NewAccount()
		a.Balance.SetUint64(1e18)
		store[senders[i]] = &a
	}
	for i := range recipients {
		recipients[i] = testAddr("bench-recipient", i)
		a := account.NewAccount()
		a.Balance.SetUint64(1)
		store[recipients[i]] = &a
	}
	rules := &params.Rules{IsSpuriousDragon: true, IsByzantium: true, IsLondon: true}
	for _, fast := range []bool{false, true} {
		b.Run(fmt.Sprintf("fast=%v", fast), func(b *testing.B) {
			defer SetFinalizeFastForTest(fast)()
			var sum RootPrepPhases
			for it := 0; it < b.N; it++ {
				b.StopTimer()
				pf := NewAccountPrefetch(mapStateReader{accts: store})
				ibs := New(pf)
				ibs.SetAccountPrefetch(pf)
				ibs.SetDetachedPendingCredits(true)
				for i, s := range senders {
					ibs.SetNonce(s, uint64(nTxs/nSenders))
					ibs.SubBalance(s, uint256.NewInt(uint64(21000*(i%7+1))))
				}
				for i := 0; i < nTxs; i++ {
					ibs.AddBalance(recipients[(i*7919)%nRecipients], uint256.NewInt(uint64(1+i%1000)))
				}
				seed := map[types.Address]*account.StateAccount{}
				for _, a := range ibs.PendingBalanceIncreases() {
					v, _ := mapStateReader{accts: store}.ReadAccountData(a)
					seed[a] = v
				}
				pf.Seed(seed)
				for k := 0; k < 2; k++ {
					if err := ibs.FinalizeTx(rules, NewNoopWriter()); err != nil {
						b.Fatal(err)
					}
				}
				ibs.AddBalance(senders[0], uint256.NewInt(2))
				ibs.SetRootComputer(&capturingRootComputer{})
				ibs.SetRootPostStateHook(func(*PostState) {})
				b.StartTimer()
				ibs.IntermediateRoot()
				rp := ibs.LastRootPrepPhases()
				sum.Pending += rp.Pending
				sum.Detach += rp.Detach
				sum.Mat += rp.Mat
				sum.Merge += rp.Merge
				sum.Sort += rp.Sort
				sum.Loop += rp.Loop
				sum.Tail += rp.Tail
				sum.Dirty = rp.Dirty
			}
			n := float64(b.N) * 1e6
			b.ReportMetric(float64(sum.Pending)/n, "pending-ms")
			b.ReportMetric(float64(sum.Detach)/n, "detach-ms")
			b.ReportMetric(float64(sum.Mat)/n, "mat-ms")
			b.ReportMetric(float64(sum.Merge)/n, "merge-ms")
			b.ReportMetric(float64(sum.Sort)/n, "sort-ms")
			b.ReportMetric(float64(sum.Loop)/n, "loop-ms")
			b.ReportMetric(float64(sum.Tail)/n, "tail-ms")
			b.ReportMetric(float64(sum.Dirty), "dirty")
		})
	}
}
