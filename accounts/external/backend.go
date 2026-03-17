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

package external

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"sync"

	"github.com/n42blockchain/N42/accounts"
	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/log"
	event "github.com/n42blockchain/N42/modules/event/v2"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
)

// ExternalBackend is an accounts.Backend that delegates all signing
// operations to an external Clef signer process via IPC JSON-RPC.
type ExternalBackend struct {
	signers []accounts.Wallet
}

// NewExternalBackend connects to the Clef IPC endpoint and returns
// a backend containing a single ExternalSigner wallet.
func NewExternalBackend(endpoint string) (*ExternalBackend, error) {
	signer, err := NewExternalSigner(endpoint)
	if err != nil {
		return nil, err
	}
	return &ExternalBackend{
		signers: []accounts.Wallet{signer},
	}, nil
}

// Wallets returns the list of wallets managed by this backend.
// For an external signer there is exactly one wallet per endpoint.
func (eb *ExternalBackend) Wallets() []accounts.Wallet {
	return eb.signers
}

// Subscribe creates an async subscription to receive wallet events.
// The external signer does not generate wallet arrival/departure events,
// so the subscription blocks until cancelled.
func (eb *ExternalBackend) Subscribe(sink chan<- accounts.WalletEvent) event.Subscription {
	return event.NewSubscription(func(quit <-chan struct{}) error {
		<-quit
		return nil
	})
}

// signTransactionResult is the JSON structure returned by the
// account_signTransaction RPC method on the Clef signer.
type signTransactionResult struct {
	Raw hexutil.Bytes            `json:"raw"`
	Tx  *transaction.Transaction `json:"tx"`
}

// ExternalSigner provides an accounts.Wallet implementation that proxies
// all signing requests to an external Clef process over IPC JSON-RPC.
type ExternalSigner struct {
	client   *jsonrpc.Client
	endpoint string
	status   string
	cacheMu  sync.RWMutex
	cache    []accounts.Account
}

// NewExternalSigner connects to the Clef endpoint and verifies
// reachability by calling account_version.
func NewExternalSigner(endpoint string) (*ExternalSigner, error) {
	client, err := jsonrpc.DialIPC(context.Background(), endpoint)
	if err != nil {
		return nil, fmt.Errorf("connect to external signer at %s: %w", endpoint, err)
	}
	extsigner := &ExternalSigner{
		client:   client,
		endpoint: endpoint,
	}
	// Verify the signer is reachable and record its version.
	version, err := extsigner.pingVersion()
	if err != nil {
		client.Close()
		return nil, fmt.Errorf("ping external signer at %s: %w", endpoint, err)
	}
	extsigner.status = fmt.Sprintf("ok [version=%v]", version)
	return extsigner, nil
}

// URL returns the canonical URL for this wallet.
func (s *ExternalSigner) URL() accounts.URL {
	return accounts.URL{
		Scheme: "extapi",
		Path:   s.endpoint,
	}
}

// Status returns a textual status description.
func (s *ExternalSigner) Status() (string, error) {
	return s.status, nil
}

// Open is not supported for external signers — the connection is
// established at construction time.
func (s *ExternalSigner) Open(passphrase string) error {
	return fmt.Errorf("operation not supported on external signers")
}

// Close closes the IPC connection to the external signer.
func (s *ExternalSigner) Close() error {
	if s.client != nil {
		s.client.Close()
	}
	return nil
}

// Accounts retrieves the list of accounts from the external signer
// and caches the result.
func (s *ExternalSigner) Accounts() []accounts.Account {
	var accts []accounts.Account
	res, err := s.listAccounts()
	if err != nil {
		log.Error("account listing failed", "error", err)
		return accts
	}
	for _, addr := range res {
		accts = append(accts, accounts.Account{
			URL: accounts.URL{
				Scheme: "extapi",
				Path:   s.endpoint,
			},
			Address: addr,
		})
	}
	s.cacheMu.Lock()
	s.cache = accts
	s.cacheMu.Unlock()
	return accts
}

// Contains returns whether the given account is managed by this signer.
// It uses Accounts() to populate the cache if needed, avoiding double-lock issues.
func (s *ExternalSigner) Contains(account accounts.Account) bool {
	accounts := s.Accounts() // handles its own locking and cache refresh
	for _, a := range accounts {
		if a.Address == account.Address {
			return true
		}
	}
	return false
}

// Derive is not supported for external signers (no HD derivation).
func (s *ExternalSigner) Derive(path accounts.DerivationPath, pin bool) (accounts.Account, error) {
	return accounts.Account{}, fmt.Errorf("operation not supported on external signers")
}

// SelfDerive is not supported for external signers.
func (s *ExternalSigner) SelfDerive(bases []accounts.DerivationPath, chain common.AccountStateReader) {
	log.Error("operation SelfDerive not supported on external signers")
}

