// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package transaction

import (
	"math/big"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
)

const benchChainID = 1142

// benchTransfers signs n EIP-155 simple transfers and returns them decoded
// from their wire encoding (so the consensus encoding is cached, as on a
// follower).
func benchTransfers(tb testing.TB, n int) ([]*Transaction, [][]byte) {
	tb.Helper()
	signer := NewLondonSigner(big.NewInt(benchChainID))
	key, err := crypto.GenerateKey()
	if err != nil {
		tb.Fatal(err)
	}
	txs := make([]*Transaction, n)
	raws := make([][]byte, n)
	for i := 0; i < n; i++ {
		to := types.Address{byte(i), byte(i >> 8), 0x42}
		tx, err := SignTx(NewTx(&LegacyTx{Nonce: uint64(i), GasPrice: uint256.NewInt(1_000_000_000), Gas: 21000,
			To: &to, Value: uint256.NewInt(uint64(1000 + i))}), signer, key)
		if err != nil {
			tb.Fatal(err)
		}
		raw, err := EncodeEthereumTransaction(tx)
		if err != nil {
			tb.Fatal(err)
		}
		raws[i] = raw
		dec, err := DecodeEthereumTransaction(raw)
		if err != nil {
			tb.Fatal(err)
		}
		txs[i] = dec
	}
	return txs, raws
}

func BenchmarkLegacySigningHash(b *testing.B) {
	txs, _ := benchTransfers(b, 64)
	cid := big.NewInt(benchChainID)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = legacySigningHash(txs[i&63], cid)
	}
}

func BenchmarkTxComputeHash(b *testing.B) {
	txs, raws := benchTransfers(b, 64)
	b.Logf("transfer encoding %d bytes", len(raws[0]))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = txs[i&63].computeHash()
	}
}

func BenchmarkLegacySender(b *testing.B) {
	txs, _ := benchTransfers(b, 64)
	signer := NewLondonSigner(big.NewInt(benchChainID))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := signer.Sender(txs[i&63]); err != nil {
			b.Fatal(err)
		}
	}
}

func resetHashes(txs []*Transaction) {
	for _, tx := range txs {
		tx.hashState.Store(hashNone)
	}
}

// BenchmarkHashTxs64 memoises the hashes of 64 decoded transfers per op:
// one by one (tx.Hash, today's frame.HashTxs) and with HashBatch.
func BenchmarkHashTxs64(b *testing.B) {
	txs, _ := benchTransfers(b, 64)
	b.Run("loop", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			resetHashes(txs)
			for _, tx := range txs {
				tx.Hash()
			}
		}
	})
	b.Run("batch", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			resetHashes(txs)
			HashBatch(txs)
		}
	})
}

// BenchmarkSenders64 recovers the senders of 64 transfers per op: one by
// one (signer.Sender, today's frame.RecoverSenders) and with SendersBatch.
func BenchmarkSenders64(b *testing.B) {
	txs, _ := benchTransfers(b, 64)
	signer := NewLondonSigner(big.NewInt(benchChainID))
	addrs := make([]types.Address, 64)
	errs := make([]error, 64)
	b.Run("loop", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			for j, tx := range txs {
				addrs[j], errs[j] = signer.Sender(tx)
			}
		}
	})
	b.Run("batch", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			SendersBatch(signer, txs, addrs, errs)
		}
	})
}
