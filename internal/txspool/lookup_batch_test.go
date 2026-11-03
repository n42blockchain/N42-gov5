package txspool

import (
	"sync"
	"testing"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
)

func TestTxLookupBatchSnapshot(t *testing.T) {
	lookup := newTxLookup()
	a, _ := queuedTx(t, 1)
	b, _ := queuedTx(t, 2)
	txs := []*transaction.Transaction{a, b}
	lookup.Add(txs[0], true)
	lookup.Add(txs[1], false)
	hashes := []types.Hash{txs[0].Hash(), types.Hash{0xfe}, txs[1].Hash(), txs[0].Hash()}
	dst := make([]*transaction.Transaction, len(hashes))
	lookup.GetBatch(hashes, dst)
	if dst[0] != txs[0] || dst[1] != nil || dst[2] != txs[1] || dst[3] != txs[0] {
		t.Fatal("incorrect snapshot")
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			lookup.Remove(hashes[0])
			lookup.Add(txs[0], true)
		}
	}()
	for i := 0; i < 100; i++ {
		lookup.GetBatch(hashes, dst)
		if dst[0] != dst[3] || dst[1] != nil || dst[2] != txs[1] {
			t.Fatal("inconsistent snapshot")
		}
	}
	wg.Wait()
	lookup.Remove(hashes[0])
	lookup.Remove(hashes[2])
	lookup.GetBatch(hashes, dst)
	for _, tx := range dst {
		if tx != nil {
			t.Fatal("stale output after removal")
		}
	}
}
