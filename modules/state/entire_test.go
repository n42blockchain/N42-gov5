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

package state

import (
	"bytes"
	"errors"
	"sort"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
)

type errWriter struct{}

func (errWriter) Write([]byte) (int, error) {
	return 0, errors.New("write failed")
}

// =============================================================================
// HashCodes Tests
// =============================================================================

func TestHashCodesLen(t *testing.T) {
	codes := HashCodes{
		{Hash: types.HexToHash("0x01"), Code: []byte{0x01}},
		{Hash: types.HexToHash("0x02"), Code: []byte{0x02}},
		{Hash: types.HexToHash("0x03"), Code: []byte{0x03}},
	}

	if codes.Len() != 3 {
		t.Errorf("HashCodes.Len() = %d, want 3", codes.Len())
	}

	t.Logf("✓ HashCodes.Len works correctly")
}

func TestHashCodesLess(t *testing.T) {
	codes := HashCodes{
		{Hash: types.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000002"), Code: []byte{0x02}},
		{Hash: types.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000001"), Code: []byte{0x01}},
	}

	// 0x01 < 0x02, so Less(1, 0) should be true
	if !codes.Less(1, 0) {
		t.Error("HashCodes.Less should return true when first hash < second hash")
	}
	if codes.Less(0, 1) {
		t.Error("HashCodes.Less should return false when first hash > second hash")
	}

	t.Logf("✓ HashCodes.Less works correctly")
}

func TestHashCodesSwap(t *testing.T) {
	codes := HashCodes{
		{Hash: types.HexToHash("0x01"), Code: []byte{0x01}},
		{Hash: types.HexToHash("0x02"), Code: []byte{0x02}},
	}

	codes.Swap(0, 1)

	if !bytes.Equal(codes[0].Code, []byte{0x02}) || !bytes.Equal(codes[1].Code, []byte{0x01}) {
		t.Error("HashCodes.Swap failed to swap elements")
	}

	t.Logf("✓ HashCodes.Swap works correctly")
}

func TestHashCodesSort(t *testing.T) {
	codes := HashCodes{
		{Hash: types.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000003"), Code: []byte{0x03}},
		{Hash: types.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000001"), Code: []byte{0x01}},
		{Hash: types.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000002"), Code: []byte{0x02}},
	}

	sort.Sort(codes)

	// After sorting, should be 0x01, 0x02, 0x03
	if !bytes.Equal(codes[0].Code, []byte{0x01}) {
		t.Error("HashCodes sort failed: first element should be 0x01")
	}
	if !bytes.Equal(codes[1].Code, []byte{0x02}) {
		t.Error("HashCodes sort failed: second element should be 0x02")
	}
	if !bytes.Equal(codes[2].Code, []byte{0x03}) {
		t.Error("HashCodes sort failed: third element should be 0x03")
	}

	t.Logf("✓ HashCodes sorting works correctly")
}

func TestEncodeBeforeStateReturnsWriterError(t *testing.T) {
	err := EncodeBeforeState(errWriter{}, Items{{Key: []byte{0x01}, Value: []byte{0x02}}}, nil)
	if err == nil {
		t.Fatal("expected writer error")
	}
}

// =============================================================================
// Items Tests
// =============================================================================

func TestItemsLen(t *testing.T) {
	items := Items{
		{Key: []byte{0x01}, Value: []byte{0x01}},
		{Key: []byte{0x02}, Value: []byte{0x02}},
	}

	if items.Len() != 2 {
		t.Errorf("Items.Len() = %d, want 2", items.Len())
	}

	t.Logf("✓ Items.Len works correctly")
}

func TestItemsLess(t *testing.T) {
	items := Items{
		{Key: []byte{0x02}, Value: []byte{0x02}},
		{Key: []byte{0x01}, Value: []byte{0x01}},
	}

	// 0x01 < 0x02, so Less(1, 0) should be true
	if !items.Less(1, 0) {
		t.Error("Items.Less should return true when first key < second key")
	}
	if items.Less(0, 1) {
		t.Error("Items.Less should return false when first key > second key")
	}

	t.Logf("✓ Items.Less works correctly")
}

func TestItemsSwap(t *testing.T) {
	items := Items{
		{Key: []byte{0x01}, Value: []byte{0x01}},
		{Key: []byte{0x02}, Value: []byte{0x02}},
	}

	items.Swap(0, 1)

	if !bytes.Equal(items[0].Key, []byte{0x02}) || !bytes.Equal(items[1].Key, []byte{0x01}) {
		t.Error("Items.Swap failed to swap elements")
	}

	t.Logf("✓ Items.Swap works correctly")
}

func TestItemsSort(t *testing.T) {
	items := Items{
		{Key: []byte{0x03}, Value: []byte{0x03}},
		{Key: []byte{0x01}, Value: []byte{0x01}},
		{Key: []byte{0x02}, Value: []byte{0x02}},
	}

	sort.Sort(items)

	// After sorting, should be 0x01, 0x02, 0x03
	for i := 0; i < 3; i++ {
		if !bytes.Equal(items[i].Key, []byte{byte(i + 1)}) {
			t.Errorf("Items sort failed at index %d", i)
		}
	}

	t.Logf("✓ Items sorting works correctly")
}

// =============================================================================
// Snapshot Tests
// =============================================================================

func TestNewWritableSnapshot(t *testing.T) {
	snap := NewWritableSnapshot()

	if snap == nil {
		t.Fatal("NewWritableSnapshot should not return nil")
	}
	if !snap.CanWrite() {
		t.Error("NewWritableSnapshot should create writable snapshot")
	}
	if snap.Items == nil {
		t.Error("NewWritableSnapshot should initialize Items")
	}
	if snap.accounts == nil {
		t.Error("NewWritableSnapshot should initialize accounts map")
	}
	if snap.storage == nil {
		t.Error("NewWritableSnapshot should initialize storage map")
	}

	t.Logf("✓ NewWritableSnapshot works correctly")
}

func TestNewReadableSnapshot(t *testing.T) {
	snap := NewReadableSnapshot()

	if snap == nil {
		t.Fatal("NewReadableSnapshot should not return nil")
	}
	if snap.CanWrite() {
		t.Error("NewReadableSnapshot should create non-writable snapshot")
	}
	if snap.Items == nil {
		t.Error("NewReadableSnapshot should initialize Items")
	}

	t.Logf("✓ NewReadableSnapshot works correctly")
}

func TestSnapshotCanWrite(t *testing.T) {
	writable := NewWritableSnapshot()
	readable := NewReadableSnapshot()

	if !writable.CanWrite() {
		t.Error("Writable snapshot should return true for CanWrite")
	}
	if readable.CanWrite() {
		t.Error("Readable snapshot should return false for CanWrite")
	}

	t.Logf("✓ Snapshot.CanWrite works correctly")
}

func TestSnapshotSetHash(t *testing.T) {
	hash := types.HexToHash("0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef")

	// SetHash should work on readable snapshot
	readable := NewReadableSnapshot()
	readable.SetHash(hash)
	if readable.OutHash != hash {
		t.Error("SetHash should set OutHash on readable snapshot")
	}

	// SetHash should NOT work on writable snapshot
	writable := NewWritableSnapshot()
	originalHash := writable.OutHash
	writable.SetHash(hash)
	if writable.OutHash != originalHash {
		t.Error("SetHash should not change OutHash on writable snapshot")
	}

	t.Logf("✓ Snapshot.SetHash works correctly")
}

func TestSnapshotSetGetFun(t *testing.T) {
	snap := NewReadableSnapshot()

	testFun := func(table string, key []byte) ([]byte, error) {
		return []byte{0x01}, nil
	}

	snap.SetGetFun(testFun)

	// Verify function was set by checking if it's not nil
	if snap.getOneFun == nil {
		t.Error("SetGetFun should set the getOneFun field")
	}

	t.Logf("✓ Snapshot.SetGetFun works correctly")
}

func TestReadSnapshotDataEmpty(t *testing.T) {
	snap, err := ReadSnapshotData([]byte{})
	if err != nil {
		t.Fatalf("ReadSnapshotData with empty data should not error: %v", err)
	}
	if snap == nil {
		t.Fatal("ReadSnapshotData should return snapshot for empty data")
	}
	if snap.CanWrite() {
		t.Error("ReadSnapshotData should return readable snapshot")
	}

	t.Logf("✓ ReadSnapshotData handles empty data correctly")
}

// =============================================================================
// Entire Tests
// =============================================================================

func TestEntireClone(t *testing.T) {
	original := Entire{
		Header: &block.Header{
			Number:   uint256.NewInt(100),
			GasLimit: 8000000,
		},
		Proof:   types.HexToHash("0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"),
		Senders: []types.Address{{0x01}, {0x02}},
		Snap: &Snapshot{
			Items:    Items{{Key: []byte{0x01}, Value: []byte{0x01}}},
			OutHash:  types.Hash{0x01},
			accounts: make(map[string]int),
			storage:  make(map[string]int),
		},
	}

	cloned := original.Clone()

	// Verify header is cloned
	if cloned.Header == original.Header {
		t.Error("Clone should create new header pointer")
	}
	if cloned.Header.Number.Uint64() != original.Header.Number.Uint64() {
		t.Error("Clone should preserve header number")
	}

	// Verify proof is copied
	if cloned.Proof != original.Proof {
		t.Error("Clone should copy proof")
	}

	// Verify senders are same slice (shallow copy)
	if len(cloned.Senders) != len(original.Senders) {
		t.Error("Clone should preserve senders")
	}

	// Verify snap is cloned
	if cloned.Snap == original.Snap {
		t.Error("Clone should create new snap pointer")
	}

	t.Logf("✓ Entire.Clone works correctly")
}

func TestEntireCloneHandlesNilHeaderAndSnapshot(t *testing.T) {
	original := Entire{
		Proof:   types.HexToHash("0x01"),
		Senders: []types.Address{{0x01}},
	}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Clone() panicked: %v", r)
		}
	}()

	cloned := original.Clone()
	if cloned.Header != nil {
		t.Fatal("expected nil Header")
	}
	if cloned.Snap != nil {
		t.Fatal("expected nil Snap")
	}
	if cloned.Proof != original.Proof {
		t.Fatal("expected Proof to be copied")
	}
}

// =============================================================================
// Item Tests
// =============================================================================

func TestItemStruct(t *testing.T) {
	item := &Item{
		Key:   []byte{0x01, 0x02, 0x03},
		Value: []byte{0x04, 0x05, 0x06},
	}

	if !bytes.Equal(item.Key, []byte{0x01, 0x02, 0x03}) {
		t.Error("Item.Key mismatch")
	}
	if !bytes.Equal(item.Value, []byte{0x04, 0x05, 0x06}) {
		t.Error("Item.Value mismatch")
	}

	t.Logf("✓ Item struct works correctly")
}

// =============================================================================
// HashCode Tests
// =============================================================================

func TestHashCodeStruct(t *testing.T) {
	hash := types.HexToHash("0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef")
	hc := &HashCode{
		Hash: hash,
		Code: []byte{0x60, 0x00, 0x60, 0x00},
	}

	if hc.Hash != hash {
		t.Error("HashCode.Hash mismatch")
	}
	if !bytes.Equal(hc.Code, []byte{0x60, 0x00, 0x60, 0x00}) {
		t.Error("HashCode.Code mismatch")
	}

	t.Logf("✓ HashCode struct works correctly")
}

// =============================================================================
// EntireCode Tests
// =============================================================================

func TestEntireCodeStruct(t *testing.T) {
	ec := &EntireCode{
		CoinBase: types.HexToAddress("0x1234567890123456789012345678901234567890"),
		Entire: Entire{
			Header: &block.Header{Number: uint256.NewInt(100)},
		},
		Codes:   []*HashCode{{Hash: types.Hash{0x01}, Code: []byte{0x60}}},
		Headers: []*block.Header{{Number: uint256.NewInt(99)}},
		Rewards: []*block.Reward{{Address: types.Address{0x01}, Amount: uint256.NewInt(1000)}},
	}

	if ec.CoinBase == (types.Address{}) {
		t.Error("EntireCode.CoinBase should be set")
	}
	if ec.Entire.Header == nil {
		t.Error("EntireCode.Entire.Header should be set")
	}
	if len(ec.Codes) != 1 {
		t.Error("EntireCode.Codes length mismatch")
	}
	if len(ec.Headers) != 1 {
		t.Error("EntireCode.Headers length mismatch")
	}
	if len(ec.Rewards) != 1 {
		t.Error("EntireCode.Rewards length mismatch")
	}

	t.Logf("✓ EntireCode struct works correctly")
}

// =============================================================================
// Benchmark Tests
// =============================================================================

func BenchmarkHashCodesSort(b *testing.B) {
	// Create unsorted codes
	baseCodes := HashCodes{
		{Hash: types.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000003"), Code: []byte{0x03}},
		{Hash: types.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000001"), Code: []byte{0x01}},
		{Hash: types.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000002"), Code: []byte{0x02}},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		codes := make(HashCodes, len(baseCodes))
		copy(codes, baseCodes)
		sort.Sort(codes)
	}
}

func BenchmarkItemsSort(b *testing.B) {
	baseItems := Items{
		{Key: []byte{0x03}, Value: []byte{0x03}},
		{Key: []byte{0x01}, Value: []byte{0x01}},
		{Key: []byte{0x02}, Value: []byte{0x02}},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		items := make(Items, len(baseItems))
		copy(items, baseItems)
		sort.Sort(items)
	}
}

func BenchmarkNewWritableSnapshot(b *testing.B) {
	for i := 0; i < b.N; i++ {
		NewWritableSnapshot()
	}
}
