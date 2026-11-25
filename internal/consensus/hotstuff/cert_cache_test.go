// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

// pairingsOf runs f and returns the pairing checks it performed.
func pairingsOf(f func()) uint64 {
	before := PairingChecks()
	f()
	return PairingChecks() - before
}

func TestCertCacheQCHit(t *testing.T) {
	setup := newTestSetup(t, 7)
	e, _ := newTestEngine(t, setup, 0)
	q := setup.vs.QuorumSize()
	h := types.Hash{0xaa}
	qc := buildSignedQC(setup.keys, 9, h, e.voteSigningMessage(9, h), q, 7)

	if n := pairingsOf(func() {
		if err := e.verifyEmbeddedQC(&qc); err != nil {
			t.Fatalf("first verify: %v", err)
		}
	}); n != 1 {
		t.Fatalf("first verify ran %d pairing checks, want 1", n)
	}
	for i := 0; i < 5; i++ {
		c := qc.Clone()
		if n := pairingsOf(func() {
			if err := e.verifyEmbeddedQC(&c); err != nil {
				t.Fatalf("cached verify: %v", err)
			}
		}); n != 0 {
			t.Fatalf("cached verify ran %d pairing checks, want 0", n)
		}
	}
}

// A commit-domain QC fails the vote-domain check first; once it verified,
// the any-domain path must not pay that failed check again.
func TestCertCacheAnyDomainCommitQC(t *testing.T) {
	setup := newTestSetup(t, 7)
	e, _ := newTestEngine(t, setup, 0)
	h := types.Hash{0xbb}
	qc := buildSignedQC(setup.keys, 11, h, e.commitSigningMessage(11, h), setup.vs.QuorumSize(), 7)
	if n := pairingsOf(func() {
		if err := e.verifyEmbeddedQC(&qc); err != nil {
			t.Fatalf("verify: %v", err)
		}
	}); n != 2 {
		t.Fatalf("first any-domain verify ran %d pairing checks, want 2", n)
	}
	if n := pairingsOf(func() {
		if err := e.verifyEmbeddedQC(&qc); err != nil {
			t.Fatalf("verify: %v", err)
		}
	}); n != 0 {
		t.Fatalf("cached any-domain verify ran %d pairing checks, want 0", n)
	}
}

// Invalid certificates are never cached, and a forgery that shares view,
// block hash and bitmap with a cached valid QC does not hit its entry.
func TestCertCacheInvalidNotCached(t *testing.T) {
	setup := newTestSetup(t, 7)
	e, _ := newTestEngine(t, setup, 0)
	q := setup.vs.QuorumSize()
	h := types.Hash{0xcc}
	valid := buildSignedQC(setup.keys, 5, h, e.voteSigningMessage(5, h), q, 7)
	// Same view, hash and bitmap, but signed over another block.
	forged := buildSignedQC(setup.keys, 5, h, e.voteSigningMessage(5, types.Hash{0xdd}), q, 7)

	for i := 0; i < 2; i++ {
		if n := pairingsOf(func() {
			if err := e.verifyQC(&forged); err == nil {
				t.Fatal("forged QC verified")
			}
		}); n != 1 {
			t.Fatalf("invalid verify %d ran %d pairing checks, want 1 (no negative or positive caching)", i, n)
		}
	}
	if err := e.verifyQC(&valid); err != nil {
		t.Fatalf("valid QC after a failed forgery: %v", err)
	}
	if n := pairingsOf(func() {
		if err := e.verifyQC(&forged); err == nil {
			t.Fatal("forged QC verified after the valid one was cached")
		}
	}); n != 1 {
		t.Fatalf("forged verify after caching ran %d pairing checks, want 1", n)
	}
	if e.certCache.len() != 1 {
		t.Fatalf("cache holds %d entries, want 1 (only the valid QC)", e.certCache.len())
	}
}

func TestCertCacheEviction(t *testing.T) {
	setup := newTestSetup(t, 4)
	e, _ := newTestEngine(t, setup, 0)
	e.certCache = newVerifiedCertCache(2)
	q := setup.vs.QuorumSize()
	qcs := make([]QuorumCertificate, 3)
	for i := range qcs {
		h := types.Hash{byte(i + 1)}
		v := ViewNumber(20 + i)
		qcs[i] = buildSignedQC(setup.keys, v, h, e.voteSigningMessage(v, h), q, 4)
		if err := e.verifyQC(&qcs[i]); err != nil {
			t.Fatal(err)
		}
	}
	if e.certCache.len() != 2 {
		t.Fatalf("cache len %d, want 2", e.certCache.len())
	}
	if n := pairingsOf(func() { _ = e.verifyQC(&qcs[2]) }); n != 0 {
		t.Fatalf("most recent QC not cached (%d pairings)", n)
	}
	if n := pairingsOf(func() {
		if err := e.verifyQC(&qcs[0]); err != nil {
			t.Fatal(err)
		}
	}); n != 1 {
		t.Fatalf("evicted QC re-verified with %d pairings, want 1", n)
	}
}

func TestCertCacheTCHitAndForgery(t *testing.T) {
	setup := newTestSetup(t, 7)
	e, _ := newTestEngine(t, setup, 0)
	q := setup.vs.QuorumSize()
	tc := buildSignedTC(setup.keys, 30, e.timeoutSigningMessage(30), q, 7)
	if n := pairingsOf(func() {
		if err := e.verifyTC(&tc); err != nil {
			t.Fatal(err)
		}
	}); n != 1 {
		t.Fatalf("first TC verify %d pairings, want 1", n)
	}
	if n := pairingsOf(func() {
		if err := e.verifyTCWithSet(&tc, setup.vs); err != nil {
			t.Fatal(err)
		}
	}); n != 0 {
		t.Fatalf("cached TC verify %d pairings, want 0", n)
	}
	bad := tc
	bad.AggregateSignature = buildSignedTC(setup.keys, 31, e.timeoutSigningMessage(31), q, 7).AggregateSignature
	if err := e.verifyTC(&bad); err == nil {
		t.Fatal("TC with another view's signature verified")
	}
	// A different bitmap is a different key, even with the same signature.
	other := tc
	other.Signers = append([]bool(nil), tc.Signers...)
	other.Signers[q-1], other.Signers[q] = false, true
	if err := e.verifyTC(&other); err == nil {
		t.Fatal("TC with a changed bitmap verified")
	}
}
