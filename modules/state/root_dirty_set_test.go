// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

// mapStateReader serves accounts from a map (absent: nil).
type mapStateReader struct {
	accts map[types.Address]*account.StateAccount
}

func (r mapStateReader) ReadAccountData(a types.Address) (*account.StateAccount, error) {
	if v := r.accts[a]; v != nil {
		c := new(account.StateAccount)
		c.Copy(v)
		return c, nil
	}
	return nil, nil
}
func (mapStateReader) ReadAccountStorage(types.Address, *types.Hash) ([]byte, error) { return nil, nil }
func (mapStateReader) ReadAccountCode(types.Address, types.Hash) ([]byte, error)     { return nil, nil }
func (mapStateReader) ReadAccountCodeSize(types.Address, types.Hash) (int, error)    { return 0, nil }

// capturingRootComputer records the dirty set it is handed.
type capturingRootComputer struct {
	accts map[types.Address]*account.StateAccount
	stor  map[types.Address]map[types.Hash]*uint256.Int
}

func (c *capturingRootComputer) ComputeRoot(a map[types.Address]*account.StateAccount, s map[types.Address]map[types.Hash]*uint256.Int) (types.Hash, error) {
	c.accts, c.stor = a, s
	return types.Hash{7}, nil
}

// recordingWriter records CommitBlock's account writes in call order.
type recordingWriter struct{ log []string }

func (w *recordingWriter) UpdateAccountData(a types.Address, o, n *account.StateAccount) error {
	w.log = append(w.log, fmt.Sprintf("upd %x %d/%s -> %d/%s %x", a, o.Nonce, o.Balance.String(), n.Nonce, n.Balance.String(), n.CodeHash))
	return nil
}
func (w *recordingWriter) UpdateAccountCode(a types.Address, h types.Hash, _ []byte) error {
	w.log = append(w.log, fmt.Sprintf("code %x %x", a, h))
	return nil
}
func (w *recordingWriter) DeleteAccount(a types.Address, _ *account.StateAccount) error {
	w.log = append(w.log, fmt.Sprintf("del %x", a))
	return nil
}
func (w *recordingWriter) WriteAccountStorage(a types.Address, k types.Hash, o, v uint256.Int) error {
	w.log = append(w.log, fmt.Sprintf("sto %x %x %s %s", a, k, o.String(), v.String()))
	return nil
}
func (w *recordingWriter) CreateContract(a types.Address) error {
	w.log = append(w.log, fmt.Sprintf("create %x", a))
	return nil
}

func testAddr(tag string, i int) types.Address {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(i))
	h := sha256.Sum256(append([]byte(tag), b[:]...))
	return types.BytesToAddress(h[:20])
}

