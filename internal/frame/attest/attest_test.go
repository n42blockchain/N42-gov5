// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package attest

import (
	"crypto/rand"
	"math/big"
	"testing"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/crypto/bls"
	blscommon "github.com/n42blockchain/N42/crypto/bls/common"
	"github.com/n42blockchain/N42/internal/frame/store"
)

func randHash(t *testing.T) types.Hash {
	var h types.Hash
	if _, err := rand.Read(h[:]); err != nil {
		t.Fatal(err)
	}
	return h
}

func randAddrs(t *testing.T, n int) []types.Address {
	out := make([]types.Address, n)
	for i := range out {
		if _, err := rand.Read(out[i][:]); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

// TestCommitteeDeterministic: the committee is h mod n and the next size-1
// indexes, the same on every call, distinct, and spread evenly.
func TestCommitteeDeterministic(t *testing.T) {
	const n, size, frames = 7, 3, 7000
	load := make([]int, n)
	for i := 0; i < frames; i++ {
		h := randHash(t)
		c := Committee(h, n, size)
		again := Committee(h, n, size)
		start := int(new(big.Int).Mod(new(big.Int).SetBytes(h[:]), big.NewInt(n)).Int64())
		if len(c) != size {
			t.Fatalf("committee of %d", len(c))
		}
		seen := map[int]bool{}
		for k, idx := range c {
			if idx != again[k] || idx != (start+k)%n {
				t.Fatalf("committee %v, again %v, start %d", c, again, start)
			}
			if seen[idx] {
				t.Fatalf("duplicate member in %v", c)
			}
			seen[idx] = true
			load[idx]++
		}
	}
	for i, l := range load {
		// Expected 3000 each; a fair hash stays well within 10%.
		if l < 2700 || l > 3300 {
			t.Fatalf("validator %d on %d committees of %d frames: %v", i, l, frames, load)
		}
	}
	if c := Committee(types.Hash{}, 2, 3); len(c) != 2 {
		t.Fatalf("committee larger than the set: %v", c)
	}
	// The order a set is given in does not matter: it is sorted first.
	set := randAddrs(t, n)
	rev := append([]types.Address(nil), set...)
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	a, b := SortAddresses(set), SortAddresses(rev)
	for i := range a {
		if a[i] != b[i] {
			t.Fatal("sorted sets differ")
		}
	}
}

func TestBatchRoundTrip(t *testing.T) {
	b := &Batch{Attester: types.Address{7}, Sig: []byte{1, 2, 3}, Entries: []Entry{
		{Frame: types.Hash{1}, Digest: types.Hash{2}, Senders: randAddrs(t, 500)},
		{Frame: types.Hash{3}, Digest: types.Hash{4}},
	}}
	got, err := DecodeBody(EncodeBody(b))
	if err != nil {
		t.Fatal(err)
	}
	if got.Attester != b.Attester || string(got.Sig) != string(b.Sig) || len(got.Entries) != 2 ||
		got.Entries[0].Frame != b.Entries[0].Frame || len(got.Entries[0].Senders) != 500 ||
		got.Entries[0].Senders[499] != b.Entries[0].Senders[499] || got.Entries[1].Senders != nil {
		t.Fatalf("round trip: %+v", got)
	}
	if _, err := DecodeBody(EncodeBody(b)[:40]); err == nil {
		t.Fatal("truncated batch decoded")
	}
}

// Fleet is a validator set with BLS keys for tests.
type fleet struct {
	addrs []types.Address // sorted
	keys  map[types.Address]blscommon.SecretKey
	v     *Validators
}

func newFleet(t *testing.T, n, f int) *fleet {
	fl := &fleet{keys: map[types.Address]blscommon.SecretKey{}}
	for _, a := range randAddrs(t, n) {
		k, err := bls.RandKey()
		if err != nil {
			t.Fatal(err)
		}
		fl.keys[a] = k
		fl.addrs = append(fl.addrs, a)
	}
	fl.addrs = SortAddresses(fl.addrs)
	fl.v = &Validators{Sorted: fl.addrs, F: f, Verify: func(a types.Address, msg, sig []byte) bool {
		k, ok := fl.keys[a]
		if !ok {
			return false
		}
		s, err := bls.SignatureFromBytes(sig)
		return err == nil && s.Verify(k.PublicKey(), msg)
	}}
	return fl
}

func (fl *fleet) batch(by types.Address, entries ...Entry) *Batch {
	msg := SigningHash(by, entries)
	return &Batch{Attester: by, Entries: entries, Sig: fl.keys[by].Sign(msg[:]).Marshal()}
}

func (fl *fleet) members(h types.Hash, size int) []types.Address {
	var out []types.Address
	for _, i := range Committee(h, len(fl.addrs), size) {
		out = append(out, fl.addrs[i])
	}
	return out
}

func (fl *fleet) outsider(h types.Hash, size int) types.Address {
	in := map[types.Address]bool{}
	for _, m := range fl.members(h, size) {
		in[m] = true
	}
	for _, a := range fl.addrs {
		if !in[a] {
			return a
		}
	}
	return types.Address{}
}

func newTestPool(fl *fleet, self types.Address, st *store.Store) *Pool {
	signer := transaction.LatestSignerForChainID(big.NewInt(1337))
	return NewPool(Config{Validators: func() *Validators { return fl.v }, Self: func() types.Address { return self },
		Signer: func() transaction.Signer { return signer }, Store: st, CommitteeSize: 3})
}

// TestPoolAcceptsAtFPlusOne: with f = 2 the three committee members must
// all agree; two are not enough, the third completes it and the senders
// reach the store.
func TestPoolAcceptsAtFPlusOne(t *testing.T) {
	fl := newFleet(t, 7, 2)
	h := randHash(t)
	st := store.New(1 << 20)
	st.Put(h, []byte("frame"))
	p := newTestPool(fl, fl.outsider(h, 3), st)
	if !p.Pending(h) {
		t.Fatal("a non-member does not expect attestations")
	}
	senders := randAddrs(t, 10)
	e := Entry{Frame: h, Digest: Digest(senders), Senders: senders}
	m := fl.members(h, 3)
	for i, by := range m {
		// Only the first carries the list; the others attest the digest.
		ee := e
		if i > 0 {
			ee.Senders = nil
		}
		if err := p.Add(fl.batch(by, ee)); err != nil {
			t.Fatal(err)
		}
		// A repeated attestation of one member never counts twice.
		if err := p.Add(fl.batch(by, ee)); err != nil {
			t.Fatal(err)
		}
		got := p.Senders(h, len(senders))
		if (i < 2) != (got == nil) {
			t.Fatalf("after %d attestations: senders %v", i+1, got != nil)
		}
	}
	if ss := st.Senders(h); ss == nil || len(ss.Addrs) != 10 || ss.Addrs[3] != senders[3] {
		t.Fatal("accepted senders did not reach the store")
	}
	if p.Senders(h, 9) != nil {
		t.Fatal("senders of the wrong count were returned")
	}
}

// TestPoolRejects: a list that does not hash to its digest, a
// non-member's attestation, a bad signature and a non-validator are all
// dropped, and a byzantine member's self-consistent wrong list cannot be
// accepted: f+1 = 3 votes need every member.
func TestPoolRejects(t *testing.T) {
	fl := newFleet(t, 7, 2)
	h := randHash(t)
	p := newTestPool(fl, fl.outsider(h, 3), nil)
	m := fl.members(h, 3)
	honest := randAddrs(t, 8)
	forged := append([]types.Address(nil), honest...)
	forged[0] = types.Address{0xbe, 0xef}

	// Mismatching digest: honest digest, forged list.
	if err := p.Add(fl.batch(m[0], Entry{Frame: h, Digest: Digest(honest), Senders: forged})); err != nil {
		t.Fatal(err)
	}
	if s := p.Stats(); s.BadDigest != 1 {
		t.Fatalf("digest mismatch not counted: %+v", s)
	}
	// Non-member.
	if err := p.Add(fl.batch(fl.outsider(h, 3), Entry{Frame: h, Digest: Digest(honest), Senders: honest})); err != nil {
		t.Fatal(err)
	}
	if s := p.Stats(); s.NotMember != 1 {
		t.Fatalf("non-member not counted: %+v", s)
	}
	// Bad signature: signed by another key.
	b := fl.batch(m[1], Entry{Frame: h, Digest: Digest(honest), Senders: honest})
	b.Sig = fl.batch(m[2], Entry{Frame: h, Digest: Digest(honest), Senders: honest}).Sig
	if err := p.Add(b); err != ErrBadSignature {
		t.Fatalf("bad signature: %v", err)
	}
	// Not a validator.
	if err := p.Add(&Batch{Attester: types.Address{1}}); err != ErrNotValidator {
		t.Fatalf("non-validator: %v", err)
	}
	// One byzantine member with a consistent forged list, two honest.
	_ = p.Add(fl.batch(m[0], Entry{Frame: h, Digest: Digest(forged), Senders: forged}))
	_ = p.Add(fl.batch(m[1], Entry{Frame: h, Digest: Digest(honest), Senders: honest}))
	_ = p.Add(fl.batch(m[2], Entry{Frame: h, Digest: Digest(honest), Senders: honest}))
	if p.Senders(h, -1) != nil {
		t.Fatal("accepted without f+1 matching attestations")
	}
	if s := p.Stats(); s.Accepted != 0 {
		t.Fatalf("accepted: %+v", s)
	}
}

// TestPoolPublishes: a member queues its own recoveries and Flush sends
// one signed batch another pool accepts as one of the votes.
func TestPoolPublishes(t *testing.T) {
	fl := newFleet(t, 7, 2)
	h := randHash(t)
	m := fl.members(h, 3)
	var sent [][]byte
	pub := NewPool(Config{Validators: func() *Validators { return fl.v }, Self: func() types.Address { return m[1] },
		Sign:          func(msg []byte) ([]byte, error) { return fl.keys[m[1]].Sign(msg).Marshal(), nil },
		Send:          func(msg []byte) { sent = append(sent, msg) },
		CommitteeSize: 3})
	if pub.Pending(h) {
		t.Fatal("a member waits for attestations of its own frame")
	}
	senders := randAddrs(t, 5)
	pub.Recovered(h, &store.Senders{Addrs: senders})
	pub.Recovered(randHashNotIn(t, fl, m[1]), &store.Senders{Addrs: senders}) // not a member: a fallback
	pub.Flush()
	if len(sent) != 1 {
		t.Fatalf("%d messages sent", len(sent))
	}
	if s := pub.Stats(); s.Published != 1 || s.Fallback != 1 {
		t.Fatalf("stats %+v", s)
	}
	rx := newTestPool(fl, fl.outsider(h, 3), nil)
	rx.HandleWire(sent[0][8:])
	_ = rx.Add(fl.batch(m[0], Entry{Frame: h, Digest: Digest(senders)}))
	_ = rx.Add(fl.batch(m[2], Entry{Frame: h, Digest: Digest(senders)}))
	if got := rx.Senders(h, 5); got == nil || got[4] != senders[4] {
		t.Fatal("published attestation not accepted")
	}
}

func randHashNotIn(t *testing.T, fl *fleet, a types.Address) types.Hash {
	for {
		h := randHash(t)
		in := false
		for _, m := range fl.members(h, 3) {
			in = in || m == a
		}
		if !in {
			return h
		}
	}
}

// BenchmarkBatchVerify is the receiver's cost of one batch: decode, one
// BLS verification, the digest of every carried list.
func BenchmarkBatchVerify(b *testing.B) {
	t := &testing.T{}
	fl := newFleet(t, 7, 2)
	var es []Entry
	for i := 0; i < 32; i++ {
		s := randAddrs(t, 500)
		es = append(es, Entry{Frame: randHash(t), Digest: Digest(s), Senders: s})
	}
	by := fl.addrs[0]
	body := EncodeBody(fl.batch(by, es...))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		bt, err := DecodeBody(body)
		if err != nil {
			b.Fatal(err)
		}
		msg := SigningHash(bt.Attester, bt.Entries)
		if !fl.v.Verify(bt.Attester, msg[:], bt.Sig) {
			b.Fatal("verify")
		}
		for _, e := range bt.Entries {
			if Digest(e.Senders) != e.Digest {
				b.Fatal("digest")
			}
		}
	}
}

// TestDigestIsConcatenatedSenders: Digest is keccak256 of the senders'
// 20-byte concatenation, for no, one and many senders.
func TestDigestIsConcatenatedSenders(t *testing.T) {
	for _, n := range []int{0, 1, 2, 500} {
		addrs := make([]types.Address, n)
		for i := range addrs {
			addrs[i] = types.Address{byte(i), byte(i >> 8), 7, byte(n)}
			addrs[i][19] = byte(i * 3)
		}
		var buf []byte
		for _, a := range addrs {
			buf = append(buf, a[:]...)
		}
		if got, want := Digest(addrs), crypto.Keccak256Hash(buf); got != want {
			t.Fatalf("%d senders: %x, want %x", n, got, want)
		}
	}
}
// TestDecodeBodyAliasesAndPoolCopies: DecodeBody's lists view the wire
// buffer (and hash to the same digest as a copied list), and the pool keeps
// its own copy, so a buffer reused after Add cannot change accepted senders.
func TestDecodeBodyAliasesAndPoolCopies(t *testing.T) {
	fl := newFleet(t, 7, 2)
	h := randHash(t)
	senders := randAddrs(t, 40)
	want := append([]types.Address(nil), senders...)
	st := store.New(1 << 20)
	st.Put(h, []byte{1})
	p := newTestPool(fl, types.Address{}, st)
	for _, m := range fl.members(h, 3) {
		body := EncodeBody(fl.batch(m, Entry{Frame: h, Digest: Digest(senders), Senders: senders}))
		b, err := DecodeBody(body)
		if err != nil {
			t.Fatal(err)
		}
		if Digest(b.Entries[0].Senders) != Digest(want) {
			t.Fatal("aliased list digest differs")
		}
		if err := p.Add(b); err != nil {
			t.Fatal(err)
		}
		for i := range body {
			body[i] ^= 0xff // the wire buffer is reused for something else
		}
	}
	got := p.Senders(h, len(want))
	if got == nil || st.Senders(h) == nil {
		t.Fatal("not accepted")
	}
	for i := range want {
		if got[i] != want[i] || st.Senders(h).Addrs[i] != want[i] {
			t.Fatalf("sender %d changed with the wire buffer", i)
		}
	}
}

// TestPoolMemberKnowsOwnList: a committee member's own recovery makes its
// list known, so the other members' attestations of the same digest count
// without their lists being hashed, and a list that does not match the
// digest it is sent with is still refused.
func TestPoolMemberKnowsOwnList(t *testing.T) {
	fl := newFleet(t, 7, 2)
	h := randHash(t)
	members := fl.members(h, 3)
	senders := randAddrs(t, 30)
	st := store.New(1 << 20)
	st.Put(h, []byte{1})
	p := newTestPool(fl, members[0], st)
	p.cfg.Sign = func([]byte) ([]byte, error) { return nil, nil }
	p.cfg.Send = func([]byte) {}
	p.Recovered(h, &store.Senders{Addrs: senders})
	if !p.hasList(h, Digest(senders)) {
		t.Fatal("own list not known")
	}
	// A forged list under the right digest from a member: its list is not
	// used (the known one is), and its vote counts for the digest it signed.
	forged := randAddrs(t, 30)
	if err := p.Add(fl.batch(members[1], Entry{Frame: h, Digest: Digest(senders), Senders: forged})); err != nil {
		t.Fatal(err)
	}
	// The same forged list under its own digest is a different digest: it
	// gathers its own votes and never the known list.
	if err := p.Add(fl.batch(members[2], Entry{Frame: h, Digest: Digest(forged), Senders: senders})); err != nil {
		t.Fatal(err)
	}
	if p.Stats().BadDigest != 1 {
		t.Fatalf("bad digests %d", p.Stats().BadDigest)
	}
	if p.Senders(h, -1) != nil {
		t.Fatal("accepted on two votes")
	}
	if err := p.Add(fl.batch(members[2], Entry{Frame: h, Digest: Digest(senders), Senders: senders})); err != nil {
		t.Fatal(err)
	}
	if p.Senders(h, -1) != nil {
		t.Fatal("accepted without f+1 remote votes")
	}
}
