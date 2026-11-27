// Copyright 2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"errors"
	"testing"

	"github.com/n42blockchain/N42/common/block"
)

// p1PrewarmChain builds headers 1..n where every header from 2 on embeds
// the CommitQC of its parent, stored in the fixture's chain reader.
func p1PrewarmChain(f *p1Fixture, n int) []*block.Header {
	out := f.chainOf(1)
	for i := 2; i <= n; i++ {
		qc := f.commitQC(ViewNumber(i-1), out[i-1].Hash(), []int{0, 1, 2}, true)
		h := f.seal(f.child(out[i-1], ViewNumber(i), qc))
		f.chain.headers[uint64(i)] = h
		out = append(out, h)
	}
	return out
}

func asIHeaders(hs []*block.Header) []block.IHeader {
	out := make([]block.IHeader, len(hs))
	for i, h := range hs {
		out[i] = h
	}
	return out
}

// After PrewarmHeaders, VerifyHeader of the same headers runs no pairing
// and still accepts them.
func TestPrewarmHeadersCachesSignatures(t *testing.T) {
	f := newP1Fixture(t)
	hs := p1PrewarmChain(f, 6)[1:]
	f.hs.PrewarmHeaders(f.chain, asIHeaders(hs))

	before := PairingChecks()
	for _, h := range hs {
		if err := f.hs.VerifyHeader(f.chain, h, true); err != nil {
			t.Fatalf("header %d refused after prewarm: %v", h.Number.Uint64(), err)
		}
	}
	if got := PairingChecks() - before; got != 0 {
		t.Fatalf("VerifyHeader ran %d pairings after prewarm, want 0", got)
	}
}

// The cache holds only what verified: a forged seal or QC still fails
// VerifyHeader after a prewarm of the bad header.
func TestPrewarmHeadersDecidesNothing(t *testing.T) {
	f := newP1Fixture(t)
	hs := p1PrewarmChain(f, 3)

	// QC signed in the vote domain instead of the commit domain.
	badQC := f.commitQC(2, hs[2].Hash(), []int{0, 1, 2}, false)
	h3qc := f.seal(f.child(hs[2], 3, badQC))
	// Seal by a validator that does not lead view 3.
	goodQC := f.commitQC(2, hs[2].Hash(), []int{0, 1, 2}, true)
	h3seal := f.sealBy(f.child(hs[2], 3, goodQC), (f.leader(3)+1)%len(f.sks))

	f.hs.PrewarmHeaders(f.chain, []block.IHeader{h3qc, h3seal})
	if err := f.hs.VerifyHeader(f.chain, h3qc, true); !errors.Is(err, ErrProfile1QC) {
		t.Fatalf("vote-domain QC after prewarm: err %v, want ErrProfile1QC", err)
	}
	if err := f.hs.VerifyHeader(f.chain, h3seal, true); !errors.Is(err, ErrProfile1Seal) {
		t.Fatalf("foreign seal after prewarm: err %v, want ErrProfile1Seal", err)
	}
	// The good header with the same QC is still accepted.
	h3 := f.seal(f.child(hs[2], 3, goodQC))
	if err := f.hs.VerifyHeader(f.chain, h3, true); err != nil {
		t.Fatalf("valid header refused: %v", err)
	}
}

// A QC whose signer bitmap differs from the verified one is not served
// from the cache.
func TestP1QCKeyBindsSigners(t *testing.T) {
	f := newP1Fixture(t)
	hs := f.chainOf(2)
	a := f.commitQC(2, hs[2].Hash(), []int{0, 1, 2}, true)
	b := *a
	b.Signers = []bool{true, true, true, true}
	msg := []byte("m")
	if p1QCKey(a, msg) == p1QCKey(&b, msg) {
		t.Fatal("different signer bitmaps share a cache key")
	}
	if p1QCKey(a, msg) == p1QCKey(a, []byte("n")) {
		t.Fatal("different messages share a cache key")
	}
}
