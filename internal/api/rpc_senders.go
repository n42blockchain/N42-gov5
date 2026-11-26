package api

import (
	"runtime"
	"sync"

	"github.com/n42blockchain/N42/common/transaction"
)

// rpcSenderPrewarmMin is the transaction count from which a block's senders
// are recovered across goroutines before it is marshalled.
const rpcSenderPrewarmMin = 64

// prewarmRPCSenders recovers the senders of a block's transactions in
// parallel, through the same path rpcTransactionFrom takes, so the
// per-transaction marshalling that follows hits the memo. A block nobody has
// read since the node started (explorer catch-up) otherwise pays ~45 us of
// ecrecover per transaction on one goroutine: 54 ms for a 1.2k-tx block.
func prewarmRPCSenders(txs []*transaction.Transaction) {
	if len(txs) < rpcSenderPrewarmMin {
		return
	}
	workers := runtime.GOMAXPROCS(0) / 2
	if workers > 8 {
		workers = 8
	}
	if workers < 2 {
		return
	}
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(start int) {
			defer wg.Done()
			for i := start; i < len(txs); i += workers {
				if tx := txs[i]; tx != nil && tx.From() == nil {
					_ = rpcTransactionFrom(tx)
				}
			}
		}(w)
	}
	wg.Wait()
}
