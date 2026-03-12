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

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/big"
	"sort"
	"sync"

	"github.com/n42blockchain/N42/accounts"
	"github.com/n42blockchain/N42/accounts/keystore"
	"github.com/n42blockchain/N42/common/crypto"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
)

const clefVersion = "1.0.0"

// TransactionArgs represents the arguments for signing a transaction.
// Field types follow the Ethereum JSON-RPC convention (hex-encoded).
type TransactionArgs struct {
	From     types.Address  `json:"from"`
	To       *types.Address `json:"to"`
	Gas      hexutil.Uint64 `json:"gas"`
	GasPrice *hexutil.Big   `json:"gasPrice"`
	Value    *hexutil.Big   `json:"value"`
	Nonce    hexutil.Uint64 `json:"nonce"`
	Data     hexutil.Bytes  `json:"data"`
	ChainID  *hexutil.Big   `json:"chainId"`
}

// SignedTx is the result of a successful transaction signing.
type SignedTx struct {
	Raw hexutil.Bytes    `json:"raw"`
	Tx  *TransactionArgs `json:"tx"`
}

// TypedData represents EIP-712 typed structured data.
type TypedData struct {
	Types       map[string][]TypedDataField `json:"types"`
	PrimaryType string                      `json:"primaryType"`
	Domain      map[string]interface{}      `json:"domain"`
	Message     map[string]interface{}      `json:"message"`
}

// TypedDataField is a single field descriptor within an EIP-712 type.
type TypedDataField struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// SignerService is the core Clef signing daemon. It wraps a keystore and
// applies rule-based approval before signing any data or transaction.
// All RPC methods are safe for concurrent use.
type SignerService struct {
	keystore *keystore.KeyStore
	chainID  *big.Int
	rules    *RuleEngine
	auditLog *AuditLogger
	mu       sync.RWMutex
}

// NewSignerService creates a SignerService backed by the given keystore,
// chain ID, rule engine, and audit logger.
func NewSignerService(ks *keystore.KeyStore, chainID *big.Int, rules *RuleEngine, audit *AuditLogger) *SignerService {
	return &SignerService{
		keystore: ks,
		chainID:  chainID,
		rules:    rules,
		auditLog: audit,
	}
}

// SignTransaction signs a transaction after applying rule-based approval.
// The keystore account matching args.From must be unlocked.
func (s *SignerService) SignTransaction(ctx context.Context, args TransactionArgs) (*SignedTx, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Apply rules.
	approved, reason := s.rules.ApproveTransaction(&args)
	if s.auditLog != nil {
		s.auditLog.LogSignRequest("SignTransaction", args.From, approved, reason)
	}
	if !approved {
		return nil, fmt.Errorf("transaction rejected by rules: %s", reason)
	}

	// Determine the chain ID to use.
	chainID := s.chainID
	if args.ChainID != nil {
		chainID = args.ChainID.ToInt()
	}

	// Look up the account.
	acct := accounts.Account{Address: args.From}
	if !s.keystore.HasAddress(args.From) {
		return nil, fmt.Errorf("unknown account %s", args.From.Hex())
	}

	// Build a hash of the transaction fields for signing.
	// We construct a canonical representation and sign its Keccak-256 hash.
	txHash := s.hashTransactionArgs(&args, chainID)

	sig, err := s.keystore.SignHash(acct, txHash)
	if err != nil {
		return nil, fmt.Errorf("sign transaction: %w", err)
	}

	return &SignedTx{
		Raw: hexutil.Bytes(sig),
		Tx:  &args,
	}, nil
}

// hashTransactionArgs produces a Keccak-256 digest of the canonical
// transaction fields for signature purposes. Each field is length-prefixed
// (4-byte big-endian length) to prevent ambiguous concatenation.
func (s *SignerService) hashTransactionArgs(args *TransactionArgs, chainID *big.Int) []byte {
	var buf bytes.Buffer

	// writeField writes a 4-byte big-endian length prefix followed by data.
	writeField := func(data []byte) {
		var lb [4]byte
		binary.BigEndian.PutUint32(lb[:], uint32(len(data)))
		buf.Write(lb[:])
		buf.Write(data)
	}

	writeField(args.From[:])
	if args.To != nil {
		writeField(args.To[:])
	} else {
		writeField(nil)
	}
	writeField(new(big.Int).SetUint64(uint64(args.Gas)).Bytes())
	if args.GasPrice != nil {
		writeField(args.GasPrice.ToInt().Bytes())
	} else {
		writeField(nil)
	}
	if args.Value != nil {
		writeField(args.Value.ToInt().Bytes())
	} else {
		writeField(nil)
	}
	writeField(new(big.Int).SetUint64(uint64(args.Nonce)).Bytes())
	writeField(args.Data)
	if args.ChainID != nil {
		writeField(args.ChainID.ToInt().Bytes())
	} else if chainID != nil {
		writeField(chainID.Bytes())
	} else {
		writeField(nil)
	}

	return crypto.Keccak256(buf.Bytes())
}

