// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
)

// TestCollectParallelResultsMatchesSerial (P10): the chunked assembly of
// receipts, logs, gas and fee totals equals the serial loops', skipped
// transactions, logs and several fee recipients included.
func TestCollectParallelResultsMatchesSerial(t *testing.T) {
	const n = 3*collectParallelMinTxs + 17
	header := &block.Header{Number: uint256.NewInt(7)}
	to := types.HexToAddress("0x00000000000000000000000000000000000000cc")
	coinbase := types.HexToAddress("0x00000000000000000000000000000000000000c0")
	collector := types.HexToAddress("0x00000000000000000000000000000000000000c1")
	txs := make([]*transaction.Transaction, n)
	results := make([]parallelTxResult, n)
	skipped := make([]bool, n)
	for i := range txs {
		txs[i] = transaction.NewTransaction(uint64(i), types.Address{}, &to, uint256.NewInt(1), 21000, uint256.NewInt(1), nil)
		r := &results[i]
		if i%997 == 5 {
			skipped[i] = true
			continue
		}
		r.gasUsed = 21000 + uint64(i%7)
		r.receipt = &block.Receipt{GasUsed: r.gasUsed, TransactionIndex: uint(i)}
		if i%1500 == 3 {
			r.logs = []*block.Log{{Index: uint(i)}, {Index: uint(i) + 1}}
		}
		fees := []deferredFee{{recipient: coinbase, amount: *uint256.NewInt(uint64(i))}}
		if i%3 == 0 {
			// The collector first appears at transaction 0.
			fees = append(fees, deferredFee{recipient: collector, amount: *uint256.NewInt(2)})
		}
		r.setFees(fees)
	}

	got, ok := collectParallelResults(txs, results, skipped, header)
	require.True(t, ok)

	// The serial loops of runParallelMode.
	var usedGas uint64
	var receipts block.Receipts
	var logs []*block.Log
	for i := 0; i < n; i++ {
		if skipped[i] {
			receipts = append(receipts, SkippedTxReceipt(txs[i], header, i, usedGas))
			continue
		}
		usedGas += results[i].gasUsed
		receipts = append(receipts, &block.Receipt{CumulativeGasUsed: usedGas})
		logs = append(logs, results[i].logs...)
	}
	totals := map[types.Address]*uint256.Int{}
	var order []types.Address
	for i := 0; i < n; i++ {
		for _, f := range results[i].fees {
			tt, ok := totals[f.recipient]
			if !ok {
				tt = new(uint256.Int)
				totals[f.recipient] = tt
				order = append(order, f.recipient)
			}
			tt.Add(tt, &f.amount)
		}
	}

	require.Equal(t, usedGas, got.usedGas)
	require.Len(t, got.receipts, n)
	for i := range receipts {
		require.Equal(t, receipts[i].CumulativeGasUsed, got.receipts[i].CumulativeGasUsed, "receipt %d", i)
		if skipped[i] {
			require.Equal(t, receipts[i].TxHash, got.receipts[i].TxHash)
			require.Equal(t, block.ReceiptStatusFailed, got.receipts[i].Status)
		} else {
			require.Same(t, results[i].receipt, got.receipts[i])
		}
	}
	require.Equal(t, logs, got.logs)
	require.Equal(t, order, got.feeOrder)
	require.Equal(t, totals, got.feeTotals)

	// A kept transaction without a receipt leaves it to the serial loops.
	results[11].receipt = nil
	_, ok = collectParallelResults(txs, results, skipped, header)
	require.False(t, ok)
	// So does a small block.
	_, ok = collectParallelResults(txs[:100], results[:100], skipped[:100], header)
	require.False(t, ok)
}
