// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package attest

import (
	"context"
	"math/big"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/frame"
	"github.com/n42blockchain/N42/internal/frame/frametest"
	"github.com/n42blockchain/N42/internal/frame/store"
)

// newKPool is newTestPool with the test-only committee k and sample p.
func newKPool(fl *fleet, self types.Address, st *store.Store, k int, p float64) *Pool {
	pool := newTestPool(fl, self, st)
	pool.cfg.TestCommittee = k
	pool.cfg.VerifySample = p
	if p > 0 {
		pool = NewPool(pool.cfg)
	}
	return pool
}

// hashWithMember returns a random frame hash whose size-k committee has a
// as its first member.
func hashWithMember(t *testing.T, fl *fleet, a types.Address, k int) types.Hash {
	for i := 0; i < 10000; i++ {
		h := randHash(t)
		if fl.members(h, k)[0] == a {
			return h
		}
	}
	t.Fatal("no hash found")
	return types.Hash{}
}

func TestEnvParsing(t *testing.T) {
	t.Setenv(EnvTestCommittee, "")
	t.Setenv(EnvVerifySample, "")
	if TestCommitteeFromEnv() != 0 || VerifySampleFromEnv() != 0 {
		t.Fatal("unset env is not off")
	}
	for in, want := range map[string]int{"1": 1, "2": 2, "0": 0, "-1": 0, "x": 0} {
		t.Setenv(EnvTestCommittee, in)
		if got := TestCommitteeFromEnv(); got != want {
			t.Fatalf("%q: %d want %d", in, got, want)
		}
	}
	for in, want := range map[string]float64{"0.25": 0.25, "1": 1, "7": 1, "-0.5": 0, "NaN": 0, "x": 0} {
		t.Setenv(EnvVerifySample, in)
		if got := VerifySampleFromEnv(); got != want {
			t.Fatalf("%q: %v want %v", in, got, want)
		}
	}
}

// TestTestCommitteeDeterministic: for k = 1, 2, 3 every node of a 7-node
// fleet (f = 2) computes the same k members and quorum k for each frame,
// exactly k nodes consider themselves members, and the other 7-k expect
// attestations.
func TestTestCommitteeDeterministic(t *testing.T) {
	fl := newFleet(t, 7, 2)
	for _, k := range []int{1, 2, 3} {
		pools := make([]*Pool, len(fl.addrs))
		for i, a := range fl.addrs {
			pools[i] = newKPool(fl, a, nil, k, 0)
		}
		load := make([]int, len(fl.addrs))
		for f := 0; f < 700; f++ {
			h := randHash(t)
			ref, need := pools[0].committee(fl.v, h)
			if len(ref) != k || need != k {
				t.Fatalf("k=%d: %d members, need %d", k, len(ref), need)
			}
			in, pending := 0, 0
			for i, p := range pools {
				m, n := p.committee(fl.v, h)
				if n != need || len(m) != len(ref) {
					t.Fatalf("k=%d: node %d disagrees", k, i)
				}
				for j := range m {
					if m[j] != ref[j] {
						t.Fatalf("k=%d: node %d member %d differs", k, i, j)
					}
				}
				if p.InCommittee(h) {
					in++
					load[i]++
				}
				if p.Pending(h) {
					pending++
				}
			}
			if in != k || pending != len(pools)-k {
				t.Fatalf("k=%d: %d members, %d pending", k, in, pending)
			}
		}
		for i, l := range load {
			// Expected 700*k/7 = 100k per node.
			if l < 60*k || l > 140*k {
				t.Fatalf("k=%d: node %d on %d committees", k, i, l)
			}
		}
	}
}

// TestDefaultQuorumUnchanged: without the test committee the quorum is
// f+1 whatever CommitteeSize says, and a larger committee keeps f+1. A
// test committee at or above f+1 only resizes the committee.
func TestDefaultQuorumUnchanged(t *testing.T) {
	fl := newFleet(t, 7, 2)
	h := randHash(t)
	for _, c := range []struct{ size, k, members, need int }{
		{1, 0, 3, 3}, {3, 0, 3, 3}, {5, 0, 5, 3}, {3, 3, 3, 3}, {3, 5, 5, 3}, {3, 9, 7, 3},
	} {
		p := newTestPool(fl, types.Address{}, nil)
		p.cfg.CommitteeSize = c.size
		p.cfg.TestCommittee = c.k
		m, need := p.committee(fl.v, h)
		if len(m) != c.members || need != c.need {
			t.Fatalf("%+v: %d members, need %d", c, len(m), need)
		}
	}
	// The default pool still refuses two of three votes.
	p := newTestPool(fl, fl.outsider(h, 3), nil)
	s := randAddrs(t, 4)
	for _, by := range fl.members(h, 3)[:2] {
		_ = p.Add(fl.batch(by, Entry{Frame: h, Digest: Digest(s), Senders: s}))
	}
	if p.Senders(h, -1) != nil {
		t.Fatal("default pool accepted two votes")
	}
}

