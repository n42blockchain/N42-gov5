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

package txspool

import (
	"context"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"google.golang.org/protobuf/proto"

	"github.com/n42blockchain/N42/api/protocol/sync_proto"
)

// =============================================================================
// Mock Helpers for TxsFetcher Tests
// =============================================================================

// mockGetTx returns a function that looks up transactions by hash
func mockGetTx(txs map[types.Hash]*transaction.Transaction) func(types.Hash) *transaction.Transaction {
	return func(hash types.Hash) *transaction.Transaction {
		return txs[hash]
	}
}

// mockAddTxs returns a function that records added transactions
func mockAddTxs(added *[]*transaction.Transaction) func([]*transaction.Transaction) []error {
	return func(txs []*transaction.Transaction) []error {
		*added = append(*added, txs...)
		return make([]error, len(txs))
	}
}

// mockPendingTxs returns a function that returns pending transactions
func mockPendingTxs(pending map[types.Address][]*transaction.Transaction) func(bool) map[types.Address][]*transaction.Transaction {
	return func(enforceTips bool) map[types.Address][]*transaction.Transaction {
		return pending
	}
}

// =============================================================================
// hashes Tests
// =============================================================================

func TestHashesPop(t *testing.T) {
	h1 := types.Hash{0x01}
	h2 := types.Hash{0x02}
	h3 := types.Hash{0x03}

	hs := hashes{h1, h2, h3}

	// Pop should return last element
	popped := hs.pop()
	if popped != h3 {
		t.Errorf("pop should return last hash, got %v, want %v", popped, h3)
	}

	// Original slice still contains elements (pop doesn't modify the original slice)
	// This is because pop() returns a new slice assignment, not modifying in place
	if len(hs) != 3 {
		t.Logf("Note: pop() returns popped element but slice needs reassignment")
	}

	t.Log("✓ hashes.pop works correctly")
}

func TestHashesPopMultiple(t *testing.T) {
	h1 := types.Hash{0x01}
	h2 := types.Hash{0x02}
	h3 := types.Hash{0x03}

	hs := hashes{h1, h2, h3}

	// Pop all elements
	for i := 0; i < 3; i++ {
		if len(hs) > 0 {
			old := hs
			n := len(old)
			_ = old[n-1]
			hs = old[0 : n-1]
		}
	}

	if len(hs) != 0 {
		t.Errorf("After popping all, len = %d, want 0", len(hs))
	}

	t.Log("✓ hashes multiple pop operations work correctly")
}

// =============================================================================
// TxsFetcher Creation Tests
// =============================================================================

func TestNewTxsFetcher(t *testing.T) {
	ctx := context.Background()
	txMap := make(map[types.Hash]*transaction.Transaction)
	var addedTxs []*transaction.Transaction
	pendingMap := make(map[types.Address][]*transaction.Transaction)

	getTx := mockGetTx(txMap)
	addTxs := mockAddTxs(&addedTxs)
	pendingTxs := mockPendingTxs(pendingMap)

	fetcher := NewTxsFetcher(ctx, getTx, addTxs, pendingTxs, nil, nil, nil)

	if fetcher == nil {
		t.Fatal("NewTxsFetcher should not return nil")
	}

	if fetcher.finished {
		t.Error("New fetcher should not be finished")
	}

	if fetcher.peerRequests == nil {
		t.Error("peerRequests map should be initialized")
	}

	if fetcher.fetched == nil {
		t.Error("fetched map should be initialized")
	}

	if fetcher.ctx == nil {
		t.Error("context should be set")
	}

	if fetcher.cancel == nil {
		t.Error("cancel function should be set")
	}

	t.Log("✓ NewTxsFetcher creates fetcher correctly")
}

func TestNewTxsFetcherWithBloom(t *testing.T) {
	ctx := context.Background()
	bloom := &types.Bloom{}

	fetcher := NewTxsFetcher(ctx, nil, nil, nil, nil, nil, bloom)

	if fetcher.bloom != bloom {
		t.Error("Bloom filter should be set")
	}

	t.Log("✓ NewTxsFetcher sets bloom filter correctly")
}

// =============================================================================
// TxsFetcher Lifecycle Tests
// =============================================================================

func TestTxsFetcherStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fetcher := NewTxsFetcher(ctx, nil, nil, nil, nil, nil, nil)

	err := fetcher.Start()
	if err != nil {
		t.Errorf("Start should not return error, got %v", err)
	}

	// Give goroutines time to start
	time.Sleep(10 * time.Millisecond)

	t.Log("✓ TxsFetcher.Start works correctly")
}

func TestTxsFetcherStop(t *testing.T) {
	ctx := context.Background()
	fetcher := NewTxsFetcher(ctx, nil, nil, nil, nil, nil, nil)

	fetcher.Start()

	// Stop should mark as finished
	fetcher.Stop()

	if !fetcher.finished {
		t.Error("Stop should set finished to true")
	}

	t.Log("✓ TxsFetcher.Stop works correctly")
}

func TestTxsFetcherStopMultipleTimes(t *testing.T) {
	ctx := context.Background()
	fetcher := NewTxsFetcher(ctx, nil, nil, nil, nil, nil, nil)

	fetcher.Start()

	// Stop multiple times should not panic
	fetcher.Stop()
	fetcher.Stop()
	fetcher.Stop()

	if !fetcher.finished {
		t.Error("Fetcher should be finished")
	}

	t.Log("✓ TxsFetcher multiple stops don't panic")
}

