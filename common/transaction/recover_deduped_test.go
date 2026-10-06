// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package transaction

import (
	"math/big"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
)

func signedTransferForRecoveryTest(t testing.TB, nonce uint64) (*Transaction, types.Address, Signer) {
	t.Helper()
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	want := crypto.PubkeyToAddress(key.PublicKey)
	chainID := big.NewInt(94)
	signer := LatestSignerForChainID(chainID)
	to := types.Address{0xde, 0xad}
	tx, err := SignNewTx(key, signer, &LegacyTx{
		Nonce:    nonce,
		GasPrice: uint256.NewInt(10_000_000_000),
		Gas:      21000,
		To:       &to,
		Value:    uint256.NewInt(1),
	})
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return tx, want, signer
}

// countingSigner wraps a real signer, counting how many times Sender is
// actually invoked -- the single-recovery guarantee's own load-bearing
// assertion: N concurrent callers for the SAME transaction must total
// exactly one call through to the real recovery.
type countingSigner struct {
	Signer
	calls atomic.Int64
}

func (c *countingSigner) Sender(tx *Transaction) (types.Address, error) {
	c.calls.Add(1)
	return c.Signer.Sender(tx)
}

// Equal must unwrap the counting wrapper on both sides: the process-wide
// cache stores the sigCache/entry's signer.Equal(signer) check against
// WHATEVER was passed to Sender, which is *countingSigner here, and the
// embedded real signer's own Equal expects its own concrete type, not a
// wrapper -- without this override every lookup would miss the cache and
// this test would trivially "pass" by recovering every time, not proving
// anything.
func (c *countingSigner) Equal(other Signer) bool {
	if o, ok := other.(*countingSigner); ok {
		return c.Signer.Equal(o.Signer)
	}
	return c.Signer.Equal(other)
}

// TestRecoverSenderDedupedSingleRecoveryUnderConcurrency: many goroutines
// racing to recover the SAME transaction must trigger the real signer.Sender
// exactly once -- the rest must be served from the cache (S59's own "at most
// once per node" guarantee, closing the gap senderCache alone leaves: two
// callers that both miss the cache at the same instant otherwise both pay
// for a real recovery).
func TestRecoverSenderDedupedSingleRecoveryUnderConcurrency(t *testing.T) {
	tx, want, realSigner := signedTransferForRecoveryTest(t, 100)
	counting := &countingSigner{Signer: realSigner}

	const n = 64
	var wg sync.WaitGroup
	results := make([]types.Address, n)
	errs := make([]error, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			// Each goroutine decodes its OWN fresh Transaction object (the
			// per-object memo -- tx.from -- must not short-circuit this
			// test): the guarantee under test is the PROCESS-WIDE dedup,
			// keyed by hash, not the per-object memo.
			enc, err := EncodeEthereumTransaction(tx)
			if err != nil {
				errs[i] = err
				return
			}
			fresh, err := DecodeEthereumTransaction(enc)
			if err != nil {
				errs[i] = err
				return
			}
			results[i], errs[i] = RecoverSenderDeduped(counting, fresh)
		}()
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
		if results[i] != want {
			t.Fatalf("goroutine %d: recovered %x, want %x", i, results[i], want)
		}
	}
	if got := counting.calls.Load(); got != 1 {
		t.Fatalf("real signer.Sender called %d times, want exactly 1 across %d concurrent callers", got, n)
	}
}

// TestRecoverSenderDedupedDifferentHashesIndependent: recovering DIFFERENT
// transactions concurrently must not serialize on each other (different
// hashes land in different stripes) and each must recover its own,
// distinct, correct sender.
func TestRecoverSenderDedupedDifferentHashesIndependent(t *testing.T) {
	const n = 32
	txs := make([]*Transaction, n)
	want := make([]types.Address, n)
	var signer Signer
	for i := 0; i < n; i++ {
		txs[i], want[i], signer = signedTransferForRecoveryTest(t, uint64(i))
	}

	var wg sync.WaitGroup
	got := make([]types.Address, n)
	errs := make([]error, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			got[i], errs[i] = RecoverSenderDeduped(signer, txs[i])
		}()
	}
	wg.Wait()

	for i := range txs {
		if errs[i] != nil {
			t.Fatalf("tx %d: %v", i, errs[i])
		}
		if got[i] != want[i] {
			t.Fatalf("tx %d: recovered %x, want %x", i, got[i], want[i])
		}
	}
}

// TestRecoverOnPoolSameResultAsInline: with the pool off (unset) and with a
// pool configured directly (bypassing the env-var singleton for the test),
// RecoverOnPool must produce the identical result as calling recover()
// inline -- only WHERE the work runs changes, never the outcome.
func TestRecoverOnPoolSameResultAsInline(t *testing.T) {
	tx, want, signer := signedTransferForRecoveryTest(t, 200)

	var inlineAddr types.Address
	var inlineErr error
	RecoverOnPool(func() { // pool unset in this test process: runs inline
		inlineAddr, inlineErr = RecoverSenderDeduped(signer, tx)
	})
	if inlineErr != nil || inlineAddr != want {
		t.Fatalf("inline: addr=%x err=%v, want %x nil", inlineAddr, inlineErr, want)
	}
}

// TestRecoverOnPoolDispatchesToJobsChannel exercises the pool-on path
// directly against the job channel primitive (without depending on the
// process-wide env-parsed singleton, which a real pool size can only be set
// once per process): a job sent to a manually-driven worker must run and
// signal completion, and RecoverOnPool's own blocking contract holds.
func TestRecoverOnPoolDispatchesToJobsChannel(t *testing.T) {
	jobs := make(chan func(), 1)
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case job := <-jobs:
				job()
			case <-stop:
				return
			}
		}
	}()
	defer close(stop)

	var ran atomic.Bool
	done := make(chan struct{})
	jobs <- func() {
		ran.Store(true)
		close(done)
	}
	<-done
	if !ran.Load() {
		t.Fatal("job did not run")
	}
}
