package state

import (
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

// CanMergeEOAAccounts checks the transaction and observer boundary before any
// speculative reads. The caller must separately qualify the reader and rules.
func (sdb *IntraBlockState) CanMergeEOAAccounts() bool {
	if sdb.Error() != nil || sdb.journal.length() != 0 || len(sdb.validRevisions) != 0 || sdb.tracer != nil || sdb.balanceReadHook != nil || sdb.snap != nil {
		return false
	}
	for _, increase := range sdb.balanceInc {
		if !increase.transferred {
			return false
		}
	}
	return true
}

// MergeEOAAccounts installs a restricted speculative dirty set at a transaction
// boundary. It verifies the entire captured input still matches, and validates
// every output before writing. False, nil means unsupported or stale; no account
// writes have been installed. Reader errors remain sticky and fatal.
//
// The owner goroutine must call this without concurrent state access. The caller
// is responsible for proving execution, gas and receipt equivalence. Code,
// storage and per-transaction observers are unsupported. Ordinary setters and
// FinalizeTx preserve the original pre-block accounts and dirty bookkeeping.
func (sdb *IntraBlockState) MergeEOAAccounts(base *AccountSnapshot, changes map[types.Address]*account.StateAccount, rules *params.Rules) (bool, error) {
	if err := sdb.Error(); err != nil {
		return false, err
	}
	if base == nil || rules == nil || rules.IsAura || !sdb.CanMergeEOAAccounts() {
		return false, nil
	}
	// Validate output membership and metadata before loading any state objects.
	for addr, after := range changes {
		before, captured := base.accounts[addr]
		if !captured || (before != nil && !before.IsEmptyCodeHash()) {
			return false, nil
		}
		if after == nil {
			if !rules.IsSpuriousDragon || (before != nil && before.Nonce != 0) {
				return false, nil
			}
			continue
		}
		if !after.Initialised || after.CodeHash != emptyCodeHashH || (rules.IsSpuriousDragon && after.Nonce == 0 && after.Balance.IsZero()) {
			return false, nil
		}
		var root types.Hash
		if before != nil {
			root = before.Root
			if after.Nonce < before.Nonce {
				return false, nil
			}
		}
		if after.Root != root {
			return false, nil
		}
	}
	for _, addr := range sortedAddresses(base.accounts) {
		before := base.accounts[addr]
		current := sdb.getStateObject(addr)
		if err := sdb.Error(); err != nil {
			return false, err
		}
		if current == nil || current.deleted {
			if before != nil {
				return false, nil
			}
		} else if before == nil || current.data != *before {
			return false, nil
		}
	}
	// All addresses are now cached, including absence. With a clean boundary,
	// these EOA setters and the noop finalizer cannot discover further DB reads.
	for _, addr := range sortedAddresses(changes) {
		balance, nonce := uint256.Int{}, uint64(0)
		if after := changes[addr]; after != nil {
			balance, nonce = after.Balance, after.Nonce
		}
		sdb.SetBalance(addr, &balance)
		sdb.SetNonce(addr, nonce)
	}
	if err := sdb.FinalizeTx(rules, NewNoopWriter()); err != nil {
		return false, err // Fatal: never attempt serial fallback after installing.
	}
	return true, nil
}
