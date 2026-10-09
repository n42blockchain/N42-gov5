package txspool

import (
	"context"
	"os"
	"runtime"
	"strconv"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules"
	"golang.org/x/sync/semaphore"
)

// ReadState provides read access to account nonces and balances.
//
// GetAccountsInfo is the batch form and the one the ingest path uses: the
// single-address getters each open their own read transaction, so validating a
// batch of transactions through them costs two MDBX transaction opens per
// transaction — and the pool holds its lock across every one of them.
type ReadState interface {
	GetNonce(types.Address) uint64
	GetBalance(types.Address) *uint256.Int
	State(types.Address) (*account.StateAccount, error)
	GetAccountsInfo([]types.Address) map[types.Address]*AccountInfo
}

// StateCli implements ReadState using a read-only database.
//
// Every read goes through budget, a small semaphore of its own, before it
// asks the database for a read transaction. Ingest can put thousands of
// goroutines on this path at once (S86: 9,600-19,000 per node). Without the
// budget they all queue on the database's read-transaction limiter, which is
// FIFO and shared with the miner and the import: a Block-STM worker's
// transaction then waits behind every queued ingest read, and every permit
// is held by an ingest read. The budget keeps ingest to a small share of the
// limiter; the rest of the queue waits here, holding nothing.
type StateCli struct {
	db     kv.RoDB
	ctx    context.Context
	budget *semaphore.Weighted
}

// ingestReadBudget is the number of concurrent txpool reads: GOMAXPROCS,
// clamped to [4, 64] (the node's limiter is max(32, 8*GOMAXPROCS)), or
// N42_TXPOOL_READ_BUDGET when set to a positive integer.
func ingestReadBudget() int64 {
	if n, err := strconv.Atoi(os.Getenv("N42_TXPOOL_READ_BUDGET")); err == nil && n > 0 {
		return int64(n)
	}
	n := runtime.GOMAXPROCS(0)
	if n < 4 {
		n = 4
	}
	if n > 64 {
		n = 64
	}
	return int64(n)
}

func StateClient(ctx context.Context, db kv.RoDB) ReadState {
	return &StateCli{db: db, ctx: ctx, budget: semaphore.NewWeighted(ingestReadBudget())}
}

// view runs f in a read transaction under the ingest read budget.
func (c *StateCli) view(f func(tx kv.Tx) error) error {
	if c.budget != nil {
		if err := c.budget.Acquire(c.ctx, 1); err != nil {
			return err
		}
		defer c.budget.Release(1)
	}
	return c.db.View(c.ctx, f)
}

func (c *StateCli) GetNonce(addr types.Address) uint64 {
	var nonce uint64
	err := c.view(func(tx kv.Tx) error {
		v, err := modules.ReadLatestAccount(tx, addr.Bytes())
		if err != nil {
			return err
		}
		if len(v) == 0 {
			return nil
		}
		sc := new(account.StateAccount)
		if err := sc.DecodeForStorage(v); err != nil {
			return err
		}
		nonce = uint64(sc.Nonce)
		return nil
	})
	if err != nil {
		log.Warn("Failed to get nonce from database", "address", addr, "err", err)
	}
	return nonce
}

func (c *StateCli) GetBalance(addr types.Address) *uint256.Int {
	balance := uint256.NewInt(0)
	err := c.view(func(tx kv.Tx) error {
		v, err := modules.ReadLatestAccount(tx, addr.Bytes())
		if err != nil {
			return err
		}
		if len(v) == 0 {
			return nil
		}
		sc := new(account.StateAccount)
		if err := sc.DecodeForStorage(v); err != nil {
			return err
		}
		balance = sc.Balance.Clone()
		return nil
	})
	if err != nil {
		log.Warn("Failed to get balance from database", "address", addr, "err", err)
	}
	return balance
}

func (c *StateCli) State(addr types.Address) (*account.StateAccount, error) {
	s := new(account.StateAccount)
	err := c.view(func(tx kv.Tx) error {
		v, err := modules.ReadLatestAccount(tx, addr.Bytes())
		if err != nil {
			return err
		}
		if len(v) == 0 {
			return nil
		}
		return s.DecodeForStorage(v)
	})
	return s, err
}

// AccountInfo holds both nonce and balance for an account.
type AccountInfo struct {
	Nonce   uint64
	Balance *uint256.Int
}

// GetAccountsInfo retrieves nonce and balance for multiple addresses in a single transaction.
func (c *StateCli) GetAccountsInfo(addrs []types.Address) map[types.Address]*AccountInfo {
	result := make(map[types.Address]*AccountInfo, len(addrs))

	err := c.view(func(tx kv.Tx) error {
		for _, addr := range addrs {
			v, err := modules.ReadLatestAccount(tx, addr.Bytes())
			if err != nil {
				log.Warn("Failed to get account info from database", "address", addr, "err", err)
				continue
			}
			if len(v) == 0 {
				result[addr] = &AccountInfo{
					Nonce:   0,
					Balance: uint256.NewInt(0),
				}
				continue
			}
			sc := new(account.StateAccount)
			if err := sc.DecodeForStorage(v); err != nil {
				log.Warn("Failed to decode account state", "address", addr, "err", err)
				continue
			}
			result[addr] = &AccountInfo{
				Nonce:   uint64(sc.Nonce),
				Balance: &sc.Balance,
			}
		}
		return nil
	})
	if err != nil {
		log.Warn("Failed to batch read accounts from database", "err", err)
	}
	return result
}
