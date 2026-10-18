package sharedexec

import (
	"context"
	"errors"
	"math/big"
	"reflect"
	"testing"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
)

func canonicalSenderFixture(t *testing.T, count int, nonceMax bool) ([]*transaction.Transaction, transaction.Signer, types.Address) {
	t.Helper()
	key, err := crypto.HexToECDSA("0000000000000000000000000000000000000000000000000000000000000023")
	if err != nil {
		t.Fatal(err)
	}
	signer := transaction.NewEIP155Signer(big.NewInt(4205001))
	to := types.Address{19: 9}
	txs := make([]*transaction.Transaction, count)
	for i := range txs {
		nonce := uint64(i)
		if nonceMax && i == count-1 {
			nonce = ^uint64(0)
		}
		txn, err := transaction.SignTx(transaction.NewTx(&transaction.LegacyTx{Nonce: nonce, Gas: 21000, GasPrice: uint256.NewInt(1), Value: uint256.NewInt(1), To: &to}), signer, key)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := transaction.EncodeEthereumTransaction(txn)
		if err != nil {
			t.Fatal(err)
		}
		txs[i], err = transaction.DecodeEthereumTransaction(raw)
		if err != nil {
			t.Fatal(err)
		}
	}
	return txs, signer, crypto.PubkeyToAddress(key.PublicKey)
}

// The direct signer must recover every input despite forged From/memo/cache.
// Counting is process-global, so this test deliberately does not run Parallel.
func TestCanonicalSendersBoundedWorkersIgnoreHintsAndRecoverEverySignature(t *testing.T) {
	txs, signer, want := canonicalSenderFixture(t, 64, false)
	fake := types.Address{19: 99}
	for _, txn := range txs {
		txn.SetFrom(fake)
		txn.SetSenderMemo(signer, fake)
	}
	var previous []types.Address
	for _, workers := range []int{1, 2, 16} {
		stop := transaction.CountSenderRecoveriesForTest()
		got, err := recoverCanonicalSenders(context.Background(), txs, signer, workers)
		count := stop()
		if err != nil || count != uint64(len(txs)) || len(got) != len(txs) {
			t.Fatalf("workers %d: recoveries %d, error %v", workers, count, err)
		}
		for _, sender := range got {
			if sender != want {
				t.Fatal("trusted forged hint instead of signed transaction")
			}
		}
		if previous != nil && !reflect.DeepEqual(got, previous) {
			t.Fatal("worker count changed ordered senders")
		}
		previous = got
	}
}

func TestCanonicalSendersRejectInvalidSignatureNonceAndCancellationWithoutPartialResult(t *testing.T) {
	for _, kind := range []string{"signature", "nonce", "nil", "canceled"} {
		t.Run(kind, func(t *testing.T) {
			txs, signer, _ := canonicalSenderFixture(t, 32, kind == "nonce")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch kind {
			case "signature":
				txs[31] = transaction.NewTx(&transaction.LegacyTx{Nonce: 31, Gas: 21000, GasPrice: uint256.NewInt(1), Value: uint256.NewInt(1), V: uint256.NewInt(27), R: new(uint256.Int), S: new(uint256.Int)})
			case "nil":
				txs[31] = nil
			case "canceled":
				cancel()
			}
			got, err := recoverCanonicalSenders(ctx, txs, signer, 16)
			if err == nil || got != nil {
				t.Fatal("invalid canonical prefix exposed partial senders", err)
			}
			if kind == "canceled" && !errors.Is(err, context.Canceled) {
				t.Fatal("owner cancellation lost", err)
			}
		})
	}
}

type gatedCanonicalSigner struct {
	transaction.Signer
	entered chan struct{}
	release <-chan struct{}
}

func (s gatedCanonicalSigner) Sender(txn *transaction.Transaction) (types.Address, error) {
	s.entered <- struct{}{}
	<-s.release
	return s.Signer.Sender(txn)
}

func TestCanonicalSendersCancellationJoinsAllWorkersBeforeReturning(t *testing.T) {
	txs, signer, _ := canonicalSenderFixture(t, 4, false)
	entered, release := make(chan struct{}, 2), make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		got, err := recoverCanonicalSenders(ctx, txs, gatedCanonicalSigner{signer, entered, release}, 2)
		if got != nil {
			finished <- errors.New("partial result after cancel")
			return
		}
		finished <- err
	}()
	for i := 0; i < 2; i++ {
		select {
		case <-entered:
		case <-time.After(time.Second):
			close(release)
			t.Fatal("worker failed to start")
		}
	}
	cancel()
	select {
	case <-finished:
		close(release)
		t.Fatal("returned while signer worker still held decoded input")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("workers failed to join")
	}
}

type indexedFailureSigner struct {
	transaction.Signer
	low, high    error
	highFinished chan struct{}
}

func (s indexedFailureSigner) Sender(txn *transaction.Transaction) (types.Address, error) {
	if txn.Nonce() == 0 {
		<-s.highFinished
		return types.Address{}, s.low
	}
	if txn.Nonce() == 16 {
		close(s.highFinished)
		return types.Address{}, s.high
	}
	return s.Signer.Sender(txn)
}

func TestCanonicalSendersFirstErrorUsesOriginalIndexNotCompletionOrder(t *testing.T) {
	txs, signer, _ := canonicalSenderFixture(t, 32, false)
	low, high := errors.New("low-index failure"), errors.New("high-index failure")
	got, err := recoverCanonicalSenders(context.Background(), txs, indexedFailureSigner{signer, low, high, make(chan struct{})}, 2)
	if got != nil || !errors.Is(err, low) {
		t.Fatal("completion order changed deterministic failure", err)
	}
	for _, workers := range []int{0, 100} {
		got, err := recoverCanonicalSenders(context.Background(), txs[:3], signer, workers)
		if err != nil || len(got) != 3 {
			t.Fatal("bounded worker edge differs", workers, err)
		}
	}
}
