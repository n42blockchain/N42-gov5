// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/params"
)

// hvSetup is an engine, a chain with a parent, and a child header whose
// extra carries a valid vote-domain QC over blockHash at view 1.
type hvSetup struct {
	h      *HotStuff
	chain  *mockChainHeaderReader
	child  *block.Header
	parent *block.Header
	setup  *testSetup
	qc     *QuorumCertificate
}

func newHVSetup(t *testing.T, blockHash types.Hash) *hvSetup {
	t.Helper()
	setup := newTestSetup(t, 4)
	h := New(&params.HotStuffConfig{BaseTimeout: 60000, MaxTimeout: 120000, Period: 3}, nil)
	h.Authorize(setup.validators[0].Address, setup.keys[0])
	if err := h.InitEngine(setup.validators, setup.f); err != nil {
		t.Fatal(err)
	}
	vc := NewVoteCollector(1, blockHash, setup.vs.Len())
	for i := 0; i < 3; i++ {
		if err := vc.AddVote(ValidatorIndex(i), setup.keys[i].Sign(SigningMessage(1, blockHash))); err != nil {
			t.Fatal(err)
		}
	}
	qc, err := vc.BuildQC(setup.vs)
	if err != nil {
		t.Fatal(err)
	}
	qcData, err := encodeQC(qc)
	if err != nil {
		t.Fatal(err)
	}
	extra := make([]byte, extraMinLen)
	copy(extra[:extraMagicLen], extraMagic[:])
	binary.LittleEndian.PutUint64(extra[extraMagicLen:], 1)
	extra = append(extra, qcData...)
	parent := &block.Header{Number: uint256.NewInt(0), Time: 1000}
	child := &block.Header{Number: uint256.NewInt(1), ParentHash: parent.Hash(), Time: 1003, Extra: extra}
	chain := newMockChainReader()
	chain.headers[0] = parent
	return &hvSetup{h: h, chain: chain, child: child, parent: parent, setup: setup, qc: qc}
}

// VerifyHeader stamps its split for the import's line.
func TestVerifyHeaderStampsSplit(t *testing.T) {
	s := newHVSetup(t, types.Hash{0xAB})
	if err := s.h.VerifyHeader(s.chain, s.child, true); err != nil {
		t.Fatal(err)
	}
	sp := consensus.LastHeaderVerify()
	if sp.Number != 1 || sp.QC <= 0 || sp.All < sp.QC+sp.Parent {
		t.Fatalf("split %+v", sp)
	}
}

// N42_IMPORT_HEADER_VERIFY_CACHE: off verifies and records nothing; on
// answers a QC this process verified before from the cache, and only the
// QC part; check verifies every time and catches a cache that lies.
func TestImportHeaderVerifyCache(t *testing.T) {
	t.Run("off", func(t *testing.T) {
		defer SetImportHeaderVerifyCacheForTest("")()
		s := newHVSetup(t, types.Hash{0xA1})
		for i := 0; i < 2; i++ {
			if err := s.h.VerifyHeader(s.chain, s.child, true); err != nil {
				t.Fatal(err)
			}
			if how := consensus.LastHeaderVerify().QCCache; how != "off" {
				t.Fatalf("call %d: %q", i, how)
			}
		}
		if verifiedQCs.m != nil {
			t.Fatal("off: the cache recorded a check")
		}
	})
	t.Run("on", func(t *testing.T) {
		defer SetImportHeaderVerifyCacheForTest("1")()
		s := newHVSetup(t, types.Hash{0xA2})
		ce := s.h.Engine()
		// The consensus path verified the QC (a proposal's justify QC):
		// the import's first header check hits.
		if err := ce.verifyQCWithSet(s.qc, s.setup.vs); err != nil {
			t.Fatal(err)
		}
		if err := s.h.VerifyHeader(s.chain, s.child, true); err != nil {
			t.Fatal(err)
		}
		if how := consensus.LastHeaderVerify().QCCache; how != "hit" {
			t.Fatalf("after the consensus check: %q, want hit", how)
		}
		// A hit skips only the QC: the parent order still runs.
		bad := *s.child
		bad.Time = s.parent.Time
		if err := s.h.VerifyHeader(s.chain, &bad, true); err == nil {
			t.Fatal("a cached QC let a header older than its parent through")
		}
		// Another signature over the same message is a miss and fails.
		forged := *s.qc
		forged.AggregateSignature = bytes.Clone(s.qc.AggregateSignature)
		other := s.setup.keys[3].Sign(SigningMessage(1, types.Hash{0xA2})).Marshal()
		copy(forged.AggregateSignature, other)
		if how, err := ce.verifyHeaderQC(&forged, s.setup.vs); err == nil || how != "miss" {
			t.Fatalf("forged QC: %q, %v", how, err)
		}
		// Failures are not recorded.
		if verifiedQCs.has(&forged, s.setup.vs, ce.voteSigningMessage(1, forged.BlockHash)) {
			t.Fatal("a failed check was recorded")
		}
		// A different validator set is a different key.
		s2 := newHVSetup(t, types.Hash{0xA2})
		if verifiedQCs.has(s.qc, s2.setup.vs, ce.voteSigningMessage(1, s.qc.BlockHash)) {
			t.Fatal("the key does not bind the validator set")
		}
	})
	t.Run("miss then hit", func(t *testing.T) {
		defer SetImportHeaderVerifyCacheForTest("1")()
		s := newHVSetup(t, types.Hash{0xA3})
		want := []string{"miss", "hit"}
		for i, w := range want {
			if err := s.h.VerifyHeader(s.chain, s.child, true); err != nil {
				t.Fatal(err)
			}
			if how := consensus.LastHeaderVerify().QCCache; how != w {
				t.Fatalf("call %d: %q, want %q", i, how, w)
			}
		}
	})
	t.Run("check", func(t *testing.T) {
		defer SetImportHeaderVerifyCacheForTest("check")()
		s := newHVSetup(t, types.Hash{0xA4})
		for i, w := range []string{"check-miss", "check-hit"} {
			if err := s.h.VerifyHeader(s.chain, s.child, true); err != nil {
				t.Fatal(err)
			}
			if how := consensus.LastHeaderVerify().QCCache; how != w {
				t.Fatalf("call %d: %q, want %q", i, how, w)
			}
		}
		// A cache entry for a QC that does not verify: check catches it.
		ce := s.h.Engine()
		forged := *s.qc
		forged.BlockHash = types.Hash{0xEE}
		verifiedQCs.add(&forged, s.setup.vs, ce.voteSigningMessage(forged.View, forged.BlockHash))
		defer func() {
			if recover() == nil {
				t.Fatal("check mode accepted a cached success that fails verification")
			}
		}()
		_, _ = ce.verifyHeaderQC(&forged, s.setup.vs)
	})
}

func TestParseQCCache(t *testing.T) {
	for v, want := range map[string]uint32{"": qcCacheOff, "0": qcCacheOff, "1": qcCacheOn, "on": qcCacheOn, "check": qcCacheCheck} {
		if got := parseQCCache(v); got != want {
			t.Fatalf("%q: %d, want %d", v, got, want)
		}
	}
}
