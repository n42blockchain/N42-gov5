package internal

import (
	"math/big"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
)

// TestRecoverBlockSendersAsyncOverlapsExecution pins the two properties the
// overlap depends on: the join returns only once every worker is done, and a
// caller that reads senders WHILE the workers run (which is what the execution
// loop now does) still gets the right address for every transaction. Run this
// under -race; the point of the change is that recovery and execution touch
// the same transactions concurrently.
func TestRecoverBlockSendersAsyncOverlapsExecution(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	want := crypto.PubkeyToAddress(key.PublicKey)
	signer := transaction.LatestSignerForChainID(big.NewInt(1))

	const n = 512
	txs := make([]*transaction.Transaction, n)
	for i := range txs {
		to := types.Address{byte(i), byte(i >> 8)}
		tx, err := transaction.SignTx(transaction.NewTx(&transaction.DynamicFeeTx{
			ChainID:   uint256.NewInt(1),
			Nonce:     uint64(i),
			GasTipCap: uint256.NewInt(1),
			GasFeeCap: uint256.NewInt(100),
			Gas:       21000,
			To:        &to,
			Value:     uint256.NewInt(1),
		}), signer, key)
		if err != nil {
			t.Fatal(err)
		}
		txs[i] = tx
	}

	join := recoverBlockSendersAsync(signer, txs)

	// Walk the transactions in order while the workers are still running, the
	// way the execution loop does. Every read must produce the right sender —
	// from a worker's memo if it got there first, by recovering inline if not.
	for i, tx := range txs {
		got, err := transaction.Sender(signer, tx)
		if err != nil {
			t.Fatalf("tx %d: %v", i, err)
		}
		if got != want {
			t.Fatalf("tx %d: sender %s, want %s", i, got.Hex(), want.Hex())
		}
	}

	join()
	join = func() {} // joining is not idempotent; the test owns it once

	for i, tx := range txs {
		got, err := transaction.Sender(signer, tx)
		if err != nil || got != want {
			t.Fatalf("tx %d after join: %s %v", i, got.Hex(), err)
		}
	}
}

// TestRecoverBlockSendersAsyncSmallBlockJoins covers the early returns: a block
// under the fan-out threshold must still hand back a join that does not block.
func TestRecoverBlockSendersAsyncSmallBlockJoins(t *testing.T) {
	signer := transaction.LatestSignerForChainID(big.NewInt(1))
	recoverBlockSendersAsync(signer, nil)()
	recoverBlockSendersAsync(nil, make([]*transaction.Transaction, 64))()
	recoverBlockSendersAsync(signer, make([]*transaction.Transaction, senderRecoveryMinTxs-1))()
}
