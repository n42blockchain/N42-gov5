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
// keystoreWallet adapter exposing a KeyStore entry as accounts.Wallet.
// Each instance wraps one Account + parent *KeyStore and reports
// Locked/Unlocked Status from the keystore.unlocked map. Open, Close,
// Derive and SelfDerive are no-ops; SignHash, SignData, SignText and
// SignTx (plus WithPassphrase variants) forward to the parent
// KeyStore after a Contains check, returning ErrUnknownAccount when
// the request targets an address outside this wallet.

package keystore

import (
	"math/big"

	"github.com/n42blockchain/N42/accounts"
	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/common/transaction"
)

// keystoreWallet implements the accounts.Wallet interface for the original
// keystore.
type keystoreWallet struct {
	account  accounts.Account // Single account contained in this wallet
	keystore *KeyStore        // Keystore where the account originates from
}

// URL implements accounts.Wallet, returning the URL of the account within.
func (w *keystoreWallet) URL() accounts.URL {
	return w.account.URL
}

// Status implements accounts.Wallet, returning whether the account held by the
// keystore wallet is unlocked or not.
func (w *keystoreWallet) Status() (string, error) {
	w.keystore.mu.RLock()
	defer w.keystore.mu.RUnlock()

	if _, ok := w.keystore.unlocked[w.account.Address]; ok {
		return "Unlocked", nil
	}
	return "Locked", nil
}

// Open implements accounts.Wallet, but is a noop for plain wallets since there
// is no connection or decryption step necessary to access the list of accounts.
func (w *keystoreWallet) Open(passphrase string) error { return nil }

// Close implements accounts.Wallet, but is a noop for plain wallets since there
// is no meaningful open operation.
func (w *keystoreWallet) Close() error { return nil }

// Accounts implements accounts.Wallet, returning an account list consisting of
// a single account that the plain keystore wallet contains.
func (w *keystoreWallet) Accounts() []accounts.Account {
	return []accounts.Account{w.account}
}

// Contains implements accounts.Wallet, returning whether a particular account is
// or is not wrapped by this wallet instance.
func (w *keystoreWallet) Contains(account accounts.Account) bool {
	return account.Address == w.account.Address && (account.URL == (accounts.URL{}) || account.URL == w.account.URL)
}

// Derive implements accounts.Wallet, but is a noop for plain wallets since there
// is no notion of hierarchical account derivation for plain keystore accounts.
func (w *keystoreWallet) Derive(path accounts.DerivationPath, pin bool) (accounts.Account, error) {
	return accounts.Account{}, accounts.ErrNotSupported
}

// SelfDerive implements accounts.Wallet, but is a noop for plain wallets since
// there is no notion of hierarchical account derivation for plain keystore accounts.
func (w *keystoreWallet) SelfDerive(bases []accounts.DerivationPath, chain common.AccountStateReader) {
}

// signHash signs the given hash with the given account.
// Returns ErrUnknownAccount if the wallet does not contain the account.
func (w *keystoreWallet) signHash(account accounts.Account, hash []byte) ([]byte, error) {
	if !w.Contains(account) {
		return nil, accounts.ErrUnknownAccount
	}
	return w.keystore.SignHash(account, hash)
}

// SignData signs keccak256(data). The mimetype parameter describes the type of data being signed.
func (w *keystoreWallet) SignData(account accounts.Account, mimeType string, data []byte) ([]byte, error) {
	return w.signHash(account, crypto.Keccak256(data))
}

// SignDataWithPassphrase signs keccak256(data). The mimetype parameter describes the type of data being signed.
func (w *keystoreWallet) SignDataWithPassphrase(account accounts.Account, passphrase, mimeType string, data []byte) ([]byte, error) {
	if !w.Contains(account) {
		return nil, accounts.ErrUnknownAccount
	}
	return w.keystore.SignHashWithPassphrase(account, passphrase, crypto.Keccak256(data))
}

// SignText implements accounts.Wallet, attempting to sign the hash of
// the given text with the given account.
func (w *keystoreWallet) SignText(account accounts.Account, text []byte) ([]byte, error) {
	return w.signHash(account, accounts.TextHash(text))
}

// SignTextWithPassphrase implements accounts.Wallet, attempting to sign the
// hash of the given text with the given account using passphrase as extra authentication.
func (w *keystoreWallet) SignTextWithPassphrase(account accounts.Account, passphrase string, text []byte) ([]byte, error) {
	if !w.Contains(account) {
		return nil, accounts.ErrUnknownAccount
	}
	return w.keystore.SignHashWithPassphrase(account, passphrase, accounts.TextHash(text))
}

// SignTx implements accounts.Wallet, attempting to sign the given transaction
// with the given account. Returns ErrUnknownAccount if the wallet does not
// contain the account.
func (w *keystoreWallet) SignTx(account accounts.Account, tx *transaction.Transaction, chainID *big.Int) (*transaction.Transaction, error) {
	if !w.Contains(account) {
		return nil, accounts.ErrUnknownAccount
	}
	return w.keystore.SignTx(account, tx, chainID)
}

// SignTxWithPassphrase implements accounts.Wallet, attempting to sign the given
// transaction with the given account using passphrase as extra authentication.
func (w *keystoreWallet) SignTxWithPassphrase(account accounts.Account, passphrase string, tx *transaction.Transaction, chainID *big.Int) (*transaction.Transaction, error) {
	if !w.Contains(account) {
		return nil, accounts.ErrUnknownAccount
	}
	return w.keystore.SignTxWithPassphrase(account, passphrase, tx, chainID)
}
