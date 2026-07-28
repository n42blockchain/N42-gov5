// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package miner

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/p2p/encoder"
	"github.com/n42blockchain/N42/lib/rlp"
)

// testMiningHeader builds a header shaped like one prepareWork produces.
func testMiningHeader(number uint64) *block.Header {
	return &block.Header{
		ParentHash: types.BytesToHash([]byte{0x11}),
		UncleHash:  types.BytesToHash([]byte{0x22}),
		Coinbase:   types.BytesToAddress([]byte{0x33}),
		Root:       types.BytesToHash([]byte{0x44}),
		TxHash:     types.BytesToHash([]byte{0x55}),
		Difficulty: uint256.NewInt(0),
		Number:     uint256.NewInt(number),
		GasLimit:   1_000_000_000,
		GasUsed:    0,
		Time:       1700000000,
		Extra:      make([]byte, 32),
		BaseFee:    uint256.NewInt(1_000_000_000),
	}
}

// testTransfer builds a signed-looking plain-transfer transaction. Only its
// encoded size matters here, so the signature values are synthetic but
// full-width, exactly as a real secp256k1 signature encodes.
func testTransfer(nonce uint64, dataLen int) *transaction.Transaction {
	to := types.BytesToAddress([]byte{byte(nonce), byte(nonce >> 8), 0xAB})
	// Full-width 32-byte signature components, as secp256k1 actually produces.
	sigWord := make([]byte, 32)
	for i := range sigWord {
		sigWord[i] = byte(0x41 + i)
	}
	sigWord[31] = byte(nonce)
	big := new(uint256.Int).SetBytes(sigWord)
	return transaction.NewTx(&transaction.DynamicFeeTx{
		ChainID:   uint256.NewInt(94),
		Nonce:     nonce,
		GasTipCap: uint256.NewInt(1_000_000_000),
		GasFeeCap: uint256.NewInt(2_000_000_000),
		Gas:       21000,
		To:        &to,
		Value:     uint256.NewInt(1_000_000_000_000_000),
		Data:      make([]byte, dataLen),
		V:         uint256.NewInt(1),
		R:         big,
		S:         big,
	})
}

// withGossipLimit temporarily overrides the p2p wire bound.
func withGossipLimit(t *testing.T, bytes uint64) {
	t.Helper()
	oldGossip, oldChunk := encoder.MaxGossipSize, encoder.MaxChunkSize
	encoder.MaxGossipSize, encoder.MaxChunkSize = bytes, bytes
	t.Cleanup(func() {
		encoder.MaxGossipSize, encoder.MaxChunkSize = oldGossip, oldChunk
	})
}

// runPackingLoop mirrors fillTransactions' size gate exactly: peek a candidate,
// ask the limiter, then accept / skip the account / stop.
func runPackingLoop(t *testing.T, lim *blockSizeLimiter, candidates []*transaction.Transaction) (packed []*transaction.Transaction, stopped bool) {
	t.Helper()
	for _, tx := range candidates {
		size, decision, err := lim.admit(tx)
		switch decision {
		case packStop:
			return packed, true
		case packSkipAccount:
			if err != nil {
				t.Fatalf("unexpected size-accounting error: %v", err)
			}
			continue
		}
		packed = append(packed, tx)
		lim.add(size)
	}
	return packed, false
}

// TestPackingStopsBelowWireLimit is the regression test for the livelock: with
// far more pending transactions than fit, the packing gate must stop early AND
// the resulting block must RLP-encode below the p2p wire bound. Before the fix
// the miner packed every transaction the gas limit allowed, sealing ~1.5 MiB
// blocks that neither gossip nor direct push could carry — no follower could
// import them, import-gated HotStuff voting never fired, and the chain
// livelocked across restarts.
func TestPackingStopsBelowWireLimit(t *testing.T) {
	withGossipLimit(t, 1<<20)

	header := testMiningHeader(1234)
	lim := newBlockSizeLimiter(header)

	// ~15000 plain transfers: comfortably above what a 1 MiB block can carry
	// (the live incident sealed 1501170 bytes at ~9500 transfers).
	candidates := make([]*transaction.Transaction, 0, 15000)
	for i := 0; i < 15000; i++ {
		candidates = append(candidates, testTransfer(uint64(i), 0))
	}

	packed, stopped := runPackingLoop(t, lim, candidates)

	if !stopped {
		t.Fatalf("packing consumed all %d candidates without hitting the size budget", len(candidates))
	}
	if len(packed) == 0 {
		t.Fatal("packing produced an empty block: budget accounting is broken")
	}
	if len(packed) >= len(candidates) {
		t.Fatalf("packing did not stop early: packed %d of %d", len(packed), len(candidates))
	}

	// The decisive assertion: the sealed block fits on the wire.
	blk := block.NewBlock(header, packed)
	enc, err := rlp.EncodeToBytes(blk)
	if err != nil {
		t.Fatalf("encode block: %v", err)
	}
	limit := encoder.MaxWireMessageSize()
	if uint64(len(enc)) > limit {
		t.Fatalf("sealed block is %d bytes, above the %d byte wire limit", len(enc), limit)
	}

	// ...and is not needlessly conservative: at least 70% of the bound is used.
	if uint64(len(enc))*10 < limit*7 {
		t.Fatalf("packing is too conservative: %d bytes of a %d byte bound (%d txs)", len(enc), limit, len(packed))
	}
	t.Logf("packed %d/%d txs, block encodes to %d bytes (limit %d)", len(packed), len(candidates), len(enc), limit)
}

