package sharedexec

import (
	"context"
	"fmt"
	"testing"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
)

func repeatedCanonicalTransactions(txs []*transaction.Transaction, n int) []*transaction.Transaction {
	out := make([]*transaction.Transaction, n)
	for i := range out {
		out[i] = txs[i%len(txs)]
	}
	return out
}

func TestCanonicalIngressSendersParallelBoundariesAndAliases(t *testing.T) {
	f, txs, signer, proofs := admittedCanonicalSenderFixture(t)
	for _, txn := range txs {
		txn.SetFrom(types.Address{19: 99})
		txn.SetSenderMemo(signer, types.Address{19: 98})
		_ = txn.Hash()
		_, _ = txn.EthEncoded()
	}
	for _, n := range []int{0, 4095, 4097} {
		for _, workers := range []int{0, 16, 32} {
			t.Run(fmt.Sprintf("transactions%d/workers%d", n, workers), func(t *testing.T) {
				// Repeated pointers span chunks: encoding must remain read-only,
				// even when metadata caches contain forged sender hints.
				input := repeatedCanonicalTransactions(txs, n)
				stop := transaction.CountSenderRecoveriesForTest()
				got, err := recoverCanonicalIngressSenders(context.Background(), input, signer, workers, f.chain.Config().ChainID, proofs)
				count := stop()
				if err != nil || count != 0 || len(got) != n {
					t.Fatalf("verified batch: recoveries=%d length=%d error=%v", count, len(got), err)
				}
				for i, sender := range got {
					if sender != f.from[(i%len(txs))/3] {
						t.Fatalf("sender %d differs from the admitted signer", i)
					}
				}
			})
		}
	}
}
