package sharedexec

import (
	"context"
	"errors"
	"sync"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
)

// Recover only from the signed transaction, never From, a sender memo or the
// corpus mapping. Workers read decoded transactions, never the database. The
// caller retains its read transaction/execute lock until every worker joins.
func recoverCanonicalSenders(ctx context.Context, txs []*transaction.Transaction, signer transaction.Signer, workers int) ([]types.Address, error) {
	if ctx == nil || signer == nil {
		return nil, errors.New("canonical sender recovery requires context and signer")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(txs) == 0 {
		return []types.Address{}, nil
	}
	if workers < 1 {
		workers = 1
	}
	if workers > 16 {
		workers = 16
	}
	if workers > len(txs) {
		workers = len(txs)
	}
	senders := make([]types.Address, len(txs))
	type recoveryFailure struct {
		index int
		err   error
	}
	failures := make([]recoveryFailure, workers)
	chunk := (len(txs) + workers - 1) / workers
	var wg sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		start, end := worker*chunk, (worker+1)*chunk
		if end > len(txs) {
			end = len(txs)
		}
		if start >= end {
			break
		}
		wg.Add(1)
		go func(worker, start, end int) {
			defer wg.Done()
			for index := start; index < end; index++ {
				if err := ctx.Err(); err != nil {
					failures[worker] = recoveryFailure{index, err}
					return
				}
				txn := txs[index]
				if txn == nil {
					failures[worker] = recoveryFailure{index, errors.New("nil canonical transaction")}
					return
				}
				from, err := signer.Sender(txn)
				if err == nil && txn.Nonce() == ^uint64(0) {
					err = errors.New("strict ingress canonical nonce overflow")
				}
				if err != nil {
					failures[worker] = recoveryFailure{index, err}
					return
				}
				senders[index] = from
			}
		}(worker, start, end)
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Preserve deterministic original-index error ordering across worker chunks.
	first := recoveryFailure{index: len(txs)}
	for _, failure := range failures {
		if failure.err != nil && failure.index < first.index {
			first = failure
		}
	}
	if first.err != nil {
		return nil, first.err
	}
	return senders, nil
}
