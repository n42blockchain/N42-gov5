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

package internal

import (
	"crypto/ecdsa"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

// mockStateReader implements state.StateReader for testing the prefetcher.
type mockStateReader struct {
	mu       sync.Mutex
	accounts map[types.Address]*account.StateAccount
	storage  map[types.Address]map[types.Hash][]byte
	code     map[types.Address][]byte

	// Track calls for assertions.
	readAccountCalls atomic.Int64
	readStorageCalls atomic.Int64
	readCodeCalls    atomic.Int64
}

func newMockStateReader() *mockStateReader {
	return &mockStateReader{
		accounts: make(map[types.Address]*account.StateAccount),
		storage:  make(map[types.Address]map[types.Hash][]byte),
		code:     make(map[types.Address][]byte),
	}
}

func (m *mockStateReader) ReadAccountData(address types.Address) (*account.StateAccount, error) {
	m.readAccountCalls.Add(1)
	m.mu.Lock()
	defer m.mu.Unlock()
	acc, ok := m.accounts[address]
	if !ok {
		return nil, nil
	}
	return acc, nil
}

func (m *mockStateReader) ReadAccountStorage(address types.Address, key *types.Hash) ([]byte, error) {
	m.readStorageCalls.Add(1)
	m.mu.Lock()
	defer m.mu.Unlock()
	slots, ok := m.storage[address]
	if !ok {
		return nil, nil
	}
	return slots[*key], nil
}

func (m *mockStateReader) ReadAccountCode(address types.Address, codeHash types.Hash) ([]byte, error) {
	m.readCodeCalls.Add(1)
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.code[address], nil
}

func (m *mockStateReader) ReadAccountCodeSize(address types.Address, codeHash types.Hash) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.code[address]), nil
}

