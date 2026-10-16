package internal

import (
	"context"
	"errors"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/internal/consensus/misc"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state"
)

// ErrPerformanceCanonicalNotApplied identifies the retryable absence of a
// canonical PlainState snapshot. On this exact error, the returned Hash and
// Number are validated canonical metadata, but Accounts remains nil and must
// never be used as a nonce/balance baseline. Other storage/header errors remain
// fatal and do not grant this metadata contract.
var ErrPerformanceCanonicalNotApplied = errors.New("canonical accounts are not actual applied state; retry")

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
	if !applied {
		return out, errors.New("canonical accounts applied marker missing")
	}
	if an != out.Number || ah != out.Hash {
		return out, ErrPerformanceCanonicalNotApplied
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

// PerformanceParentAccounts is the actual applied parent's account snapshot,
// which may legitimately lead canonical state. It is never a nonce baseline
// for admitting new transactions.
type PerformanceParentAccounts struct {
	Hash        types.Hash
	Number      uint64
	Accounts    map[types.Address]account.StateAccount
	NextBaseFee uint256.Int
	GasLimit    uint64
	View        hotstuff.ViewNumber
}

// ReadPerformanceParentAccounts pins PlainState to one explicitly requested
// applied parent. Build must independently recheck that parent before execution.
func (bc *BlockChain) ReadPerformanceParentAccounts(ctx context.Context, hash types.Hash, number uint64, addresses []types.Address) (PerformanceParentAccounts, error) {
	bc.lock.Lock()
	defer bc.lock.Unlock()
	var out PerformanceParentAccounts
	if bc.chainConfig == nil || bc.chainConfig.Performance == nil || !bc.qmdbEnabled || !bc.canonicalByCommitOnly() || len(addresses) > 12500 || number == ^uint64(0) {
		return out, errors.New("parent accounts require bounded performance QMDB owner")
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if err := bc.joinLagWrite(); err != nil {
		return out, err
	}
	tx, err := bc.ChainDB.BeginRo(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	an, ah, applied, err := rawdb.ReadQMDBApplied(tx)
	if err != nil {
		return out, err
	}
	if !applied && number == 0 && hash == bc.GenesisBlock().Hash() && rawdb.ReadHeadBlockHash(tx) == hash && bc.qmdbRootComputer != nil && bc.qmdbRootComputer.Root() == bc.GenesisBlock().StateRoot() {
		an, ah, applied = 0, hash, true
	}
	if !applied || an != number || ah != hash {
		return out, errors.New("queue build parent is not actual applied state; retry applied child")
	}
	parent := rawdb.ReadHeader(tx, hash, number)
	if parent == nil || (bc.chainConfig.IsLondon(number) && parent.BaseFee == nil) {
		return out, errors.New("queue build parent header/fee missing")
	}
	out.Hash, out.Number, out.GasLimit = hash, number, parent.GasLimit
	if number > 0 {
		out.View, err = hotstuff.ExtractViewFromExtra(parent.Extra)
		if err != nil {
			return out, err
		}
	}
	if bc.chainConfig.IsLondon(number + 1) {
		fee, overflow := uint256.FromBig(misc.CalcBaseFee(bc.chainConfig, parent))
		if overflow || fee == nil {
			return out, errors.New("queue build parent fee overflow")
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
