// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package extensions

import (
	"context"
	"math/big"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/exex"
)

func TestAIIndexer_Name(t *testing.T) {
	idx := NewAIIndexer()
	if name := idx.Name(); name != "ai_indexer" {
		t.Fatalf("Name() = %q, want %q", name, "ai_indexer")
	}
}

// makeTestBlock creates a minimal block for testing with the given parameters.
func makeTestBlock(number uint64, timestamp uint64, gasLimit, gasUsed uint64, txs []*transaction.Transaction) block.IBlock {
	hdr := &block.Header{
		Number:   uint256.NewInt(number),
		Time:     timestamp,
		GasLimit: gasLimit,
		GasUsed:  gasUsed,
		BaseFee:  uint256.NewInt(1000000000), // 1 Gwei
	}
	return block.NewBlock(hdr, txs)
}

// makeTransferLog creates a log entry that matches the ERC20 Transfer event signature.
func makeTransferLog(contract, from, to types.Address, value *big.Int) *block.Log {
	// Transfer(address,address,uint256) — 3 topics for ERC20.
	var fromHash, toHash types.Hash
	copy(fromHash[12:], from.Bytes())
	copy(toHash[12:], to.Bytes())

	data := make([]byte, 32)
	value.FillBytes(data)

	return &block.Log{
		Address: contract,
		Topics:  []types.Hash{transferEventSig, fromHash, toHash},
		Data:    data,
	}
}

func TestAIIndexer_ProcessBlock(t *testing.T) {
	idx := NewAIIndexer()
	if err := idx.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer idx.Stop()

	contract := types.HexToAddress("0xToken")
	sender := types.HexToAddress("0xSender")
	receiver := types.HexToAddress("0xReceiver")

	to := receiver
	txs := []*transaction.Transaction{
		transaction.NewTransaction(0, sender, &to, uint256.NewInt(100), 21000, uint256.NewInt(1000000000), nil),
	}
	blk := makeTestBlock(100, 1700000000, 30000000, 21000, txs)

	transferLog := makeTransferLog(contract, sender, receiver, big.NewInt(500))
	receipts := []*block.Receipt{
		{
			Status: block.ReceiptStatusSuccessful,
			Logs:   []*block.Log{transferLog},
		},
	}

	notif := &exex.ExExNotification{
		Type:     exex.NotificationCommit,
		Block:    blk,
		Receipts: receipts,
		Number:   100,
	}

	if err := idx.OnNotification(context.Background(), notif); err != nil {
		t.Fatalf("OnNotification: %v", err)
	}

	// Verify transfers were indexed.
	transfers := idx.QueryTransfers(&contract, nil, nil, 100, 100, 10)
	if len(transfers) != 1 {
		t.Fatalf("transfers = %d, want 1", len(transfers))
	}
	if transfers[0].Contract != contract {
		t.Fatalf("transfer contract = %s, want %s", transfers[0].Contract.Hex(), contract.Hex())
	}
	if transfers[0].BlockNumber != 100 {
		t.Fatalf("transfer block = %d, want 100", transfers[0].BlockNumber)
	}

	// Verify gas metrics were recorded.
	metrics := idx.QueryGasMetrics(100, 100)
	if len(metrics) != 1 {
		t.Fatalf("gas metrics = %d, want 1", len(metrics))
	}
	if metrics[0].GasUsed != 21000 {
		t.Fatalf("gasUsed = %d, want 21000", metrics[0].GasUsed)
	}
	if metrics[0].GasLimit != 30000000 {
		t.Fatalf("gasLimit = %d, want 30000000", metrics[0].GasLimit)
	}
}

func TestAIIndexer_QueryTransfers(t *testing.T) {
	idx := NewAIIndexer()
	idx.Start(context.Background())
	defer idx.Stop()

	contractA := types.HexToAddress("0xA0")
	contractB := types.HexToAddress("0xB0")
	sender := types.HexToAddress("0x01")
	receiver := types.HexToAddress("0x02")

	// Process two blocks with different contracts.
	for i, contract := range []types.Address{contractA, contractB} {
		blk := makeTestBlock(uint64(100+i), 1700000000, 30000000, 21000, nil)
		log := makeTransferLog(contract, sender, receiver, big.NewInt(int64(100+i)))
		receipts := []*block.Receipt{
			{Status: block.ReceiptStatusSuccessful, Logs: []*block.Log{log}},
		}
		idx.OnNotification(context.Background(), &exex.ExExNotification{
			Type:     exex.NotificationCommit,
			Block:    blk,
			Receipts: receipts,
			Number:   uint64(100 + i),
		})
	}

	// Query by contract filter.
	results := idx.QueryTransfers(&contractA, nil, nil, 0, 0, 10)
	if len(results) != 1 {
		t.Fatalf("query contractA: got %d, want 1", len(results))
	}

	// Query by sender filter.
	results = idx.QueryTransfers(nil, &sender, nil, 0, 0, 10)
	if len(results) != 2 {
		t.Fatalf("query sender: got %d, want 2", len(results))
	}

	// Query by block range.
	results = idx.QueryTransfers(nil, nil, nil, 101, 101, 10)
	if len(results) != 1 {
		t.Fatalf("query block 101: got %d, want 1", len(results))
	}
}

