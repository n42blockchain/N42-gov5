// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"math"
	"math/rand"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
)

func gasTxs(gas []uint64) []*transaction.Transaction {
	txs := make([]*transaction.Transaction, len(gas))
	for i, g := range gas {
		txs[i] = transaction.NewTx(&transaction.LegacyTx{Nonce: uint64(i), Gas: g, GasPrice: uint256.NewInt(1), Value: uint256.NewInt(0)})
	}
	return txs
}

// TestAvailGasWithinMatchesSerial: in every mode the decision equals the
// original serial loop's, including sums that wrap uint64 (the serial loop
// can pass those; the resolver's saturating sum then falls back to it).
func TestAvailGasWithinMatchesSerial(t *testing.T) {
	rng := rand.New(rand.NewSource(63))
	cases := [][]uint64{
		nil,
		{21000},
		{21000, 21000, 21000},
		{math.MaxUint64 - 5, 10}, // wraps to 4
		{10, math.MaxUint64 - 5}, // wraps to 4
		{1 << 62, 1 << 62, 1 << 62, 1 << 62, 7},
	}
	for i := 0; i < 200; i++ {
		n := rng.Intn(50)
		g := make([]uint64, n)
		for j := range g {
			switch rng.Intn(4) {
			case 0:
				g[j] = rng.Uint64()
			default:
				g[j] = uint64(rng.Intn(100000))
			}
		}
		cases = append(cases, g)
	}
	limits := []uint64{0, 21000, 63000, 1 << 20, 1 << 30, math.MaxUint64 - 1, math.MaxUint64}
	for _, mode := range []int{availAdmitGasOff, availAdmitGasOn, availAdmitGasCheck} {
		restore := SetAvailAdmitGasForTest(mode)
		for ci, g := range cases {
			txs := gasTxs(g)
			// Split into "frames" the way the resolver sums them.
			var sum uint64
			for k := 0; k < len(txs); k += 3 {
				end := k + 3
				if end > len(txs) {
					end = len(txs)
				}
				sum = saturatingAdd(sum, saturatingGasSum(txs[k:end]))
			}
			body := &block.Body{Txs: txs, FrameHashes: []types.Hash{{1}}}
			body.SetResolvedGas(sum, len(txs))
			for _, lim := range limits {
				want := serialGasWithin(txs, lim)
				got, _ := availGasWithin(body, lim)
				if got != want {
					t.Fatalf("mode %d case %d limit %d: got %v, serial %v (gas %v)", mode, ci, lim, got, want, g)
				}
			}
		}
		restore()
	}
	if n := availAdmitGasMismatches.Load(); n != 0 {
		t.Fatalf("check mode reported %d mismatches", n)
	}
}

// TestResolveFramesRecordsGas: with the switch on, the resolver's recorded
// sum equals the serial sum of the resolved transactions; off, nothing is
// recorded (the old path).
func TestResolveFramesRecordsGas(t *testing.T) {
	for _, mode := range []int{availAdmitGasOff, availAdmitGasOn} {
		restore := SetAvailAdmitGasForTest(mode)
		c := newAvailChain(t)
		blk := c.child(gResult(c.g), c.hashes)
		if err := c.bc.ResolveFrames(blk); err != nil {
			t.Fatal(err)
		}
		body := blk.Body().(*block.Body)
		g, ok := body.ResolvedGas()
		if mode == availAdmitGasOff {
			if ok {
				t.Fatal("switch off: no sum must be recorded")
			}
		} else if !ok || g != saturatingGasSum(body.Txs) || g != uint64(len(body.Txs))*c.gasEach {
			t.Fatalf("switch on: recorded %d (ok %v), serial %d, txs %d", g, ok, saturatingGasSum(body.Txs), len(body.Txs))
		}
		// A changed transaction list invalidates the sum.
		body.Txs = body.Txs[:len(body.Txs)-1]
		if _, ok := body.ResolvedGas(); ok {
			t.Fatal("sum must not survive a change of the transaction count")
		}
		restore()
	}
}

// TestAvailabilityGasLimitWithResolverSum: the availability check passes
// and refuses on the gas limit the same way with the switch on.
func TestAvailabilityGasLimitWithResolverSum(t *testing.T) {
	defer block.SetDeferredDepthForTest(2, 0)()
	defer block.SetVoteOnAvailabilityForTest(true)()
	defer SetAvailAdmitGasForTest(availAdmitGasOn)()
	c := newAvailChain(t)
	blk := c.child(gResult(c.g), c.hashes)
	if _, retry, err := c.bc.CheckDeferredBlock(blk); retry || err != nil {
		t.Fatalf("within limit: retry=%v err=%v", retry, err)
	}
	if g, ok := blk.Body().(*block.Body).ResolvedGas(); !ok || g != 6*c.gasEach {
		t.Fatalf("resolved gas %d ok %v", g, ok)
	}

	c2 := newAvailChain(t)
	hdr := &block.Header{Number: uint256.NewInt(12), Time: 1001, ParentHash: c2.p.Hash(), GasLimit: 6*c2.gasEach - 1,
		Difficulty: uint256.NewInt(0), Root: c2.g.Root, ReceiptHash: c2.g.ReceiptHash, GasUsed: c2.g.GasUsed}
	over := block.NewFrameBlockFromReceipt(hdr, c2.hashes, nil, nil, nil).(*block.Block)
	if _, retry, err := c2.bc.CheckDeferredBlock(over); err == nil || retry {
		t.Fatalf("over the limit: retry=%v err=%v", retry, err)
	}
}
