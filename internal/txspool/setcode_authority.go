// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.
//
// setcode_authority.go — EIP-7702 (SetCode) authorization pool restrictions,
// ported from go-ethereum's legacypool. Without these limits a single
// authority address can be reserved by many in-flight SetCode transactions,
// and a delegated account can stack transactions — both pool-spam / griefing
// vectors that become invalid at block inclusion.

package txspool

import (
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
)

// emptyCodeHash is keccak256(nil) — the code hash of a non-contract (and
// non-delegated) account. Matches common/account's unexported constant.
var emptyCodeHash = crypto.Keccak256Hash(nil)

// setCodeAuthorities returns the recovered authority addresses of an EIP-7702
// SetCode transaction. Non-SetCode transactions have no authorization list, so
// this returns nil for them. Authorizations whose signature cannot be recovered
// are skipped (they are rejected by full validation at inclusion time anyway).
func setCodeAuthorities(tx *transaction.Transaction) []types.Address {
	authList := tx.AuthList()
	if len(authList) == 0 {
		return nil
	}
	auths := make([]types.Address, 0, len(authList))
	for _, auth := range authList {
		if auth == nil {
			continue
		}
		addr, err := auth.RecoverSigner()
		if err != nil {
			continue
		}
		auths = append(auths, addr)
	}
	return auths
}

// validateAuth enforces the EIP-7702 authorization restrictions on a candidate
// transaction. Mirrors go-ethereum legacypool.validateAuth. The caller must
// hold pool.mu and should only invoke this once Prague (EIP-7702) is active.
func (pool *TxsPool) validateAuth(tx *transaction.Transaction) error {
	// Allow at most one in-flight tx for delegated accounts or those with a
	// pending authorization (sender side).
	if err := pool.checkDelegationLimit(tx); err != nil {
		return err
	}
	// For symmetry, an authority that already has an in-flight transaction
	// cannot be reserved by a new SetCode transaction (authority side).
	auths := setCodeAuthorities(tx)
	for _, auth := range auths {
		var count int
		if list := pool.pending[auth]; list != nil {
			count += list.Len()
		}
		if list := pool.queue[auth]; list != nil {
			count += list.Len()
		}
		if count > 1 {
			return ErrAuthorityReserved
		}
	}
	return nil
}

// checkDelegationLimit determines whether the tx sender is delegated (has 7702
// code) or has a pending delegation, and if so ensures the account keeps at
// most one in-flight executable transaction — no stacked or gapped nonces.
// Mirrors go-ethereum legacypool.checkDelegationLimit. Caller holds pool.mu.
func (pool *TxsPool) checkDelegationLimit(tx *transaction.Transaction) error {
	from, err := transaction.Sender(transaction.LatestSignerForChainID(pool.chainconfig.ChainID), tx)
	if err != nil {
		return ErrInvalidSender
	}

	// Short circuit if the sender is neither delegated nor an in-flight authority.
	if !pool.isDelegated(from) && !pool.all.hasAuth(from) {
		return nil
	}
	pending := pool.pending[from]
	if pending == nil {
		// A delegated account may not introduce a gapped (non-executable) nonce.
		if pool.pendingNonces.get(from) != tx.Nonce() {
			return ErrOutOfOrderTxFromDelegated
		}
		return nil
	}
	// Replacement of the existing in-flight tx is allowed; stacking is not.
	if pending.Contains(tx.Nonce()) {
		return nil
	}
	return ErrInflightTxLimitReached
}

// isDelegated reports whether addr currently holds an EIP-7702 delegation
// (i.e. has non-empty code). A missing/EOA account is not delegated.
func (pool *TxsPool) isDelegated(addr types.Address) bool {
	acc, err := pool.currentState.State(addr)
	if err != nil || acc == nil {
		return false
	}
	return acc.CodeHash != emptyCodeHash && acc.CodeHash != (types.Hash{})
}