// TestTestCommitteeAcceptsAtK: k agreeing attestations are accepted
// (k-1 are not) and the senders reach the store.
func TestTestCommitteeAcceptsAtK(t *testing.T) {
	fl := newFleet(t, 7, 2)
	for _, k := range []int{1, 2} {
		h := randHash(t)
		st := store.New(1 << 20)
		st.Put(h, []byte("frame"))
		p := newKPool(fl, fl.outsider(h, k), st, k, 0)
		senders := randAddrs(t, 6)
		for i, by := range fl.members(h, k) {
			if p.Senders(h, -1) != nil {
				t.Fatalf("k=%d: accepted after %d votes", k, i)
			}
			if err := p.Add(fl.batch(by, Entry{Frame: h, Digest: Digest(senders), Senders: senders})); err != nil {
				t.Fatal(err)
			}
		}
		if got := p.Senders(h, 6); got == nil || got[2] != senders[2] {
			t.Fatalf("k=%d: not accepted at k votes", k)
		}
		if ss := st.Senders(h); ss == nil || ss.Addrs[5] != senders[5] {
			t.Fatalf("k=%d: senders not in the store", k)
		}
	}
}

// TestTestCommitteeLiar documents the safety cost: at k = 1 a single
// lying member's self-consistent forged list is accepted outright; at
// k = 2 one liar against one honest member blocks acceptance (the frame
// falls back to local recovery) but cannot forge.
func TestTestCommitteeLiar(t *testing.T) {
	fl := newFleet(t, 7, 2)
	honest := randAddrs(t, 5)
	forged := append([]types.Address(nil), honest...)
	forged[0] = types.Address{0xbe, 0xef}

	h := randHash(t)
	p := newKPool(fl, fl.outsider(h, 1), nil, 1, 0)
	_ = p.Add(fl.batch(fl.members(h, 1)[0], Entry{Frame: h, Digest: Digest(forged), Senders: forged}))
	if got := p.Senders(h, -1); got == nil || got[0] != forged[0] {
		t.Fatal("k=1: expected the forged list to be accepted (no honest vote is required)")
	}

	h = randHash(t)
	p = newKPool(fl, fl.outsider(h, 2), nil, 2, 0)
	m := fl.members(h, 2)
	_ = p.Add(fl.batch(m[0], Entry{Frame: h, Digest: Digest(forged), Senders: forged}))
	_ = p.Add(fl.batch(m[1], Entry{Frame: h, Digest: Digest(honest), Senders: honest}))
	if p.Senders(h, -1) != nil {
		t.Fatal("k=2: accepted with split votes")
	}
}