// waitForAccountReads polls until the expected number of account reads is reached or timeout.
func (m *mockStateReader) waitForAccountReads(min int64, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if m.readAccountCalls.Load() >= min {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return m.readAccountCalls.Load() >= min
}

// waitForCodeReads polls until the expected number of code reads is reached or timeout.
func (m *mockStateReader) waitForCodeReads(min int64, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if m.readCodeCalls.Load() >= min {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return m.readCodeCalls.Load() >= min
}

// testChainConfig returns a chain config suitable for testing.
// Berlin is enabled so MakeSigner produces EIP2930Signer (supports access lists).
func testChainConfig() *params.ChainConfig {
	return params.TestChainConfig
}

// makeSignedTx creates a properly signed legacy transaction for testing.
func makeSignedTx(t *testing.T, nonce uint64, to *types.Address, signer transaction.Signer, key *ecdsa.PrivateKey) *transaction.Transaction {
	t.Helper()
	tx := transaction.NewTx(&transaction.LegacyTx{
		Nonce:    nonce,
		To:       to,
		Value:    uint256.NewInt(1000),
		Gas:      21000,
		GasPrice: uint256.NewInt(1),
		Data:     nil,
	})
	signed, err := transaction.SignTx(tx, signer, key)
	if err != nil {
		t.Fatalf("failed to sign tx: %v", err)
	}
	return signed
}

// makePrefetchBlock creates a block with the given header and transactions.
func makePrefetchBlock(header *block.Header, txs []*transaction.Transaction) *block.Block {
	iBlock := block.NewBlock(header, txs)
	return iBlock.(*block.Block)
}

func TestNewStatePrefetcher(t *testing.T) {
	config := testChainConfig()
	pf := NewStatePrefetcher(config)
	if pf == nil {
		t.Fatal("NewStatePrefetcher returned nil")
	}
	if pf.config != config {
		t.Error("config not set correctly")
	}
	// Close on a fresh prefetcher should not panic.
	pf.Close()
}

func TestPrefetcher_CloseWithoutPrefetch(t *testing.T) {
	pf := NewStatePrefetcher(testChainConfig())
	// Close without calling Prefetch should be safe (cancel is nil).
	pf.Close()
}

func TestPrefetcher_CloseIdempotent(t *testing.T) {
	pf := NewStatePrefetcher(testChainConfig())
	pf.Close()
	pf.Close() // Second close should not panic.
}

func TestPrefetcher_EmptyBlock(t *testing.T) {
	pf := NewStatePrefetcher(testChainConfig())

	header := &block.Header{
		Number:     uint256.NewInt(1),
		Difficulty: uint256.NewInt(1),
	}
	blk := makePrefetchBlock(header, nil)
	reader := newMockStateReader()

	// Prefetch with no transactions should return immediately (no goroutines started).
	pf.Prefetch(blk, reader)
	pf.Close()

	if reader.readAccountCalls.Load() != 0 {
		t.Errorf("expected 0 account reads for empty block, got %d", reader.readAccountCalls.Load())
	}
}

func TestPrefetcher_SkipsMalformedHeader(t *testing.T) {
	pf := NewStatePrefetcher(testChainConfig())

	blk := makePrefetchBlock(&block.Header{Difficulty: uint256.NewInt(1)}, []*transaction.Transaction{
		transaction.NewTransaction(0, types.Address{0x01}, nil, nil, 21000, nil, nil),
	})
	reader := newMockStateReader()

	pf.Prefetch(blk, reader)
	pf.Close()

	if reader.readAccountCalls.Load() != 0 {
		t.Fatalf("expected 0 account reads for malformed header, got %d", reader.readAccountCalls.Load())
	}
	if reader.readCodeCalls.Load() != 0 {
		t.Fatalf("expected 0 code reads for malformed header, got %d", reader.readCodeCalls.Load())
	}
}

func TestPrefetcher_PrefetchesSenderAndRecipient(t *testing.T) {
	config := testChainConfig()
	signer := transaction.MakeSigner(config, uint256.NewInt(0).ToBig())

	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}
	senderAddr := crypto.PubkeyToAddress(key.PublicKey)

	recipient := types.HexToAddress("0x1234567890abcdef1234567890abcdef12345678")

	reader := newMockStateReader()
	// Populate sender account so readAccount returns non-nil.
	reader.accounts[senderAddr] = &account.StateAccount{
		Nonce:   0,
		Balance: *uint256.NewInt(1000000),
	}
	// Populate recipient as a contract account with code.
	codeHash := types.HexToHash("0xdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	reader.accounts[recipient] = &account.StateAccount{
		Nonce:    1,
		Balance:  *uint256.NewInt(0),
		CodeHash: codeHash,
	}
	reader.code[recipient] = []byte{0x60, 0x00, 0x60, 0x00, 0xfd} // dummy code

	tx := makeSignedTx(t, 0, &recipient, signer, key)
	header := &block.Header{
		Number:     uint256.NewInt(0),
		Difficulty: uint256.NewInt(1),
		Coinbase:   types.HexToAddress("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
	}
	blk := makePrefetchBlock(header, []*transaction.Transaction{tx})

	pf := NewStatePrefetcher(config)
	pf.Prefetch(blk, reader)

	// Wait for workers to process before cancelling.
	// The prefetcher reads: sender, recipient, code, coinbase = 3 account reads + 1 code.
	if !reader.waitForAccountReads(3, 2*time.Second) {
		t.Errorf("timed out waiting for account reads, got %d", reader.readAccountCalls.Load())
	}
	if !reader.waitForCodeReads(1, 2*time.Second) {
		t.Errorf("timed out waiting for code reads, got %d", reader.readCodeCalls.Load())
	}

	pf.Close()

	// Verify metrics.
	if pf.accountsPrefetched.Load() < 3 {
		t.Errorf("expected accountsPrefetched >= 3, got %d", pf.accountsPrefetched.Load())
	}
	if pf.codePrefetched.Load() < 1 {
		t.Errorf("expected codePrefetched >= 1, got %d", pf.codePrefetched.Load())
	}
}

func TestPrefetcher_MultipleTxs(t *testing.T) {
	config := testChainConfig()
	signer := transaction.MakeSigner(config, uint256.NewInt(0).ToBig())

	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}
	senderAddr := crypto.PubkeyToAddress(key.PublicKey)

	reader := newMockStateReader()
	reader.accounts[senderAddr] = &account.StateAccount{
		Nonce:   0,
		Balance: *uint256.NewInt(1000000),
	}

	const numTxs = 10
	txs := make([]*transaction.Transaction, numTxs)
	for i := 0; i < numTxs; i++ {
		to := types.HexToAddress("0x" + "00" + string(rune('a'+i)) + "0000000000000000000000000000000000000000"[:38])
		txs[i] = makeSignedTx(t, uint64(i), &to, signer, key)
	}

	header := &block.Header{
		Number:     uint256.NewInt(0),
		Difficulty: uint256.NewInt(1),
	}
	blk := makePrefetchBlock(header, txs)

	pf := NewStatePrefetcher(config)
	pf.Prefetch(blk, reader)

	// Each tx prefetches: sender + recipient + coinbase = 3 per tx.
	// Wait for at least 2 reads per tx (sender + recipient).
	expectedMin := int64(numTxs) * 2
	if !reader.waitForAccountReads(expectedMin, 5*time.Second) {
		t.Errorf("expected at least %d account reads for %d txs, got %d",
			expectedMin, numTxs, reader.readAccountCalls.Load())
	}

	pf.Close()
}

func TestPrefetcher_ConcurrentPrefetchCalls(t *testing.T) {
	config := testChainConfig()
	signer := transaction.MakeSigner(config, uint256.NewInt(0).ToBig())

	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}
	senderAddr := crypto.PubkeyToAddress(key.PublicKey)

	reader := newMockStateReader()
	reader.accounts[senderAddr] = &account.StateAccount{
		Nonce:   0,
		Balance: *uint256.NewInt(1000000),
	}

	to := types.HexToAddress("0x1111111111111111111111111111111111111111")

	pf := NewStatePrefetcher(config)

	// Call Prefetch multiple times in sequence (simulating re-prefetch).
	// The prefetcher cancels the previous one before starting new.
	for i := 0; i < 5; i++ {
		tx := makeSignedTx(t, uint64(i), &to, signer, key)
		header := &block.Header{
			Number:     uint256.NewInt(uint64(i)),
			Difficulty: uint256.NewInt(1),
		}
		blk := makePrefetchBlock(header, []*transaction.Transaction{tx})
		pf.Prefetch(blk, reader)
	}

	// Should complete without deadlock or panic.
	pf.Close()
}

