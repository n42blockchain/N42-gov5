// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"math/big"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

// TestVerifyHeaderGasUsedBoundFollowsTheDepth: under deferred execution a
// header's GasUsed is the execution of the block it reports, so it is
// bounded by that block's gas limit -- the parent's at depth 1, the
// grandparent's at depth 2 (P2a).
func TestVerifyHeaderGasUsedBoundFollowsTheDepth(t *testing.T) {
	cfg := &params.ChainConfig{DeferredExecutionTime: big.NewInt(1)}
	h := New(nil, cfg)
	chain := newMockChainReader()
	g := &block.Header{Number: uint256.NewInt(5), Time: 10, GasLimit: 1000, Difficulty: uint256.NewInt(0)}
	p := &block.Header{Number: uint256.NewInt(6), Time: 11, GasLimit: 900, ParentHash: g.Hash(), Difficulty: uint256.NewInt(0)}
	chain.headers[5], chain.headers[6] = g, p
	extra, err := buildHeaderExtra(7, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := &block.Header{Number: uint256.NewInt(7), Time: 12, GasLimit: 900, GasUsed: 950, ParentHash: p.Hash(),
		Difficulty: uint256.NewInt(0), Extra: extra}

	restore := block.SetDeferredDepthForTest(1, 0)
	if err := h.VerifyHeader(chain, c, false); err == nil {
		t.Fatal("depth 1: gas used above the parent's limit must fail")
	}
	restore()
	defer block.SetDeferredDepthForTest(2, 0)()
	if err := h.VerifyHeader(chain, c, false); err != nil {
		t.Fatalf("depth 2: gas used within the grandparent's limit must pass: %v", err)
	}
	c.GasUsed = 1001
	if err := h.VerifyHeader(chain, c, false); err == nil {
		t.Fatal("depth 2: gas used above the grandparent's limit must fail")
	}
}

// TestAssembleWithoutExecutionKeepsTheLaggedFields: the leader's frame
// block built without execution keeps the lagged execution fields the
// builder stamped, carries the frame root and the frames, and leaves the
// transactions to be resolved when it executes.
func TestAssembleWithoutExecutionKeepsTheLaggedFields(t *testing.T) {
	prev := block.DeferredExecutionTime
	block.DeferredExecutionTime = 1
	defer func() { block.DeferredExecutionTime = prev }()
	h := New(nil, &params.ChainConfig{})
	hdr := &block.Header{Number: uint256.NewInt(9), Time: 100, GasLimit: 1 << 20, Difficulty: uint256.NewInt(0),
		Root: types.Hash{0xA}, ReceiptHash: types.Hash{0xB}, GasUsed: 77}
	hashes := []types.Hash{{1}, {2}}
	raws := [][]byte{{0x01}, {0x02}}
	b, err := h.AssembleWithoutExecution(newMockChainReader(), hdr, hashes, raws)
	if err != nil {
		t.Fatal(err)
	}
	out := b.Header().(*block.Header)
	if out.Root != hdr.Root || out.ReceiptHash != hdr.ReceiptHash || out.GasUsed != 77 {
		t.Fatalf("lagged fields not kept: %+v", out)
	}
	body := b.Body().(*block.Body)
	if b.TxHash() != block.FrameTxRoot(hashes) || len(body.FrameHashes) != 2 || len(body.FrameData) != 2 || body.Txs != nil {
		t.Fatal("frame body not assembled for later resolution")
	}
}
