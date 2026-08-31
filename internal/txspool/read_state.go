package txspool

import (
	"context"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules"
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
type StateCli struct {
	db  kv.RoDB
	ctx context.Context
}

func StateClient(ctx context.Context, db kv.RoDB) ReadState {
	return &StateCli{db: db, ctx: ctx}
}

func (c *StateCli) GetNonce(addr types.Address) uint64 {
	var nonce uint64
	err := c.db.View(c.ctx, func(tx kv.Tx) error {
		v, err := tx.GetOne(modules.Account, addr.Bytes())
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
	err := c.db.View(c.ctx, func(tx kv.Tx) error {
		v, err := tx.GetOne(modules.Account, addr.Bytes())
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
	err := c.db.View(c.ctx, func(tx kv.Tx) error {
		v, err := tx.GetOne(modules.Account, addr.Bytes())
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

	err := c.db.View(c.ctx, func(tx kv.Tx) error {
		for _, addr := range addrs {
			v, err := tx.GetOne(modules.Account, addr.Bytes())
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