// SignData signs arbitrary data after applying rule-based approval.
// The contentType parameter describes the MIME type of the data being signed.
func (s *SignerService) SignData(ctx context.Context, contentType string, addr types.Address, data hexutil.Bytes) (hexutil.Bytes, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	approved, reason := s.rules.ApproveSignData(addr, data)
	if s.auditLog != nil {
		s.auditLog.LogSignRequest("SignData", addr, approved, reason)
	}
	if !approved {
		return nil, fmt.Errorf("sign data rejected by rules: %s", reason)
	}

	if !s.keystore.HasAddress(addr) {
		return nil, fmt.Errorf("unknown account %s", addr.Hex())
	}

	acct := accounts.Account{Address: addr}

	var hash []byte
	switch contentType {
	case accounts.MimetypeTextPlain:
		hash = accounts.TextHash(data)
	default:
		hash = crypto.Keccak256(data)
	}

	sig, err := s.keystore.SignHash(acct, hash)
	if err != nil {
		return nil, fmt.Errorf("sign data: %w", err)
	}
	return hexutil.Bytes(sig), nil
}

// SignTypedData signs EIP-712 typed structured data.
// The typed data is hashed according to a simplified EIP-712 scheme and
// then signed with the private key of addr.
func (s *SignerService) SignTypedData(ctx context.Context, addr types.Address, typedData TypedData) (hexutil.Bytes, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	approved, reason := s.rules.ApproveSignData(addr, nil)
	if s.auditLog != nil {
		s.auditLog.LogSignRequest("SignTypedData", addr, approved, reason)
	}
	if !approved {
		return nil, fmt.Errorf("sign typed data rejected by rules: %s", reason)
	}

	if !s.keystore.HasAddress(addr) {
		return nil, fmt.Errorf("unknown account %s", addr.Hex())
	}

	// Compute a hash of the typed data using the EIP-712 prefix.
	dataHash := hashTypedData(typedData)

	acct := accounts.Account{Address: addr}
	sig, err := s.keystore.SignHash(acct, dataHash)
	if err != nil {
		return nil, fmt.Errorf("sign typed data: %w", err)
	}
	return hexutil.Bytes(sig), nil
}

// hashTypedData produces a Keccak-256 digest of the EIP-712 typed data
// using a simplified encoding: prefix + domainSeparator + messageHash.
// Map keys are sorted to ensure deterministic hashing.
func hashTypedData(td TypedData) []byte {
	// Domain separator: hash the primary type name and domain fields (sorted).
	var domainData []byte
	domainData = append(domainData, []byte(td.PrimaryType)...)
	domainKeys := make([]string, 0, len(td.Domain))
	for k := range td.Domain {
		domainKeys = append(domainKeys, k)
	}
	sort.Strings(domainKeys)
	for _, k := range domainKeys {
		domainData = append(domainData, []byte(k)...)
		domainData = append(domainData, []byte(fmt.Sprintf("%v", td.Domain[k]))...)
	}
	domainHash := crypto.Keccak256(domainData)

	// Message hash: hash all message fields (sorted).
	var msgData []byte
	msgKeys := make([]string, 0, len(td.Message))
	for k := range td.Message {
		msgKeys = append(msgKeys, k)
	}
	sort.Strings(msgKeys)
	for _, k := range msgKeys {
		msgData = append(msgData, []byte(k)...)
		msgData = append(msgData, []byte(fmt.Sprintf("%v", td.Message[k]))...)
	}
	msgHash := crypto.Keccak256(msgData)

	// EIP-712: "\x19\x01" ++ domainSeparator ++ hash(message)
	var rawData []byte
	rawData = append(rawData, []byte("\x19\x01")...)
	rawData = append(rawData, domainHash...)
	rawData = append(rawData, msgHash...)
	return crypto.Keccak256(rawData)
}

// ListAccounts returns all accounts managed by the keystore.
func (s *SignerService) ListAccounts(ctx context.Context) ([]types.Address, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	accts := s.keystore.Accounts()
	addrs := make([]types.Address, len(accts))
	for i, a := range accts {
		addrs[i] = a.Address
		if s.auditLog != nil {
			s.auditLog.LogAccountAccess("ListAccounts", a.Address)
		}
	}
	return addrs, nil
}

// NewAccount creates a new account in the keystore with a randomly generated
// passphrase. The passphrase is not returned or logged for security reasons;
// callers should use SetPassword to set a known passphrase afterward.
func (s *SignerService) NewAccount(ctx context.Context) (types.Address, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Generate a random passphrase for security — never use an empty passphrase.
	passBytes := make([]byte, 16)
	if _, err := rand.Read(passBytes); err != nil {
		return types.Address{}, fmt.Errorf("failed to generate passphrase: %w", err)
	}
	passphrase := hex.EncodeToString(passBytes)

	acct, err := s.keystore.NewAccount(passphrase)
	if err != nil {
		return types.Address{}, fmt.Errorf("create account: %w", err)
	}
	if s.auditLog != nil {
		s.auditLog.LogAccountAccess("NewAccount", acct.Address)
	}
	return acct.Address, nil
}

// Version returns the Clef signer version string.
func (s *SignerService) Version(ctx context.Context) (string, error) {
	return clefVersion, nil
}