// A block shaped like a Block-STM import: senders loaded and written,
// thousands of recipients only credited (pending increases), some of them
// absent from the store, one credited with zero, a few not prefetched, one
// credited and then loaded. With detached credits the root must be handed
// exactly the dirty set the materialising walk builds, the published
// post-state must read like CapturePostState's, and CommitBlock must write
// the same rows in the same order.
func TestDetachedPendingCreditsMatchMaterialised(t *testing.T) {
	const nRecipients = 5000
	store := map[types.Address]*account.StateAccount{}
	var senders, recipients []types.Address
	for i := 0; i < 50; i++ {
		a := testAddr("sender", i)
		acc := account.NewAccount()
		acc.Nonce = uint64(i)
		acc.Balance.SetUint64(1_000_000)
		store[a] = &acc
		senders = append(senders, a)
	}
	for i := 0; i < nRecipients; i++ {
		a := testAddr("recipient", i)
		if i%7 != 0 { // every 7th recipient is absent from the store
			acc := account.NewAccount()
			acc.Balance.SetUint64(uint64(i + 1))
			acc.Nonce = uint64(i % 3)
			store[a] = &acc
		}
		recipients = append(recipients, a)
	}

	run := func(detach bool) (*capturingRootComputer, *PostState, []string) {
		pf := NewAccountPrefetch(mapStateReader{accts: store})
		ibs := New(pf)
		ibs.SetAccountPrefetch(pf)
		ibs.SetDetachedPendingCredits(detach)
		// Senders: loaded and written.
		for i, s := range senders {
			ibs.SetNonce(s, uint64(i)+1)
			ibs.SubBalance(s, uint256.NewInt(21000))
		}
		// Recipients: credited without a read.
		for i, r := range recipients {
			amt := uint64(1 + i%1000)
			if i == 11 {
				amt = 0
			}
			ibs.AddBalance(r, uint256.NewInt(amt))
		}
		// The processor's prefetch of the pending credits, minus a few.
		seed := map[types.Address]*account.StateAccount{}
		for _, addr := range ibs.PendingBalanceIncreases() {
			if addr == recipients[3] || addr == recipients[4] {
				continue
			}
			v, _ := mapStateReader{accts: store}.ReadAccountData(addr)
			seed[addr] = v
		}
		pf.Seed(seed)
		// A credited recipient read later (block end).
		_ = ibs.GetBalance(recipients[5])
		rc := &capturingRootComputer{}
		ibs.SetRootComputer(rc)
		var post *PostState
		if detach {
			// The root-built snapshot is the one under test.
			ibs.SetRootPostStateHook(func(p *PostState) { post = p })
		} else {
			ibs.SetBeforeRootHook(func() { post = CapturePostState(ibs) })
		}
		ibs.IntermediateRoot()
		if loaded := len(ibs.stateObjects); detach && loaded > len(senders)+10 {
			t.Fatalf("detached root materialised %d objects", loaded)
		} else if !detach && loaded < nRecipients {
			t.Fatalf("materialising root loaded only %d objects", loaded)
		}
		w := &recordingWriter{}
		if err := ibs.CommitBlock(&params.Rules{IsSpuriousDragon: true}, w); err != nil {
			t.Fatal(err)
		}
		return rc, post, w.log
	}

	ref, refPost, refLog := run(false)
	got, gotPost, gotLog := run(true)
	if len(got.accts) != len(ref.accts) || len(got.accts) < nRecipients {
		t.Fatalf("dirty accounts %d, want %d", len(got.accts), len(ref.accts))
	}
	for addr, want := range ref.accts {
		have, ok := got.accts[addr]
		if !ok {
			t.Fatalf("account %x missing from the detached dirty set", addr)
		}
		if (want == nil) != (have == nil) || (want != nil && !want.Equals(have)) {
			t.Fatalf("account %x: detached %+v, materialised %+v", addr, have, want)
		}
	}
	if len(got.stor) != len(ref.stor) {
		t.Fatalf("storage accounts %d, want %d", len(got.stor), len(ref.stor))
	}
	if len(gotLog) != len(refLog) {
		t.Fatalf("CommitBlock wrote %d rows, want %d", len(gotLog), len(refLog))
	}
	for i := range refLog {
		if gotLog[i] != refLog[i] {
			t.Fatalf("CommitBlock row %d: %s, want %s", i, gotLog[i], refLog[i])
		}
	}
	base := mapStateReader{accts: store}
	refR, gotR := NewPostStateReader(refPost, base), NewPostStateReader(gotPost, base)
	for _, addr := range append(append([]types.Address{}, senders...), recipients...) {
		w, _ := refR.ReadAccountData(addr)
		h, _ := gotR.ReadAccountData(addr)
		if (w == nil) != (h == nil) || (w != nil && !w.Equals(h)) {
			t.Fatalf("post-state read of %x: %+v, want %+v", addr, h, w)
		}
	}
}