// TestVerifySampleCatchesLiar: with every frame sampled, a k = 1 liar's
// forged attestation is never taken, the local recovery convicts it in
// either arrival order, and its later votes are ignored even for frames
// this node does not sample.
func TestVerifySampleCatchesLiar(t *testing.T) {
	fl := newFleet(t, 7, 2)
	liar := fl.addrs[3]
	var self types.Address
	for _, a := range fl.addrs {
		if a != liar {
			self = a
			break
		}
	}
	p := newKPool(fl, self, nil, 1, 1)
	honest := randAddrs(t, 5)
	forged := append([]types.Address(nil), honest...)
	forged[1] = types.Address{0xde, 0xad}

	// Attestation first, then the local recovery.
	h1 := hashWithMember(t, fl, liar, 1)
	if p.Pending(h1) {
		t.Fatal("a sampled frame waits for attestations")
	}
	_ = p.Add(fl.batch(liar, Entry{Frame: h1, Digest: Digest(forged), Senders: forged}))
	if p.Senders(h1, -1) != nil {
		t.Fatal("a sampled frame was accepted from attestations")
	}
	p.Recovered(h1, &store.Senders{Addrs: honest})
	if s := p.Stats(); s.Sampled != 1 || s.SampleMismatch != 1 {
		t.Fatalf("stats %+v", s)
	}

	// An honest attester agreeing with the sample is not convicted.
	h2 := randHash(t)
	member := fl.members(h2, 1)[0]
	p.Recovered(h2, &store.Senders{Addrs: honest})
	if member != liar && member != self {
		_ = p.Add(fl.batch(member, Entry{Frame: h2, Digest: Digest(honest), Senders: honest}))
		if s := p.Stats(); s.SampleMismatch != 1 {
			t.Fatalf("honest attester convicted: %+v", s)
		}
	}

	// The liar is distrusted: with sampling off, its vote on a frame it
	// alone attests no longer counts.
	p.cfg.VerifySample = 0
	h3 := hashWithMember(t, fl, liar, 1)
	_ = p.Add(fl.batch(liar, Entry{Frame: h3, Digest: Digest(honest), Senders: honest}))
	if p.Senders(h3, -1) != nil {
		t.Fatal("a distrusted attester's vote counted")
	}
	if s := p.Stats(); s.DistrustedVotes != 1 {
		t.Fatalf("stats %+v", s)
	}

	// Local recovery first, then the lie.
	q := newKPool(fl, self, nil, 1, 1)
	h4 := hashWithMember(t, fl, liar, 1)
	q.Recovered(h4, &store.Senders{Addrs: honest})
	_ = q.Add(fl.batch(liar, Entry{Frame: h4, Digest: Digest(forged), Senders: forged}))
	if s := q.Stats(); s.SampleMismatch != 1 || q.Senders(h4, -1) != nil {
		t.Fatalf("late lie not caught: %+v", s)
	}
}

// TestVerifySampleFraction: about p of the frames are sampled, and a
// sampled frame is not Pending.
func TestVerifySampleFraction(t *testing.T) {
	fl := newFleet(t, 7, 2)
	p := newKPool(fl, types.Address{1}, nil, 1, 0.25)
	n := 0
	for i := 0; i < 4000; i++ {
		if p.sampledFrame(randHash(t)) {
			n++
		}
	}
	if n < 850 || n > 1150 {
		t.Fatalf("%d of 4000 sampled at p=0.25", n)
	}
}

// TestRecovererWithLiar runs real frames through admission recovery with a
// k = 1 pool whose only committee member lies: without the sample the
// forged senders reach the store (nothing downstream in this package
// re-checks them); with p = 1 the frame is recovered locally, the store
// holds the true senders and the liar is convicted.
func TestRecovererWithLiar(t *testing.T) {
	chainID := big.NewInt(1337)
	keys := frametest.Keys(t, 4, 21)
	fb := frametest.NewBuilder(keys[0], chainID, keys[1:])
	signer := transaction.LatestSignerForChainID(chainID)
	fl := newFleet(t, 7, 2)

	for _, sample := range []float64{0, 1} {
		raw, h := fb.Frame(t, []*transaction.Transaction{fb.Tx(t, 0), fb.Tx(t, 0), fb.Tx(t, 0)})
		truth := make([]types.Address, 3)
		for i := range truth {
			truth[i] = crypto.PubkeyToAddress(keys[1+i].PublicKey)
		}
		liar := fl.members(h, 1)[0]
		forged := append([]types.Address(nil), truth...)
		forged[0] = types.Address{0xbe, 0xef}

		st := store.New(1 << 24)
		pool := newKPool(fl, fl.outsider(h, 1), st, 1, sample)
		rec := frame.NewSenderRecoverer(st, func() transaction.Signer { return signer }, 1)
		rec.SetSource(pool)
		rec.Start(context.Background())
		_ = pool.Add(fl.batch(liar, Entry{Frame: h, Digest: Digest(forged), Senders: forged}))
		st.Put(h, raw)
		deadline := time.Now().Add(5 * time.Second)
		for st.Senders(h) == nil && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		rec.Stop()
		ss := st.Senders(h)
		if ss == nil {
			t.Fatalf("p=%v: no senders", sample)
		}
		if sample == 0 {
			if ss.Addrs[0] != forged[0] {
				t.Fatal("p=0: expected the forged sender in the store")
			}
			continue
		}
		for i := range truth {
			if ss.Addrs[i] != truth[i] {
				t.Fatalf("p=1: sender %d = %s want %s", i, ss.Addrs[i], truth[i])
			}
		}
		if s := pool.Stats(); s.SampleMismatch != 1 {
			t.Fatalf("p=1: liar not convicted: %+v", s)
		}
	}
}
