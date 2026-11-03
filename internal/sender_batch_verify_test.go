package internal

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
)

type batchVerifyHints struct {
	pool    mapHintSource
	mu      sync.RWMutex
	batches atomic.Int64
}

func (h *batchVerifyHints) GetTx(hash types.Hash) *transaction.Transaction {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.pool[hash]
}
func (h *batchVerifyHints) GetTxs(hashes []types.Hash, dst []*transaction.Transaction) {
	if len(hashes) > 256 {
		panic("unbounded hint batch")
	}
	h.batches.Add(1)
	h.mu.RLock()
	defer h.mu.RUnlock()
	for i, hash := range hashes {
		dst[i] = h.pool[hash]
	}
}

type scalarVerifyHints struct{ h *batchVerifyHints }

func (h scalarVerifyHints) GetTx(hash types.Hash) *transaction.Transaction { return h.h.GetTx(hash) }

func TestVerifyBlockSendersBatchHints(t *testing.T) {
	for _, n := range []int{3, 257, 1031} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			txs, pool, signer := poolVerifyFixture(t, n)
			hints := &batchVerifyHints{pool: pool}
			// A wrong hash and a missing pool entry must use signature recovery.
			pool[txs[0].Hash()] = pool[txs[1].Hash()]
			delete(pool, txs[n-1].Hash())
			if err := verifyBlockSendersWithHints(signer, txs, hints); err != nil {
				t.Fatal(err)
			}
			if hints.batches.Load() == 0 {
				t.Fatal("batch path unused")
			}
			// Cover both a complete batch boundary and the final partial batch.
			bad := n - 1
			if n > 256 {
				bad = 256
			}
			txs[bad].SetFrom(types.Address{0xfe})
			txs[n-1].SetFrom(types.Address{0xfd})
			err := verifyBlockSendersWithHints(signer, txs, hints)
			if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("tx %d ", bad)) {
				t.Fatalf("expected offender %d: %v", bad, err)
			}
		})
	}
}

func BenchmarkVerifyBlockSendersBatchHints(b *testing.B) {
	txs, pool, signer := poolVerifyFixture(b, 163000)
	hints := &batchVerifyHints{pool: pool}
	for _, batch := range []bool{false, true} {
		b.Run(fmt.Sprintf("batch=%v", batch), func(b *testing.B) {
			var source SenderHintSource = scalarVerifyHints{hints}
			if batch {
				source = hints
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := verifyBlockSendersWithHints(signer, txs, source); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func TestApplySenderHintBatches(t *testing.T) {
	txs, pool, signer := poolVerifyFixture(t, 517)
	wire := make([]*transaction.Transaction, len(txs))
	for i, tx := range txs {
		raw, err := transaction.EncodeEthereumTransaction(tx)
		if err != nil {
			t.Fatal(err)
		}
		wire[i], err = transaction.DecodeEthereumTransaction(raw)
		if err != nil {
			t.Fatal(err)
		}
	}
	// Poison the asserted field on a genuine pool object: Sender must use its
	// signature memo. Wrong-hash hints and misses must remain unfilled.
	pool[txs[2].Hash()].SetFrom(types.Address{0xff})
	pool[txs[0].Hash()] = pool[txs[1].Hash()]
	delete(pool, txs[516].Hash())
	wire[255] = nil
	hints := &batchVerifyHints{pool: pool}
	if n := applySenderHints(hints, signer, wire); n != 514 {
		t.Fatalf("filled %d", n)
	}
	for i, tx := range wire {
		if tx == nil {
			continue
		}
		if i == 0 || i == 516 {
			if tx.From() != nil {
				t.Fatal("unverified hint applied")
			}
			continue
		}
		if tx.From() == nil || *tx.From() != *txs[i].From() {
			t.Fatalf("sender %d changed", i)
		}
	}
	if n := applySenderHints(hints, signer, wire); n != 0 {
		t.Fatalf("refilled %d", n)
	}
}

func BenchmarkApplySenderHintBatches(b *testing.B) {
	txs, pool, signer := poolVerifyFixture(b, 163000)
	encoded := make([][]byte, len(txs))
	for i, tx := range txs {
		var err error
		encoded[i], err = transaction.EncodeEthereumTransaction(tx)
		if err != nil {
			b.Fatal(err)
		}
	}
	hints := &batchVerifyHints{pool: pool}
	for _, batch := range []bool{false, true} {
		b.Run(fmt.Sprintf("batch=%v", batch), func(b *testing.B) {
			var source SenderHintSource = scalarVerifyHints{hints}
			if batch {
				source = hints
			}
			b.ReportAllocs()
			b.ResetTimer()
			for j := 0; j < b.N; j++ {
				b.StopTimer()
				wire := make([]*transaction.Transaction, len(txs))
				for i, raw := range encoded {
					var err error
					wire[i], err = transaction.DecodeEthereumTransaction(raw)
					if err != nil {
						b.Fatal(err)
					}
				}
				b.StartTimer()
				if n := applySenderHints(source, signer, wire); n != len(wire) {
					b.Fatalf("filled %d", n)
				}
			}
		})
	}
}
