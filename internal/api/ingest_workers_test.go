package api

import (
	"fmt"
	"math/big"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
)

// signedTransferBatch builds n signed legacy transfers (one sender per
// entry, distinct nonces so none collide) and their wire-encoded bytes, for
// tests and the benchmark that need real decode+recovery work rather than
// stand-ins.
func signedTransferBatch(t testing.TB, n int) ([]hexutil.Bytes, []types.Address, transaction.Signer) {
	t.Helper()
	chainID := big.NewInt(94)
	signer := transaction.LatestSignerForChainID(chainID)
	to := types.Address{0xde, 0xad}

	inputs := make([]hexutil.Bytes, n)
	wantFrom := make([]types.Address, n)
	for i := 0; i < n; i++ {
		key, err := crypto.GenerateKey()
		if err != nil {
			t.Fatalf("key %d: %v", i, err)
		}
		wantFrom[i] = crypto.PubkeyToAddress(key.PublicKey)
		tx, err := transaction.SignNewTx(key, signer, &transaction.LegacyTx{
			Nonce:    0,
			GasPrice: uint256.NewInt(10_000_000_000),
			Gas:      21000,
			To:       &to,
			Value:    uint256.NewInt(1),
		})
		if err != nil {
			t.Fatalf("sign %d: %v", i, err)
		}
		enc, err := transaction.EncodeEthereumTransaction(tx)
		if err != nil {
			t.Fatalf("encode %d: %v", i, err)
		}
		inputs[i] = enc
	}
	return inputs, wantFrom, signer
}

// decodeAndRecover is the same shape of work BatchRawTransaction's own
// processOne closure does (decode + sender recovery), used directly here so
// the test exercises the real, non-trivial CPU path processBatchEntries is
// meant to parallelize -- not a stand-in that always succeeds instantly.
func decodeAndRecover(signer transaction.Signer) func(int, hexutil.Bytes) (*transaction.Transaction, error) {
	return func(_ int, t hexutil.Bytes) (*transaction.Transaction, error) {
		if len(t) == 0 {
			return nil, fmt.Errorf("empty transaction data")
		}
		tx, err := transaction.DecodeEthereumTransaction(t)
		if err != nil {
			return nil, err
		}
		from, err := transaction.Sender(signer, tx)
		if err != nil {
			return nil, err
		}
		tx.SetFrom(from)
		return tx, nil
	}
}

// TestProcessBatchEntriesOrderingIdenticalAcrossWorkerCounts is the ordering/
// errors-identical requirement: the same batch (mixed valid entries and one
// deliberately empty/invalid one) must produce the exact same per-index
// results -- same recovered sender, same error -- whether run serially or
// spread over 1, 8, or 32 workers.
func TestProcessBatchEntriesOrderingIdenticalAcrossWorkerCounts(t *testing.T) {
	const n = 37 // not a multiple of any worker count, on purpose
	inputs, wantFrom, signer := signedTransferBatch(t, n)
	// Corrupt one entry so the batch has a real error to compare, at a fixed
	// index so every run sees it at the same place.
	badIdx := 11
	inputs[badIdx] = hexutil.Bytes{0xff, 0xff, 0xff}

	var reference []ingestResult
	for _, workers := range []int{0, 1, 8, 32} {
		var jobs chan func()
		if workers > 1 {
			jobs = newIngestPool(workers)
		}
		got := processBatchEntries(inputs, workers, jobs, decodeAndRecover(signer))
		if len(got) != n {
			t.Fatalf("workers=%d: got %d results, want %d", workers, len(got), n)
		}
		for i, r := range got {
			if i == badIdx {
				if r.err == nil {
					t.Fatalf("workers=%d index %d: err = nil, want a decode error", workers, i)
				}
				continue
			}
			if r.err != nil {
				t.Fatalf("workers=%d index %d: err = %v, want nil", workers, i, r.err)
			}
			if r.tx == nil {
				t.Fatalf("workers=%d index %d: tx = nil, want a decoded transaction", workers, i)
			}
			from := r.tx.From()
			if from == nil || *from != wantFrom[i] {
				t.Fatalf("workers=%d index %d: from = %v, want %x", workers, i, from, wantFrom[i])
			}
		}
		if reference == nil {
			reference = got
			continue
		}
		for i := range got {
			gotErr, refErr := got[i].err, reference[i].err
			if (gotErr == nil) != (refErr == nil) {
				t.Fatalf("index %d: error presence differs between worker counts (got=%v ref=%v)", i, gotErr, refErr)
			}
		}
	}
}

// TestProcessBatchEntriesSerialWhenWorkersOff confirms workers<=1 (and a nil
// jobs channel) never touches the jobs channel at all -- passing a closed or
// nil channel must not panic or block, since the serial path must not send
// to it.
func TestProcessBatchEntriesSerialWhenWorkersOff(t *testing.T) {
	inputs, _, signer := signedTransferBatch(t, 5)
	for _, workers := range []int{0, 1} {
		got := processBatchEntries(inputs, workers, nil, decodeAndRecover(signer))
		if len(got) != 5 {
			t.Fatalf("workers=%d: got %d results, want 5", workers, len(got))
		}
		for i, r := range got {
			if r.err != nil {
				t.Fatalf("workers=%d index %d: unexpected error %v", workers, i, r.err)
			}
		}
	}
}

// TestProcessBatchEntriesSingleEntryStaysSerial: fewer than 2 inputs must
// take the serial path even with workers>1 and a live pool (nothing to gain
// from dispatching one job).
func TestProcessBatchEntriesSingleEntryStaysSerial(t *testing.T) {
	inputs, wantFrom, signer := signedTransferBatch(t, 1)
	jobs := newIngestPool(8)
	got := processBatchEntries(inputs, 8, jobs, decodeAndRecover(signer))
	if len(got) != 1 || got[0].err != nil {
		t.Fatalf("got = %+v, want one successful result", got)
	}
	from := got[0].tx.From()
	if from == nil || *from != wantFrom[0] {
		t.Fatalf("from = %v, want %x", from, wantFrom[0])
	}
}
