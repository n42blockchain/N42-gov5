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

package api

import (
	"context"
	"math/big"
	"strings"
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/miner/builder"
)

// mockBundleSubmitter implements BundleSubmitter with a real BundlePool.
type mockBundleSubmitter struct {
	pool *builder.BundlePool
}

func newMockBundleSubmitter() *mockBundleSubmitter {
	return &mockBundleSubmitter{
		pool: builder.NewBundlePool(),
	}
}

func (m *mockBundleSubmitter) BundlePool() *builder.BundlePool {
	return m.pool
}

// makeLegacyTxBytes creates a SIGNED legacy transaction in the wire format
// eth_sendBundle takes: Ethereum RLP, the same encoding eth_sendRawTransaction
// accepts and the same one a transaction arrives in inside a block. The sender
// is carried only by the signature — the bundle path recovers it during
// execution and never reads it from the payload.
func makeLegacyTxBytes(t *testing.T, nonce uint64, gasPrice uint64) []byte {
	t.Helper()
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}
	to := types.HexToAddress("0x1234567890abcdef1234567890abcdef12345678")
	from := crypto.PubkeyToAddress(key.PublicKey)
	inner := &transaction.LegacyTx{
		Nonce:    nonce,
		GasPrice: uint256.NewInt(gasPrice),
		Gas:      21000,
		To:       &to,
		Value:    uint256.NewInt(1000),
		From:     &from,
	}
	signed, err := transaction.SignTx(transaction.NewTx(inner), transaction.NewLondonSigner(big.NewInt(1)), key)
	if err != nil {
		t.Fatalf("failed to sign tx: %v", err)
	}
	data, err := transaction.EncodeEthereumTransaction(signed)
	if err != nil {
		t.Fatalf("failed to encode tx: %v", err)
	}
	return data
}