func TestAIIndexer_QueryGasMetrics(t *testing.T) {
	idx := NewAIIndexer()
	idx.Start(context.Background())
	defer idx.Stop()

	// Process multiple blocks.
	for i := uint64(0); i < 5; i++ {
		blk := makeTestBlock(100+i, 1700000000+i*12, 30000000, 15000000+i*1000000, nil)
		idx.OnNotification(context.Background(), &exex.ExExNotification{
			Type:   exex.NotificationCommit,
			Block:  blk,
			Number: 100 + i,
		})
	}

	// Query all.
	all := idx.QueryGasMetrics(0, 0)
	if len(all) != 5 {
		t.Fatalf("all metrics = %d, want 5", len(all))
	}

	// Query subset.
	subset := idx.QueryGasMetrics(102, 103)
	if len(subset) != 2 {
		t.Fatalf("subset metrics = %d, want 2", len(subset))
	}

	// Verify utilization.
	for _, m := range all {
		if m.Utilization <= 0 || m.Utilization > 1.0 {
			t.Fatalf("utilization = %f, want in (0, 1]", m.Utilization)
		}
	}
}

// makeCustomEventLog creates a log entry with a custom event signature for testing event queries.
func makeCustomEventLog(contract types.Address, eventSig types.Hash, data []byte) *block.Log {
	return &block.Log{
		Address: contract,
		Topics:  []types.Hash{eventSig},
		Data:    data,
	}
}

func TestAIIndexer_RevertBlock(t *testing.T) {
	idx := NewAIIndexer()
	if err := idx.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer idx.Stop()

	contract := types.HexToAddress("0xToken")
	sender := types.HexToAddress("0xSender")
	receiver := types.HexToAddress("0xReceiver")

	to := receiver
	txs := []*transaction.Transaction{
		transaction.NewTransaction(0, sender, &to, uint256.NewInt(100), 21000, uint256.NewInt(1000000000), nil),
	}
	blk := makeTestBlock(200, 1700000000, 30000000, 21000, txs)

	transferLog := makeTransferLog(contract, sender, receiver, big.NewInt(1000))
	receipts := []*block.Receipt{
		{
			Status: block.ReceiptStatusSuccessful,
			Logs:   []*block.Log{transferLog},
		},
	}

	// Process the block.
	notif := &exex.ExExNotification{
		Type:     exex.NotificationCommit,
		Block:    blk,
		Receipts: receipts,
		Number:   200,
	}
	if err := idx.OnNotification(context.Background(), notif); err != nil {
		t.Fatalf("OnNotification (commit): %v", err)
	}

	// Verify data exists before revert.
	transfers := idx.QueryTransfers(nil, nil, nil, 200, 200, 10)
	if len(transfers) != 1 {
		t.Fatalf("pre-revert transfers = %d, want 1", len(transfers))
	}
	events := idx.QueryEvents(nil, nil, 200, 200, 10)
	if len(events) == 0 {
		t.Fatal("pre-revert events should not be empty")
	}
	gasMetrics := idx.QueryGasMetrics(200, 200)
	if len(gasMetrics) != 1 {
		t.Fatalf("pre-revert gas metrics = %d, want 1", len(gasMetrics))
	}

	// Revert the block.
	revertNotif := &exex.ExExNotification{
		Type:   exex.NotificationRevert,
		Number: 200,
	}
	if err := idx.OnNotification(context.Background(), revertNotif); err != nil {
		t.Fatalf("OnNotification (revert): %v", err)
	}

	// Verify transfers are removed.
	transfers = idx.QueryTransfers(nil, nil, nil, 200, 200, 10)
	if len(transfers) != 0 {
		t.Fatalf("post-revert transfers = %d, want 0", len(transfers))
	}

	// Verify events are removed.
	events = idx.QueryEvents(nil, nil, 200, 200, 10)
	if len(events) != 0 {
		t.Fatalf("post-revert events = %d, want 0", len(events))
	}

	// Verify gas metrics are removed.
	gasMetrics = idx.QueryGasMetrics(200, 200)
	if len(gasMetrics) != 0 {
		t.Fatalf("post-revert gas metrics = %d, want 0", len(gasMetrics))
	}
}