// TestDetachedCreditsSurviveSystemCallFinalize (P10): the block-end system
// calls finalize with a no-op writer. On a state whose root detaches its
// pending credits they stay pending, and the root, the post-state and
// CommitBlock's rows equal those of a state that materialised every credit
// at the system call (EIP-161 removal of a zero credit to an absent account
// included: an empty account and a deletion are the same root op). The
// credits' base also comes from a known-accounts cache instead of a seeded
// set.
func TestDetachedCreditsSurviveSystemCallFinalize(t *testing.T) {
	const nRecipients = 3000
	store := map[types.Address]*account.StateAccount{}
	var senders, recipients []types.Address
	for i := 0; i < 40; i++ {
		a := testAddr("sc-sender", i)
		acc := account.NewAccount()
		acc.Nonce = uint64(i)
		acc.Balance.SetUint64(1_000_000)
		store[a] = &acc
		senders = append(senders, a)
	}
	for i := 0; i < nRecipients; i++ {
		a := testAddr("sc-recipient", i)
		if i%7 != 0 {
			acc := account.NewAccount()
			acc.Balance.SetUint64(uint64(i + 1))
			store[a] = &acc
		}
		recipients = append(recipients, a)
	}
	rules := &params.Rules{IsSpuriousDragon: true, IsByzantium: true}
	run := func(detach, known bool) (*capturingRootComputer, *PostState, []string) {
		pf := NewAccountPrefetch(mapStateReader{accts: store})
		ibs := New(pf)
		ibs.SetAccountPrefetch(pf)
		ibs.SetDetachedPendingCredits(detach)
		for i, s := range senders {
			ibs.SetNonce(s, uint64(i)+1)
			ibs.SubBalance(s, uint256.NewInt(21000))
		}
		for i, r := range recipients {
			amt := uint64(1 + i%1000)
			if i == 11 || i == 14 { // 14 is absent: a zero credit touches an empty account
				amt = 0
			}
			ibs.AddBalance(r, uint256.NewInt(amt))
		}
		if known {
			pf.SetKnown(mapKnown{accts: store})
		} else {
			seed := map[types.Address]*account.StateAccount{}
			for _, addr := range ibs.PendingBalanceIncreases() {
				v, _ := mapStateReader{accts: store}.ReadAccountData(addr)
				seed[addr] = v
			}
			pf.Seed(seed)
		}
		// The two Prague system calls' finalization.
		for k := 0; k < 2; k++ {
			if err := ibs.FinalizeTx(rules, NewNoopWriter()); err != nil {
				t.Fatal(err)
			}
		}
		if pending := len(ibs.pendingIncreaseAddrs()); detach && pending < nRecipients-2 {
			t.Fatalf("system call materialised the credits (%d pending)", pending)
		}
		rc := &capturingRootComputer{}
		ibs.SetRootComputer(rc)
		var post *PostState
		ibs.SetRootPostStateHook(func(p *PostState) { post = p })
		ibs.IntermediateRoot()
		w := &recordingWriter{}
		if err := ibs.CommitBlock(rules, w); err != nil {
			t.Fatal(err)
		}
		return rc, post, w.log
	}
	empty := func(a *account.StateAccount) bool { return a == nil || accountIsEmpty(a) }
	ref, refPost, refLog := run(false, false)
	for _, known := range []bool{false, true} {
		got, gotPost, gotLog := run(true, known)
		if len(got.accts) != len(ref.accts) {
			t.Fatalf("known=%v: dirty accounts %d, want %d", known, len(got.accts), len(ref.accts))
		}
		for addr, want := range ref.accts {
			have, ok := got.accts[addr]
			if !ok {
				t.Fatalf("known=%v: account %x missing", known, addr)
			}
			if empty(want) != empty(have) || (!empty(want) && !want.Equals(have)) {
				t.Fatalf("known=%v: account %x: %+v, want %+v", known, addr, have, want)
			}
		}
		if len(gotLog) != len(refLog) {
			t.Fatalf("known=%v: CommitBlock wrote %d rows, want %d", known, len(gotLog), len(refLog))
		}
		for i := range refLog {
			if gotLog[i] != refLog[i] {
				t.Fatalf("known=%v: CommitBlock row %d: %s, want %s", known, i, gotLog[i], refLog[i])
			}
		}
		base := mapStateReader{accts: store}
		refR, gotR := NewPostStateReader(refPost, base), NewPostStateReader(gotPost, base)
		for _, addr := range append(append([]types.Address{}, senders...), recipients...) {
			w, _ := refR.ReadAccountData(addr)
			h, _ := gotR.ReadAccountData(addr)
			if empty(w) != empty(h) || (!empty(w) && !w.Equals(h)) {
				t.Fatalf("known=%v: post-state read of %x: %+v, want %+v", known, addr, h, w)
			}
		}
	}
}

// mapKnown is a KnownAccounts over a map.
type mapKnown struct {
	accts map[types.Address]*account.StateAccount
}

func (k mapKnown) Peek(a types.Address) (*account.StateAccount, bool) {
	return k.accts[a], true
}

func (k mapKnown) Get(a types.Address) (*account.StateAccount, bool) {
	v, ok := k.Peek(a)
	if v == nil {
		return nil, ok
	}
	c := new(account.StateAccount)
	c.Copy(v)
	return c, ok
}
