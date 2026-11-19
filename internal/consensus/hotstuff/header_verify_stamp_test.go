// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
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
