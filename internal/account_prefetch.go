package internal

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/state"
)

const maxAccountPrefetchAddresses = 262144

var accountPrefetchWorkers = parseAccountPrefetchWorkers(os.Getenv("N42_ACCOUNT_PREFETCH_WORKERS"))

func parseAccountPrefetchWorkers(value string) int {
	n, err := strconv.Atoi(value)
	if err != nil || n < 1 {
		return 0
	}
	if n > 16 {
		return 16
	}
	return n
}

type accountSnapshotDB interface {
	BeginRo(context.Context) (kv.Tx, error)
}

// This reader is private to one serial block execution. Each prefetched value
// is handed to its first caller; subsequent reads go back to the pinned base
// snapshot. A caller may therefore mutate its returned account safely.
// Embedding PlainStateReader also preserves code, storage and enumeration APIs.
type blockAccountPrefetchReader struct {
	*state.PlainStateReader
	ctx      context.Context
	db       accountSnapshotDB
	viewID   uint64
	workers  int
	accounts map[types.Address]*account.StateAccount
	hits     uint64
}

var _ state.StateReader = (*blockAccountPrefetchReader)(nil)
var _ state.StorageEnumerator = (*blockAccountPrefetchReader)(nil)

func (r *blockAccountPrefetchReader) ReadAccountData(addr types.Address) (*account.StateAccount, error) {
	if a, ok := r.accounts[addr]; ok {
		delete(r.accounts, addr)
		r.hits++
		return a, nil
	}
	return r.PlainStateReader.ReadAccountData(addr)
}

// prepare runs only after the serial processor's sender verification. It is
// best effort: any snapshot change, read/decode error or cancellation discards
// the entire result and leaves execution using its original snapshot reader.
func (r *blockAccountPrefetchReader) prepare(txs transaction.Transactions) {
	r.accounts = nil
	if len(txs) < 2048 || len(txs) > maxAccountPrefetchAddresses {
		return
	}
	start := time.Now()
	addrs := make([]types.Address, 0, len(txs))
	for _, tx := range txs {
		if to := tx.To(); to != nil {
			addrs = append(addrs, *to)
		}
	}
	accounts, err := readSnapshotAccounts(r.ctx, r.db, r.viewID, addrs, r.workers)
	if err == nil {
		r.accounts = accounts
	}
	log.Info("account prefetch", "workers", r.workers, "accounts", len(accounts), "duration", time.Since(start), "fallback", err != nil, "err", err)
}

func readSnapshotAccounts(ctx context.Context, db accountSnapshotDB, viewID uint64, addresses []types.Address, workers int) (map[types.Address]*account.StateAccount, error) {
	if workers < 1 || workers > 16 || len(addresses) > maxAccountPrefetchAddresses {
		return nil, fmt.Errorf("account prefetch limits exceeded")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	order := append([]types.Address(nil), addresses...)
	sort.Slice(order, func(i, j int) bool { return bytes.Compare(order[i][:], order[j][:]) < 0 })
	// Deduplicate before splitting so each account has exactly one owned result.
	unique := order[:0]
	for _, addr := range order {
		if len(unique) == 0 || unique[len(unique)-1] != addr {
			unique = append(unique, addr)
		}
	}
	order = unique
	if len(order) == 0 {
		return nil, nil
	}
	if workers > len(order) {
		workers = len(order)
	}
	values := make([]*account.StateAccount, len(order))
	errs := make([]error, workers)
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			tx, err := db.BeginRo(workCtx)
			if err != nil {
				errs[worker] = err
				cancel()
				return
			}
			defer tx.Rollback()
			// ViewID is MDBX's read transaction ID, not merely the chain height.
			// Even a metadata-only commit causes fallback, keeping the proof exact.
			if tx.ViewID() != viewID {
				errs[worker] = fmt.Errorf("account prefetch snapshot changed")
				cancel()
				return
			}
			reader := state.NewPlainStateReader(tx)
			for i := len(order) * worker / workers; i < len(order)*(worker+1)/workers; i++ {
				if (i % 256) == 0 {
					if err := workCtx.Err(); err != nil {
						errs[worker] = err
						return
					}
				}
				values[i], err = reader.ReadAccountData(order[i])
				if err != nil {
					errs[worker] = err
					cancel()
					return
				}
			}
		}(w)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result := make(map[types.Address]*account.StateAccount, len(order))
	for i, addr := range order {
		result[addr] = values[i]
	}
	return result, nil
}