// TestPackingBudgetScalesWithWireLimit proves the budget is derived from the
// encoder bound rather than hard-coded, so raising N42_MAX_GOSSIP_MB
// network-wide also raises how much a leader may pack.
func TestPackingBudgetScalesWithWireLimit(t *testing.T) {
	header := testMiningHeader(1)

	withGossipLimit(t, 1<<20)
	small := newBlockSizeLimiter(header).budget

	withGossipLimit(t, 16<<20)
	large := newBlockSizeLimiter(header).budget

	if small <= 0 || large <= 0 {
		t.Fatalf("non-positive budgets: small=%d large=%d", small, large)
	}
	if large <= small*8 {
		t.Fatalf("budget did not scale with the wire limit: 1 MiB -> %d, 16 MiB -> %d", small, large)
	}

	// The 1 MiB budget must leave real headroom under the bound for the header,
	// the consensus seal, rewards and the outer RLP framing.
	withGossipLimit(t, 1<<20)
	if uint64(newBlockSizeLimiter(header).budget) >= encoder.MaxWireMessageSize() {
		t.Fatal("budget leaves no headroom below the wire limit")
	}
}

// TestPackingSkipsUnfittableTransaction proves a transaction too large to ever
// fit is skipped rather than stalling the block forever at the head of the
// priority queue, and that smaller transactions behind it still get packed.
func TestPackingSkipsUnfittableTransaction(t *testing.T) {
	withGossipLimit(t, 1<<20)

	header := testMiningHeader(7)
	lim := newBlockSizeLimiter(header)

	huge := testTransfer(0, lim.budget+1024)
	hugeSize, decision, err := lim.admit(huge)
	if err != nil {
		t.Fatalf("size accounting failed: %v", err)
	}
	if decision != packSkipAccount {
		t.Fatalf("oversized transaction: got decision %v, want packSkipAccount", decision)
	}
	if !lim.tooLargeForAnyBlock(hugeSize) {
		t.Fatalf("oversized transaction (%d bytes) not reported as unfittable (budget %d)", hugeSize, lim.budget)
	}

	small := testTransfer(1, 0)
	size, decision, err := lim.admit(small)
	if err != nil {
		t.Fatalf("size accounting failed: %v", err)
	}
	if decision != packAccept {
		t.Fatalf("normal transaction after an oversized one: got %v, want packAccept", decision)
	}
	lim.add(size)
	if lim.used == 0 {
		t.Fatal("accepted transaction was not charged to the budget")
	}
}

// TestTxPayloadSizeIsAnUpperBound proves the per-transaction estimate never
// undercounts the bytes a transaction contributes to rlp(block) — the property
// the whole guarantee rests on.
func TestTxPayloadSizeIsAnUpperBound(t *testing.T) {
	header := testMiningHeader(9)
	for _, dataLen := range []int{0, 1, 55, 56, 200, 4096, 70000} {
		tx := testTransfer(1, dataLen)
		est, err := txPayloadSize(tx)
		if err != nil {
			t.Fatalf("dataLen=%d: %v", dataLen, err)
		}

		empty, err := rlp.EncodeToBytes(block.NewBlock(header, nil))
		if err != nil {
			t.Fatalf("encode empty block: %v", err)
		}
		one, err := rlp.EncodeToBytes(block.NewBlock(header, []*transaction.Transaction{tx}))
		if err != nil {
			t.Fatalf("encode one-tx block: %v", err)
		}
		// The marginal contribution of a transaction is its payload plus, once
		// per block, the growth of the two enclosing RLP list headers (bounded
		// by 9 bytes each). That block-level framing is charged separately, in
		// blockNonTxReserve — the per-transaction estimate only has to bound the
		// payload.
		const listFramingGrowth = 18
		actual := len(one) - len(empty)
		if est+listFramingGrowth < actual {
			t.Fatalf("dataLen=%d: estimate %d undercounts actual contribution %d", dataLen, est, actual)
		}
		if est > actual+16 {
			t.Fatalf("dataLen=%d: estimate %d wastes %d bytes over actual %d", dataLen, est, est-actual, actual)
		}
	}
}
