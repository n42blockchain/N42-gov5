package txspool

import (
	"context"
	"github.com/holiman/uint256"
	"github.com/ledgerwatch/erigon-lib/kv"
	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules"
)

type ReadState interface {
	GetNonce(types.Address) uint64
	GetBalance(types.Address) *uint256.Int
	State(types.Address) (*account.StateAccount, error)
}

type StateCli struct {
	db  kv.RoDB
	ctx context.Context
}

func StateClient(ctx context.Context, db kv.RoDB) ReadState {
	return &StateCli{
		db:  db,
		ctx: ctx,
	}
}

// GetNonce retrieves the nonce for the given address from the database.
// R1 fix: Log database errors instead of silently ignoring them.
// Returns 0 if the account doesn't exist or on error.
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
		if err := sc.DecodeForStorage(v); nil != err {
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

// GetBalance retrieves the balance for the given address from the database.
// R1 fix: Log database errors instead of silently ignoring them.
// Returns 0 if the account doesn't exist or on error.
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
		if err := sc.DecodeForStorage(v); nil != err {
			return err
		}
		balance = &sc.Balance
		return nil
	})
	if err != nil {
		log.Warn("Failed to get balance from database", "address", addr, "err", err)
	}
	return balance
}
// State retrieves the full state account for the given address.
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

		if err := s.DecodeForStorage(v); nil != err {
			return err
		}
		return nil
	})
	return s, err
}

// AccountInfo holds both nonce and balance for an account.
// P1 fix: Used for batch retrieval to reduce database round trips.
type AccountInfo struct {
	Nonce   uint64
	Balance *uint256.Int
}

// GetAccountsInfo retrieves nonce and balance for multiple addresses in a single transaction.
// P1 fix: Batch retrieval significantly reduces I/O overhead compared to individual queries.
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
				// Account doesn't exist, use zero values
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