func TestPrefetcher_ContractCreationTx(t *testing.T) {
	config := testChainConfig()
	signer := transaction.MakeSigner(config, uint256.NewInt(0).ToBig())

	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}
	senderAddr := crypto.PubkeyToAddress(key.PublicKey)

	reader := newMockStateReader()
	reader.accounts[senderAddr] = &account.StateAccount{
		Nonce:   0,
		Balance: *uint256.NewInt(1000000),
	}

	// Contract creation: To is nil.
	tx := makeSignedTx(t, 0, nil, signer, key)
	header := &block.Header{
		Number:     uint256.NewInt(0),
		Difficulty: uint256.NewInt(1),
	}
	blk := makePrefetchBlock(header, []*transaction.Transaction{tx})

	pf := NewStatePrefetcher(config)
	pf.Prefetch(blk, reader)

	// Should have read sender + coinbase (no recipient for contract creation).
	if !reader.waitForAccountReads(2, 2*time.Second) {
		t.Errorf("expected at least 2 account reads (sender+coinbase), got %d",
			reader.readAccountCalls.Load())
	}

	pf.Close()
}

func TestPrefetcher_NilConfig(t *testing.T) {
	// NewStatePrefetcher with nil config should work for construction.
	// Prefetch will fail when MakeSigner is called, but it should not panic
	// because the workers recover from panics.
	pf := NewStatePrefetcher(nil)
	if pf == nil {
		t.Fatal("NewStatePrefetcher(nil) returned nil")
	}
	pf.Close()
}
