package txspool

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/params"
)

// prewarmTestTxs builds n distinct signed transactions from distinct keys, so
// no sender recovery can be served from a memo of another one.
func prewarmTestTxs(t testing.TB, n int) ([]*transaction.Transaction, []types.Address, transaction.Signer) {
	t.Helper()
	chainID := params.TestChainConfig.ChainID
	signer := transaction.LatestSignerForChainID(chainID)
	to := types.Address{0x11, 0x22}

	txs := make([]*transaction.Transaction, 0, n)
	want := make([]types.Address, 0, n)
	for i := 0; i < n; i++ {
		key, err := crypto.GenerateKey()
		if err != nil {
			t.Fatal(err)
		}
		tx, err := transaction.SignTx(transaction.NewTx(&transaction.DynamicFeeTx{
			ChainID:   uint256.MustFromBig(chainID),
			Nonce:     0,
			GasTipCap: uint256.NewInt(1),
			GasFeeCap: uint256.NewInt(1e10),
			Gas:       21000,
			To:        &to,
			Value:     uint256.NewInt(1),
		}), signer, key)
		if err != nil {
			t.Fatal(err)
		}
		txs = append(txs, tx)
		want = append(want, crypto.PubkeyToAddress(key.PublicKey))
	}
	return txs, want, signer
}

// TestPrewarmSendersMatchesSerial is the property that matters: warming the
// batch concurrently must produce exactly the senders a serial pass would.
// The prewarm decides nothing on its own, so a divergence here would mean the
// pool admits transactions under the wrong account.
func TestPrewarmSendersMatchesSerial(t *testing.T) {
	txs, want, signer := prewarmTestTxs(t, 64)

	pool := &TxsPool{chainconfig: params.TestChainConfig, all: newTxLookup()}
	pool.prewarmSenders(txs)

	for i, tx := range txs {
		got, err := transaction.Sender(signer, tx)
		if err != nil {
			t.Fatalf("tx %d: %v", i, err)
		}
		if got != want[i] {
			t.Fatalf("tx %d sender = %s, want %s", i, got, want[i])
		}
	}
}

// TestPrewarmSendersSkipsKnown: a transaction the pool already holds had its
// sender recovered when it was admitted, and re-recovering it is the waste
// this exists to remove.
func TestPrewarmSendersSkipsKnown(t *testing.T) {
	txs, _, _ := prewarmTestTxs(t, 16)
	pool := &TxsPool{chainconfig: params.TestChainConfig, all: newTxLookup()}
	for _, tx := range txs {
		pool.all.Add(tx, false)
	}

	transaction.ResetSenderCacheStats()
	pool.prewarmSenders(txs)
	if hits, misses := transaction.SenderCacheStats(); hits != 0 || misses != 0 {
		t.Fatalf("prewarm touched the sender cache for known transactions: hits=%d misses=%d", hits, misses)
	}
}

// TestPrewarmSendersToleratesNil covers a batch with holes; the caller's
// validation loop is what rejects them.
func TestPrewarmSendersToleratesNil(t *testing.T) {
	txs, _, _ := prewarmTestTxs(t, 16)
	txs[3], txs[9] = nil, nil
	pool := &TxsPool{chainconfig: params.TestChainConfig, all: newTxLookup()}
	pool.prewarmSenders(txs) // must not panic
}

func benchmarkSenderRecovery(b *testing.B, parallel bool) {
	b.Helper()
	const batch = 512
	pool := &TxsPool{chainconfig: params.TestChainConfig, all: newTxLookup()}
	signer := transaction.LatestSignerForChainID(params.TestChainConfig.ChainID)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		txs, _, _ := prewarmTestTxs(b, batch)
		b.StartTimer()
		if parallel {
			pool.prewarmSenders(txs)
		} else {
			for _, tx := range txs {
				_, _ = transaction.Sender(signer, tx)
			}
		}
	}
}

// BenchmarkSenderRecoverySerial / Parallel quantify the change. Recovery is
// pure CPU with no shared state, so the ratio should track core count up to
// the worker bound.
func BenchmarkSenderRecoverySerial(b *testing.B)   { benchmarkSenderRecovery(b, false) }
func BenchmarkSenderRecoveryParallel(b *testing.B) { benchmarkSenderRecovery(b, true) }