func TestAIIndexer_QueryEventsFiltered(t *testing.T) {
	idx := NewAIIndexer()
	if err := idx.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer idx.Stop()

	contractA := types.HexToAddress("0xAAA")
	contractB := types.HexToAddress("0xBBB")
	eventSigA := crypto.Keccak256Hash([]byte("EventA(uint256)"))
	eventSigB := crypto.Keccak256Hash([]byte("EventB(address,uint256)"))

	// Block 300: contractA emits EventA, contractB emits EventB.
	blk1 := makeTestBlock(300, 1700000000, 30000000, 50000, nil)
	logA := makeCustomEventLog(contractA, eventSigA, []byte{0x01})
	logB := makeCustomEventLog(contractB, eventSigB, []byte{0x02})
	receipts1 := []*block.Receipt{
		{
			Status: block.ReceiptStatusSuccessful,
			Logs:   []*block.Log{logA, logB},
		},
	}
	if err := idx.OnNotification(context.Background(), &exex.ExExNotification{
		Type:     exex.NotificationCommit,
		Block:    blk1,
		Receipts: receipts1,
		Number:   300,
	}); err != nil {
		t.Fatalf("OnNotification block 300: %v", err)
	}

	// Block 301: contractA emits EventB, contractB emits EventA.
	blk2 := makeTestBlock(301, 1700000012, 30000000, 60000, nil)
	logC := makeCustomEventLog(contractA, eventSigB, []byte{0x03})
	logD := makeCustomEventLog(contractB, eventSigA, []byte{0x04})
	receipts2 := []*block.Receipt{
		{
			Status: block.ReceiptStatusSuccessful,
			Logs:   []*block.Log{logC, logD},
		},
	}
	if err := idx.OnNotification(context.Background(), &exex.ExExNotification{
		Type:     exex.NotificationCommit,
		Block:    blk2,
		Receipts: receipts2,
		Number:   301,
	}); err != nil {
		t.Fatalf("OnNotification block 301: %v", err)
	}

	// Query all events: should be 4 total.
	all := idx.QueryEvents(nil, nil, 0, 0, 100)
	if len(all) != 4 {
		t.Fatalf("all events = %d, want 4", len(all))
	}

	// Filter by contractA: should get 2 events (EventA in block 300, EventB in block 301).
	byContractA := idx.QueryEvents(&contractA, nil, 0, 0, 100)
	if len(byContractA) != 2 {
		t.Fatalf("events for contractA = %d, want 2", len(byContractA))
	}
	for _, e := range byContractA {
		if e.Contract != contractA {
			t.Fatalf("event contract = %s, want %s", e.Contract.Hex(), contractA.Hex())
		}
	}

	// Filter by eventSigA: should get 2 events (contractA block 300, contractB block 301).
	byEventSigA := idx.QueryEvents(nil, &eventSigA, 0, 0, 100)
	if len(byEventSigA) != 2 {
		t.Fatalf("events for eventSigA = %d, want 2", len(byEventSigA))
	}
	for _, e := range byEventSigA {
		if e.EventSig != eventSigA {
			t.Fatalf("event sig = %s, want %s", e.EventSig.Hex(), eventSigA.Hex())
		}
	}

	// Filter by contractB + eventSigB: should get exactly 1 event (block 300).
	byContractBSigB := idx.QueryEvents(&contractB, &eventSigB, 0, 0, 100)
	if len(byContractBSigB) != 1 {
		t.Fatalf("events for contractB+eventSigB = %d, want 1", len(byContractBSigB))
	}
	if byContractBSigB[0].BlockNumber != 300 {
		t.Fatalf("event block = %d, want 300", byContractBSigB[0].BlockNumber)
	}

	// Filter by block range: only block 301.
	byBlock301 := idx.QueryEvents(nil, nil, 301, 301, 100)
	if len(byBlock301) != 2 {
		t.Fatalf("events in block 301 = %d, want 2", len(byBlock301))
	}
	for _, e := range byBlock301 {
		if e.BlockNumber != 301 {
			t.Fatalf("event block = %d, want 301", e.BlockNumber)
		}
	}
}