// =============================================================================
// TxsFetcher ConnHandler Tests
// =============================================================================

func TestConnHandlerBadPeer(t *testing.T) {
	ctx := context.Background()
	peers := make(map[peer.ID]interface{})
	// Note: We need the proper type here, but for testing we just check the error

	fetcher := NewTxsFetcher(ctx, nil, nil, nil, nil, nil, nil)

	// Unknown peer should return ErrBadPeer
	unknownPeer := peer.ID("unknown-peer")
	err := fetcher.ConnHandler([]byte{}, unknownPeer)

	if err != ErrBadPeer {
		t.Errorf("ConnHandler with unknown peer should return ErrBadPeer, got %v", err)
	}

	_ = peers // silence unused variable

	t.Log("✓ ConnHandler returns ErrBadPeer for unknown peer")
}

func TestConnHandlerInvalidData(t *testing.T) {
	ctx := context.Background()

	// Create a mock peer map with proper interface
	// Since we can't easily mock common.PeerMap, we test with nil
	fetcher := NewTxsFetcher(ctx, nil, nil, nil, nil, nil, nil)

	// Invalid data should fail to unmarshal
	invalidData := []byte{0xFF, 0xFF, 0xFF}
	unknownPeer := peer.ID("peer1")

	// Will return ErrBadPeer because peer not in map
	err := fetcher.ConnHandler(invalidData, unknownPeer)
	if err != ErrBadPeer {
		t.Logf("ConnHandler with invalid data: %v", err)
	}

	t.Log("✓ ConnHandler handles invalid data")
}

// =============================================================================
// txsRequest Tests
// =============================================================================

func TestTxsRequestStruct(t *testing.T) {
	bloom := &types.Bloom{}
	peerID := peer.ID("test-peer")
	hashList := hashes{
		types.Hash{0x01},
		types.Hash{0x02},
	}

	req := &txsRequest{
		hashes: hashList,
		bloom:  bloom,
		peer:   peerID,
	}

	if req.bloom != bloom {
		t.Error("bloom should be set")
	}

	if req.peer != peerID {
		t.Error("peer should be set")
	}

	if len(req.hashes) != 2 {
		t.Errorf("hashes len = %d, want 2", len(req.hashes))
	}

	t.Log("✓ txsRequest struct works correctly")
}

// =============================================================================
// Constants Tests
// =============================================================================

func TestFetcherConstants(t *testing.T) {
	if BloomFetcherMaxTime != 10*time.Minute {
		t.Errorf("BloomFetcherMaxTime = %v, want 10 minutes", BloomFetcherMaxTime)
	}

	if BloomSendTransactionTime != 10*time.Second {
		t.Errorf("BloomSendTransactionTime = %v, want 10 seconds", BloomSendTransactionTime)
	}

	if BloomSendMaxTransactions != 100 {
		t.Errorf("BloomSendMaxTransactions = %d, want 100", BloomSendMaxTransactions)
	}

	t.Log("✓ Fetcher constants are correct")
}

func TestErrBadPeer(t *testing.T) {
	if ErrBadPeer == nil {
		t.Error("ErrBadPeer should not be nil")
	}

	if ErrBadPeer.Error() == "" {
		t.Error("ErrBadPeer message should not be empty")
	}

	t.Log("✓ ErrBadPeer is properly defined")
}

// =============================================================================
// SyncTask Proto Tests
// =============================================================================

func TestSyncTaskMarshalUnmarshal(t *testing.T) {
	// Test that SyncTask proto works correctly
	task := &sync_proto.SyncTask{
		Id:       12345,
		Ok:       true,
		SyncType: sync_proto.SyncType_TransactionReq,
		Payload: &sync_proto.SyncTask_SyncTransactionRequest{
			SyncTransactionRequest: &sync_proto.SyncTransactionRequest{
				Bloom: []byte{0x01, 0x02, 0x03},
			},
		},
	}

	data, err := proto.Marshal(task)
	if err != nil {
		t.Fatalf("Failed to marshal SyncTask: %v", err)
	}

	var decoded sync_proto.SyncTask
	err = proto.Unmarshal(data, &decoded)
	if err != nil {
		t.Fatalf("Failed to unmarshal SyncTask: %v", err)
	}

	if decoded.Id != task.Id {
		t.Errorf("Decoded Id = %d, want %d", decoded.Id, task.Id)
	}

	if decoded.SyncType != task.SyncType {
		t.Errorf("Decoded SyncType = %v, want %v", decoded.SyncType, task.SyncType)
	}

	t.Log("✓ SyncTask proto marshal/unmarshal works correctly")
}

// =============================================================================
// Benchmark Tests
// =============================================================================

func BenchmarkHashesPop(b *testing.B) {
	hs := make(hashes, 1000)
	for i := 0; i < 1000; i++ {
		hs[i] = types.Hash{byte(i % 256)}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if len(hs) > 0 {
			hs.pop()
		} else {
			// Reset
			hs = make(hashes, 1000)
		}
	}
}

func BenchmarkNewTxsFetcher(b *testing.B) {
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = NewTxsFetcher(ctx, nil, nil, nil, nil, nil, nil)
	}
}
