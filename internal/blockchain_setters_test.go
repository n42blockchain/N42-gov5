package internal

import (
	"testing"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
)

func TestBlockChainSetParallelEVM(t *testing.T) {
	bc := &BlockChain{}
	bc.SetParallelEVM(true)
	if !bc.parallelEVM {
		t.Fatalf("expected parallelEVM true")
	}
	bc.SetParallelEVM(false)
	if bc.parallelEVM {
		t.Fatalf("expected parallelEVM false")
	}
}

func TestBlockChainSetPrefetch(t *testing.T) {
	bc := &BlockChain{}
	bc.SetPrefetch(true)
	if !bc.prefetchEnabled {
		t.Fatalf("expected prefetchEnabled true")
	}
	bc.SetPrefetch(false)
	if bc.prefetchEnabled {
		t.Fatalf("expected prefetchEnabled false")
	}
}

type testTxIndexer struct {
	added map[uint64][]types.Hash
}

func (ix *testTxIndexer) Add(number uint64, hashes []types.Hash) {
	if ix.added == nil {
		ix.added = make(map[uint64][]types.Hash)
	}
	ix.added[number] = hashes
}

func TestBlockChainSetTxIndexer(t *testing.T) {
	bc := &BlockChain{}
	ix := &testTxIndexer{}
	bc.SetTxIndexer(ix)
	if bc.txIndexer == nil {
		t.Fatalf("expected txIndexer set")
	}
	bc.txIndexer.Add(5, []types.Hash{{0x01}})
	if len(ix.added[5]) != 1 {
		t.Fatalf("expected indexer to receive Add call")
	}
}

type testSenderHintSource struct {
	tx *transaction.Transaction
}

func (s *testSenderHintSource) GetTx(hash types.Hash) *transaction.Transaction {
	return s.tx
}

func TestBlockChainSetSenderHintSource(t *testing.T) {
	bc := &BlockChain{}
	src := &testSenderHintSource{}
	bc.SetSenderHintSource(src)
	if bc.senderHints == nil {
		t.Fatalf("expected senderHints set")
	}
}

func TestBlockChainSetExecutedHook(t *testing.T) {
	bc := &BlockChain{}
	called := false
	bc.SetExecutedHook(func(hash, txHash, parentHash types.Hash, number uint64, extra []byte) {
		called = true
	})
	if bc.executedHook == nil {
		t.Fatalf("expected executedHook set")
	}
	bc.executedHook(types.Hash{}, types.Hash{}, types.Hash{}, 1, nil)
	if !called {
		t.Fatalf("expected hook invoked")
	}
}