// TestSendBundleRejectsWireSuppliedSender pins the fix for an unauthenticated
// sender-forgery hole. eth_sendBundle used to decode with the protobuf codec,
// which reads the sender straight out of the payload; AsMessage then skips
// signature recovery for any transaction that already carries a sender, and a
// bundle never passes through the transaction pool, so nothing verified the
// signature at all. A caller could name any account as the sender and have the
// builder execute a transfer out of it. The payload below is exactly that
// attack: an unsigned transaction claiming to come from a victim address.
func TestSendBundleRejectsWireSuppliedSender(t *testing.T) {
	victim := types.HexToAddress("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	to := types.HexToAddress("0x1234567890abcdef1234567890abcdef12345678")
	forged := transaction.NewTransaction(0, victim, &to, uint256.NewInt(1000), 21000, uint256.NewInt(10), nil)
	payload, err := forged.Marshal() // protobuf codec: carries From on the wire
	if err != nil {
		t.Fatalf("failed to marshal forged tx: %v", err)
	}

	api := NewMevAPI(newMockBundleSubmitter())
	if _, err := api.SendBundle(context.Background(), SendBundleArgs{
		Txs:         [][]byte{payload},
		BlockNumber: 1,
	}); err == nil {
		t.Fatal("eth_sendBundle accepted a transaction with a wire-supplied sender")
	}
}

func TestSendBundle_Success(t *testing.T) {
	submitter := newMockBundleSubmitter()
	api := NewMevAPI(submitter)

	txData1 := makeLegacyTxBytes(t, 0, 10)
	txData2 := makeLegacyTxBytes(t, 1, 20)

	args := SendBundleArgs{
		Txs:         [][]byte{txData1, txData2},
		BlockNumber: 100,
	}

	result, err := api.SendBundle(context.Background(), args)
	if err != nil {
		t.Fatalf("SendBundle returned error: %v", err)
	}
	if result == nil {
		t.Fatal("SendBundle returned nil result")
	}
	if result.BundleHash == (types.Hash{}) {
		t.Error("expected non-zero bundle hash")
	}

	// Verify bundle was added to the pool.
	bundles := submitter.pool.GetBundles(100, 0)
	if len(bundles) != 1 {
		t.Fatalf("expected 1 bundle in pool for block 100, got %d", len(bundles))
	}
	if len(bundles[0].Txs) != 2 {
		t.Errorf("expected 2 txs in bundle, got %d", len(bundles[0].Txs))
	}
}

func TestSendBundle_EmptyTxs(t *testing.T) {
	submitter := newMockBundleSubmitter()
	api := NewMevAPI(submitter)

	args := SendBundleArgs{
		Txs:         [][]byte{},
		BlockNumber: 100,
	}

	result, err := api.SendBundle(context.Background(), args)
	if err == nil {
		t.Fatal("expected error for empty txs, got nil")
	}
	if result != nil {
		t.Errorf("expected nil result for empty txs, got %v", result)
	}
	if !strings.Contains(err.Error(), "at least one transaction") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestSendBundle_NilSubmitter(t *testing.T) {
	api := NewMevAPI(nil)

	args := SendBundleArgs{
		Txs:         [][]byte{{0x01}},
		BlockNumber: 100,
	}

	result, err := api.SendBundle(context.Background(), args)
	if err == nil {
		t.Fatal("expected error for nil submitter, got nil")
	}
	if result != nil {
		t.Errorf("expected nil result for nil submitter, got %v", result)
	}
	if !strings.Contains(err.Error(), "miner not available") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestSendBundle_InvalidTx(t *testing.T) {
	submitter := newMockBundleSubmitter()
	api := NewMevAPI(submitter)

	// Invalid raw bytes that cannot be decoded as a protobuf transaction.
	invalidRaw := []byte{0xff, 0xfe, 0xfd, 0xfc, 0xfb}

	args := SendBundleArgs{
		Txs:         [][]byte{invalidRaw},
		BlockNumber: 100,
	}

	result, err := api.SendBundle(context.Background(), args)
	if err == nil {
		t.Fatal("expected error for invalid tx bytes, got nil")
	}
	if result != nil {
		t.Errorf("expected nil result for invalid tx, got %v", result)
	}
	if !strings.Contains(err.Error(), "failed to decode transaction") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestSendBundle_NilBundlePool(t *testing.T) {
	// Create a submitter that returns nil BundlePool.
	submitter := &mockBundleSubmitter{pool: nil}
	api := NewMevAPI(submitter)

	txData := makeLegacyTxBytes(t, 0, 10)
	args := SendBundleArgs{
		Txs:         [][]byte{txData},
		BlockNumber: 100,
	}

	result, err := api.SendBundle(context.Background(), args)
	if err == nil {
		t.Fatal("expected error for nil bundle pool, got nil")
	}
	if result != nil {
		t.Errorf("expected nil result, got %v", result)
	}
	if !strings.Contains(err.Error(), "bundle pool not available") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestSendBundle_WithTimestampConstraints(t *testing.T) {
	submitter := newMockBundleSubmitter()
	api := NewMevAPI(submitter)

	txData := makeLegacyTxBytes(t, 0, 10)

	args := SendBundleArgs{
		Txs:          [][]byte{txData},
		BlockNumber:  100,
		MinTimestamp: 1000,
		MaxTimestamp: 2000,
	}

	result, err := api.SendBundle(context.Background(), args)
	if err != nil {
		t.Fatalf("SendBundle returned error: %v", err)
	}
	if result == nil {
		t.Fatal("SendBundle returned nil result")
	}

	// Bundle with timestamp constraints should be filterable.
	// Timestamp within range.
	bundles := submitter.pool.GetBundles(100, 1500)
	if len(bundles) != 1 {
		t.Errorf("expected 1 bundle for timestamp 1500 (in range), got %d", len(bundles))
	}

	// Timestamp before min — should filter out.
	bundles = submitter.pool.GetBundles(100, 500)
	if len(bundles) != 0 {
		t.Errorf("expected 0 bundles for timestamp 500 (before min), got %d", len(bundles))
	}

	// Timestamp after max — should filter out.
	bundles = submitter.pool.GetBundles(100, 3000)
	if len(bundles) != 0 {
		t.Errorf("expected 0 bundles for timestamp 3000 (after max), got %d", len(bundles))
	}
}

func TestSendBundle_WithRevertingTxHashes(t *testing.T) {
	submitter := newMockBundleSubmitter()
	api := NewMevAPI(submitter)

	txData := makeLegacyTxBytes(t, 0, 10)
	revertHash := types.HexToHash("0xdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef")

	args := SendBundleArgs{
		Txs:               [][]byte{txData},
		BlockNumber:        100,
		RevertingTxHashes:  []types.Hash{revertHash},
	}

	result, err := api.SendBundle(context.Background(), args)
	if err != nil {
		t.Fatalf("SendBundle returned error: %v", err)
	}
	if result == nil {
		t.Fatal("SendBundle returned nil result")
	}

	bundles := submitter.pool.GetBundles(100, 0)
	if len(bundles) != 1 {
		t.Fatalf("expected 1 bundle, got %d", len(bundles))
	}
	if !bundles[0].IsRevertAllowed(revertHash) {
		t.Error("expected revert hash to be allowed")
	}
	otherHash := types.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111")
	if bundles[0].IsRevertAllowed(otherHash) {
		t.Error("expected other hash to not be allowed for revert")
	}
}

func TestSendBundle_DeterministicHash(t *testing.T) {
	submitter := newMockBundleSubmitter()
	api := NewMevAPI(submitter)

	txData := makeLegacyTxBytes(t, 0, 10)

	args := SendBundleArgs{
		Txs:         [][]byte{txData},
		BlockNumber: 100,
	}

	result1, err := api.SendBundle(context.Background(), args)
	if err != nil {
		t.Fatalf("first SendBundle returned error: %v", err)
	}

	// Submit again with same tx data (different block to avoid eviction issues).
	args.BlockNumber = 101
	result2, err := api.SendBundle(context.Background(), args)
	if err != nil {
		t.Fatalf("second SendBundle returned error: %v", err)
	}

	// Same transaction content should produce the same bundle hash.
	if result1.BundleHash != result2.BundleHash {
		t.Errorf("expected deterministic bundle hash, got %s vs %s",
			result1.BundleHash.Hex(), result2.BundleHash.Hex())
	}
}