// SignData signs the hash of the given data via the external signer.
// The mimeType describes the type of data being signed.
func (s *ExternalSigner) SignData(account accounts.Account, mimeType string, data []byte) ([]byte, error) {
	var res hexutil.Bytes
	if err := s.client.Call(&res, "account_signData",
		mimeType,
		account.Address,
		hexutil.Encode(data)); err != nil {
		return nil, err
	}
	// If V is on 27/28-form, convert to 0/1 for consensus use.
	if mimeType == accounts.MimetypeClique && len(res) > 64 && (res[64] == 27 || res[64] == 28) {
		res[64] -= 27
	}
	return res, nil
}

// SignDataWithPassphrase is not supported on external signers.
func (s *ExternalSigner) SignDataWithPassphrase(account accounts.Account, passphrase, mimeType string, data []byte) ([]byte, error) {
	return nil, fmt.Errorf("password-operations not supported on external signers")
}

// SignText signs the hash of the given text (with Ethereum prefix) via
// the external signer.
func (s *ExternalSigner) SignText(account accounts.Account, text []byte) ([]byte, error) {
	var signature hexutil.Bytes
	if err := s.client.Call(&signature, "account_signData",
		accounts.MimetypeTextPlain,
		account.Address,
		hexutil.Encode(text)); err != nil {
		return nil, err
	}
	if len(signature) > 64 && (signature[64] == 27 || signature[64] == 28) {
		signature[64] -= 27 // Transform V from Ethereum-legacy to 0/1
	}
	return signature, nil
}

// SignTextWithPassphrase is not supported on external signers.
func (s *ExternalSigner) SignTextWithPassphrase(account accounts.Account, passphrase string, hash []byte) ([]byte, error) {
	return nil, fmt.Errorf("password-operations not supported on external signers")
}

// SignTx sends the transaction to the external signer for signing.
// If chainID is nil or the transaction's chain ID is zero, the signer
// will assign the chain ID from its own configuration.
func (s *ExternalSigner) SignTx(account accounts.Account, tx *transaction.Transaction, chainID *big.Int) (*transaction.Transaction, error) {
	data := hexutil.Bytes(tx.Data())
	var to *types.Address
	if tx.To() != nil {
		t := *tx.To()
		to = &t
	}

	args := map[string]interface{}{
		"from":  account.Address,
		"data":  &data,
		"nonce": hexutil.Uint64(tx.Nonce()),
		"gas":   hexutil.Uint64(tx.Gas()),
	}
	if to != nil {
		args["to"] = to
	}

	// Set gas price fields based on transaction type.
	switch tx.Type() {
	case transaction.LegacyTxType:
		if tx.GasPrice() != nil {
			args["gasPrice"] = (*hexutil.Big)(tx.GasPrice().ToBig())
		}
	case transaction.AccessListTxType:
		if tx.GasPrice() != nil {
			args["gasPrice"] = (*hexutil.Big)(tx.GasPrice().ToBig())
		}
		args["accessList"] = tx.AccessList()
	case transaction.DynamicFeeTxType:
		if tx.GasFeeCap() != nil {
			args["maxFeePerGas"] = (*hexutil.Big)(tx.GasFeeCap().ToBig())
		}
		if tx.GasTipCap() != nil {
			args["maxPriorityFeePerGas"] = (*hexutil.Big)(tx.GasTipCap().ToBig())
		}
	default:
		return nil, fmt.Errorf("unsupported tx type %d", tx.Type())
	}

	if tx.Value() != nil {
		args["value"] = (*hexutil.Big)(tx.Value().ToBig())
	}

	// Determine the chain ID to send.
	if chainID != nil && chainID.Sign() != 0 {
		args["chainId"] = (*hexutil.Big)(chainID)
	}
	if tx.Type() != transaction.LegacyTxType && tx.ChainId() != nil && tx.ChainId().Sign() != 0 {
		args["chainId"] = (*hexutil.Big)(tx.ChainId().ToBig())
	}

	var res signTransactionResult
	if err := s.client.Call(&res, "account_signTransaction", args); err != nil {
		return nil, err
	}
	if len(res.Raw) > 0 {
		var signedTx transaction.Transaction
		if err := signedTx.Unmarshal(res.Raw); err != nil {
			return nil, fmt.Errorf("decode signed transaction: %w", err)
		}
		return &signedTx, nil
	}
	if res.Tx == nil {
		return nil, errors.New("external signer returned empty transaction result")
	}
	return res.Tx, nil
}

// SignTxWithPassphrase is not supported on external signers.
func (s *ExternalSigner) SignTxWithPassphrase(account accounts.Account, passphrase string, tx *transaction.Transaction, chainID *big.Int) (*transaction.Transaction, error) {
	return nil, fmt.Errorf("password-operations not supported on external signers")
}

// listAccounts calls the account_list RPC on the external signer.
func (s *ExternalSigner) listAccounts() ([]types.Address, error) {
	var res []types.Address
	if err := s.client.Call(&res, "account_list"); err != nil {
		return nil, err
	}
	return res, nil
}

// pingVersion calls the account_version RPC on the external signer
// and returns the version string.
func (s *ExternalSigner) pingVersion() (string, error) {
	var v string
	if err := s.client.Call(&v, "account_version"); err != nil {
		return "", err
	}
	return v, nil
}
