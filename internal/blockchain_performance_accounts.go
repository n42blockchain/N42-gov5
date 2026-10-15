package internal

import (
	"context"
	"errors"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus/misc"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state"
)

// PerformanceCanonicalAccounts is one stable canonical PlainState snapshot.
// A speculative applied child cannot be used as a canonical nonce baseline.
type PerformanceCanonicalAccounts struct {
	Hash        types.Hash
	Number      uint64
	Accounts    map[types.Address]account.StateAccount
	NextBaseFee uint256.Int
}

func (bc *BlockChain) ReadPerformanceCanonicalAccounts(ctx context.Context, addresses []types.Address) (PerformanceCanonicalAccounts, error) {
	bc.lock.Lock()
	defer bc.lock.Unlock()
	var out PerformanceCanonicalAccounts
	if bc.chainConfig == nil || bc.chainConfig.Performance == nil || !bc.qmdbEnabled || !bc.canonicalByCommitOnly() || len(addresses) > 1000 {
		return out, errors.New("canonical accounts require bounded performance QMDB owner")
	}
	if err := bc.joinLagWrite(); err != nil {
		return out, err
	}
	tx, err := bc.ChainDB.BeginRo(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	out.Hash = rawdb.ReadHeadBlockHash(tx)
	number := rawdb.ReadHeaderNumber(tx, out.Hash)
	if number == nil {
		return out, errors.New("canonical accounts head missing")
	}
	out.Number = *number
	canonical, err := rawdb.ReadCanonicalHash(tx, out.Number)
	if err != nil {
		return out, err
	}
	if canonical != out.Hash {
		return out, errors.New("canonical accounts head mismatch")
	}
	an, ah, applied, err := rawdb.ReadQMDBApplied(tx)
	if err != nil {
		return out, err
	}
	if !applied && out.Number == 0 && out.Hash == bc.GenesisBlock().Hash() && bc.qmdbRootComputer != nil && bc.qmdbRootComputer.Root() == bc.GenesisBlock().StateRoot() {
		an, ah, applied = 0, out.Hash, true
	}
	if !applied || an != out.Number || ah != out.Hash {
		return out, errors.New("canonical accounts are not actual applied state; retry")
	}
	parent := rawdb.ReadHeader(tx, out.Hash, out.Number)
	if parent == nil || out.Number == ^uint64(0) || (bc.chainConfig.IsLondon(out.Number) && parent.BaseFee == nil) {
		return out, errors.New("canonical accounts fee parent missing")
	}
	if bc.chainConfig.IsLondon(out.Number + 1) {
		fee, overflow := uint256.FromBig(misc.CalcBaseFee(bc.chainConfig, parent))
		if overflow {
			return out, errors.New("canonical accounts base fee overflow")
		}
		out.NextBaseFee = *fee
	}
	reader := state.NewPlainStateReader(tx)
	out.Accounts = make(map[types.Address]account.StateAccount, len(addresses))
	for _, address := range addresses {
		a, err := reader.ReadAccountData(address)
		if err != nil {
			return out, err
		}
		value := account.NewAccount()
		if a != nil {
			value = *a
		}
		out.Accounts[address] = value
	}
	return out, nil
}
