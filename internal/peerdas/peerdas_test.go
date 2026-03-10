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

package peerdas

import (
	"context"
	"crypto/rand"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
)

func init() {
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
}

// randomBytes returns n random bytes.
func randomBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}

func randomHash() types.Hash {
	var h types.Hash
	_, _ = rand.Read(h[:])
	return h
}

// --- Custody tests ---

func TestCustodyColumns_Deterministic(t *testing.T) {
	nodeID := randomBytes(32)
	cols1 := CustodyColumns(nodeID, CustodyRequirement)
	cols2 := CustodyColumns(nodeID, CustodyRequirement)

	if len(cols1) != len(cols2) {
		t.Fatalf("different lengths: %d vs %d", len(cols1), len(cols2))
	}
	for i := range cols1 {
		if cols1[i] != cols2[i] {
			t.Fatalf("column %d differs: %d vs %d", i, cols1[i], cols2[i])
		}
	}
}

func TestCustodyColumns_Coverage(t *testing.T) {
	// With enough nodes, all 128 columns should be covered.
	seen := make(map[uint64]bool)
	for i := 0; i < 500; i++ {
		nodeID := randomBytes(32)
		cols := CustodyColumns(nodeID, CustodyRequirement)
		for _, c := range cols {
			seen[c] = true
		}
	}

	// With 500 nodes each custodying 4 columns, 2000 assignments total.
	// Expected coverage per column ~= 1 - (124/128)^500 ≈ 1.0 (virtually certain).
	if len(seen) < NumberOfColumns {
		t.Errorf("only %d of %d columns covered with 500 nodes", len(seen), NumberOfColumns)
	}
}

func TestCustodyColumns_MinCount(t *testing.T) {
	nodeID := randomBytes(32)

	// Requesting fewer than CustodyRequirement should be clamped up.
	cols := CustodyColumns(nodeID, 1)
	if uint64(len(cols)) != CustodyRequirement {
		t.Fatalf("expected %d columns, got %d", CustodyRequirement, len(cols))
	}

	// Requesting more than NumberOfColumns should be clamped down.
	cols = CustodyColumns(nodeID, NumberOfColumns+100)
	if uint64(len(cols)) != NumberOfColumns {
		t.Fatalf("expected %d columns, got %d", NumberOfColumns, len(cols))
	}
}

func TestCustodyColumns_NoDuplicates(t *testing.T) {
	nodeID := randomBytes(32)
	cols := CustodyColumns(nodeID, 32)
	seen := make(map[uint64]bool)
	for _, c := range cols {
		if seen[c] {
			t.Fatalf("duplicate column index %d", c)
		}
		seen[c] = true
	}
}

func TestCustodyColumns_Sorted(t *testing.T) {
	nodeID := randomBytes(32)
	cols := CustodyColumns(nodeID, 16)
	for i := 1; i < len(cols); i++ {
		if cols[i] <= cols[i-1] {
			t.Fatalf("columns not sorted: index %d has %d <= %d", i, cols[i], cols[i-1])
		}
	}
}

// --- Sample tests ---

func TestSampleColumns_Count(t *testing.T) {
	blockHash := randomHash()
	cols := SampleColumns(blockHash, SamplesPerSlot)
	if len(cols) != SamplesPerSlot {
		t.Fatalf("expected %d samples, got %d", SamplesPerSlot, len(cols))
	}
}

func TestSampleColumns_Range(t *testing.T) {
	blockHash := randomHash()
	cols := SampleColumns(blockHash, SamplesPerSlot)
	for _, c := range cols {
		if c >= NumberOfColumns {
			t.Fatalf("sample column %d out of range [0, %d)", c, NumberOfColumns)
		}
	}
}

func TestSampleColumns_Deterministic(t *testing.T) {
	blockHash := randomHash()
	cols1 := SampleColumns(blockHash, SamplesPerSlot)
	cols2 := SampleColumns(blockHash, SamplesPerSlot)
	if len(cols1) != len(cols2) {
		t.Fatalf("different lengths")
	}
	for i := range cols1 {
		if cols1[i] != cols2[i] {
			t.Fatalf("sample %d differs: %d vs %d", i, cols1[i], cols2[i])
		}
	}
}

func TestSampleColumns_NoDuplicates(t *testing.T) {
	blockHash := randomHash()
	cols := SampleColumns(blockHash, 64)
	seen := make(map[uint64]bool)
	for _, c := range cols {
		if seen[c] {
			t.Fatalf("duplicate sample column %d", c)
		}
		seen[c] = true
	}
}

// --- Storage tests ---

func newTestDB(t *testing.T) kv.RwDB {
	t.Helper()
	return memdb.NewTestDB(t)
}

func makeTestColumn(blockHash types.Hash, colIdx uint64) *DataColumn {
	return &DataColumn{
		Index:       colIdx,
		BlockHash:   blockHash,
		BlockNumber: 42,
		Data: [][]byte{
			{0x01, 0x02, 0x03},
			{0x04, 0x05},
		},
		KZGProof: randomBytes(48),
	}
}

func TestColumnStorage_Roundtrip(t *testing.T) {
	db := newTestDB(t)
	blockHash := randomHash()
	col := makeTestColumn(blockHash, 7)

	ctx := context.Background()

	// Store
	if err := db.Update(ctx, func(tx kv.RwTx) error {
		return StoreColumn(tx, col)
	}); err != nil {
		t.Fatalf("StoreColumn: %v", err)
	}

	// Get
	var got *DataColumn
	if err := db.View(ctx, func(tx kv.Tx) error {
		var err error
		got, err = GetColumn(tx, blockHash, 7)
		return err
	}); err != nil {
		t.Fatalf("GetColumn: %v", err)
	}

	if got == nil {
		t.Fatal("GetColumn returned nil")
	}
	if got.Index != col.Index {
		t.Errorf("index: got %d, want %d", got.Index, col.Index)
	}
	if got.BlockHash != col.BlockHash {
		t.Errorf("block hash mismatch")
	}
	if got.BlockNumber != col.BlockNumber {
		t.Errorf("block number: got %d, want %d", got.BlockNumber, col.BlockNumber)
	}
	if len(got.Data) != len(col.Data) {
		t.Fatalf("data length: got %d, want %d", len(got.Data), len(col.Data))
	}
	for i := range got.Data {
		if string(got.Data[i]) != string(col.Data[i]) {
			t.Errorf("data[%d] mismatch", i)
		}
	}
	if string(got.KZGProof) != string(col.KZGProof) {
		t.Error("KZG proof mismatch")
	}

	// Has
	var has bool
	if err := db.View(ctx, func(tx kv.Tx) error {
		var err error
		has, err = HasColumn(tx, blockHash, 7)
		return err
	}); err != nil {
		t.Fatalf("HasColumn: %v", err)
	}
	if !has {
		t.Error("HasColumn returned false for stored column")
	}

	// Has for non-existent column
	if err := db.View(ctx, func(tx kv.Tx) error {
		var err error
		has, err = HasColumn(tx, blockHash, 99)
		return err
	}); err != nil {
		t.Fatalf("HasColumn: %v", err)
	}
	if has {
		t.Error("HasColumn returned true for non-existent column")
	}
}

func TestColumnStorage_GetNonExistent(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	blockHash := randomHash()

	var got *DataColumn
	if err := db.View(ctx, func(tx kv.Tx) error {
		var err error
		got, err = GetColumn(tx, blockHash, 0)
		return err
	}); err != nil {
		t.Fatalf("GetColumn: %v", err)
	}
	if got != nil {
		t.Fatal("expected nil for non-existent column")
	}
}

func TestColumnStorage_InvalidIndex(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	col := &DataColumn{
		Index:     NumberOfColumns, // out of range
		BlockHash: randomHash(),
		KZGProof:  []byte{0x01},
	}
	err := db.Update(ctx, func(tx kv.RwTx) error {
		return StoreColumn(tx, col)
	})
	if err == nil {
		t.Fatal("expected error for out-of-range column index")
	}
}

// --- Service / VerifyAvailability tests ---

func TestVerifyAvailability_AllPresent(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	blockHash := randomHash()
	nodeID := randomBytes(32)

	cfg := Config{
		Enable:          true,
		CustodyCount:    CustodyRequirement,
		SamplingEnabled: true,
		SampleCount:     SamplesPerSlot,
	}
	svc := NewService(cfg, nodeID, db)

	// Determine which columns will be sampled for this block.
	sampleCols := SampleColumns(blockHash, cfg.SampleCount)

	// Store all sampled columns.
	for _, colIdx := range sampleCols {
		col := makeTestColumn(blockHash, colIdx)
		if err := db.Update(ctx, func(tx kv.RwTx) error {
			return StoreColumn(tx, col)
		}); err != nil {
			t.Fatalf("store column %d: %v", colIdx, err)
		}
	}

	available, err := svc.VerifyAvailability(ctx, blockHash, 42)
	if err != nil {
		t.Fatalf("VerifyAvailability: %v", err)
	}
	if !available {
		t.Error("expected availability=true when all sampled columns are present")
	}
}

func TestVerifyAvailability_Missing(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	blockHash := randomHash()
	nodeID := randomBytes(32)

	cfg := Config{
		Enable:          true,
		CustodyCount:    CustodyRequirement,
		SamplingEnabled: true,
		SampleCount:     SamplesPerSlot,
	}
	svc := NewService(cfg, nodeID, db)

	// Don't store any columns.
	available, err := svc.VerifyAvailability(ctx, blockHash, 42)
	if err != nil {
		t.Fatalf("VerifyAvailability: %v", err)
	}
	if available {
		t.Error("expected availability=false when no columns are stored")
	}
}

func TestVerifyAvailability_Disabled(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	blockHash := randomHash()

	cfg := Config{Enable: false}
	svc := NewService(cfg, nil, db)

	_, err := svc.VerifyAvailability(ctx, blockHash, 42)
	if err != ErrServiceNotEnabled {
		t.Fatalf("expected ErrServiceNotEnabled, got %v", err)
	}
}

// --- DataColumn.Validate tests ---

func TestDataColumn_Validate(t *testing.T) {
	tests := []struct {
		name    string
		col     *DataColumn
		wantErr error
	}{
		{
			name:    "nil",
			col:     nil,
			wantErr: ErrNilColumn,
		},
		{
			name:    "index out of range",
			col:     &DataColumn{Index: NumberOfColumns, BlockHash: randomHash(), KZGProof: randomBytes(KZGProofLength), Data: [][]byte{{1}}},
			wantErr: ErrColumnIndexOutOfRange,
		},
		{
			name:    "empty block hash",
			col:     &DataColumn{Index: 0, KZGProof: randomBytes(KZGProofLength), Data: [][]byte{{1}}},
			wantErr: ErrEmptyBlockHash,
		},
		{
			name:    "empty proof",
			col:     &DataColumn{Index: 0, BlockHash: randomHash()},
			wantErr: ErrEmptyKZGProof,
		},
		{
			name:    "invalid proof length",
			col:     &DataColumn{Index: 0, BlockHash: randomHash(), KZGProof: []byte{1, 2, 3}},
			wantErr: ErrInvalidKZGProofLength,
		},
		{
			name:    "empty column data",
			col:     &DataColumn{Index: 0, BlockHash: randomHash(), KZGProof: randomBytes(KZGProofLength)},
			wantErr: ErrEmptyColumnData,
		},
		{
			name:    "valid",
			col:     &DataColumn{Index: 0, BlockHash: randomHash(), KZGProof: randomBytes(KZGProofLength), Data: [][]byte{{1, 2, 3}}},
			wantErr: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.col.Validate()
			if err != tt.wantErr {
				t.Errorf("got err=%v, want %v", err, tt.wantErr)
			}
		})
	}
}

// --- Service lifecycle tests ---

func TestService_IsCustodyColumn(t *testing.T) {
	nodeID := randomBytes(32)
	cfg := Config{
		Enable:       true,
		CustodyCount: CustodyRequirement,
	}
	svc := NewService(cfg, nodeID, nil)

	cols := svc.CustodyColumnIndices()
	if len(cols) == 0 {
		t.Fatal("no custody columns assigned")
	}

	// First custody column should return true.
	if !svc.IsCustodyColumn(cols[0]) {
		t.Error("IsCustodyColumn returned false for custodied column")
	}

	// Find a non-custodied column.
	custodySet := make(map[uint64]bool)
	for _, c := range cols {
		custodySet[c] = true
	}
	for i := uint64(0); i < NumberOfColumns; i++ {
		if !custodySet[i] {
			if svc.IsCustodyColumn(i) {
				t.Errorf("IsCustodyColumn returned true for non-custodied column %d", i)
			}
			break
		}
	}
}

func TestService_StartStop(t *testing.T) {
	db := newTestDB(t)
	cfg := Config{Enable: true, CustodyCount: CustodyRequirement}
	svc := NewService(cfg, randomBytes(32), db)

	ctx := context.Background()
	if err := svc.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Double start should fail.
	if err := svc.Start(ctx); err != ErrServiceAlreadyRunning {
		t.Fatalf("expected ErrServiceAlreadyRunning, got %v", err)
	}

	if err := svc.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// Stop again should be no-op.
	if err := svc.Stop(); err != nil {
		t.Fatalf("Stop again: %v", err)
	}
}

// --- mockBlockProvider for testing ---

type mockBlockProvider struct {
	hash   types.Hash
	number uint64
}

func (m *mockBlockProvider) CurrentBlock() (types.Hash, uint64) {
	return m.hash, m.number
}

func TestService_StartStopWithSampling(t *testing.T) {
	db := newTestDB(t)
	cfg := Config{
		Enable:          true,
		CustodyCount:    CustodyRequirement,
		SamplingEnabled: true,
		SampleCount:     SamplesPerSlot,
	}
	svc := NewService(cfg, randomBytes(32), db)
	svc.SetBlockProvider(&mockBlockProvider{hash: randomHash(), number: 100})

	ctx := context.Background()
	if err := svc.Start(ctx); err != nil {
		t.Fatalf("Start with sampling: %v", err)
	}

	// Service should be running with sampling loop active.
	if err := svc.Stop(); err != nil {
		t.Fatalf("Stop with sampling: %v", err)
	}
}

// --- KZG proof length validation in VerifyAvailability ---

func TestVerifyAvailability_InvalidKZGProofLength(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	blockHash := randomHash()
	nodeID := randomBytes(32)

	cfg := Config{
		Enable:          true,
		CustodyCount:    CustodyRequirement,
		SamplingEnabled: true,
		SampleCount:     SamplesPerSlot,
	}
	svc := NewService(cfg, nodeID, db)

	// Store columns with wrong KZG proof length (not 48 bytes).
	sampleCols := SampleColumns(blockHash, cfg.SampleCount)
	for _, colIdx := range sampleCols {
		col := &DataColumn{
			Index:       colIdx,
			BlockHash:   blockHash,
			BlockNumber: 42,
			Data:        [][]byte{{0x01, 0x02}},
			KZGProof:    randomBytes(32), // wrong length: 32 instead of 48
		}
		if err := db.Update(ctx, func(tx kv.RwTx) error {
			return StoreColumn(tx, col)
		}); err != nil {
			t.Fatalf("store column %d: %v", colIdx, err)
		}
	}

	available, err := svc.VerifyAvailability(ctx, blockHash, 42)
	if err != nil {
		t.Fatalf("VerifyAvailability: %v", err)
	}
	if available {
		t.Error("expected availability=false when KZG proofs have wrong length")
	}
}

func TestVerifyAvailability_EmptyColumnData(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	blockHash := randomHash()
	nodeID := randomBytes(32)

	cfg := Config{
		Enable:          true,
		CustodyCount:    CustodyRequirement,
		SamplingEnabled: true,
		SampleCount:     SamplesPerSlot,
	}
	svc := NewService(cfg, nodeID, db)

	// Store columns with correct KZG proof but empty data.
	sampleCols := SampleColumns(blockHash, cfg.SampleCount)
	for _, colIdx := range sampleCols {
		col := &DataColumn{
			Index:       colIdx,
			BlockHash:   blockHash,
			BlockNumber: 42,
			Data:        [][]byte{}, // empty data
			KZGProof:    randomBytes(KZGProofLength),
		}
		if err := db.Update(ctx, func(tx kv.RwTx) error {
			return StoreColumn(tx, col)
		}); err != nil {
			t.Fatalf("store column %d: %v", colIdx, err)
		}
	}

	available, err := svc.VerifyAvailability(ctx, blockHash, 42)
	if err != nil {
		t.Fatalf("VerifyAvailability: %v", err)
	}
	if available {
		t.Error("expected availability=false when column data is empty")
	}
}

// --- Sampling round test ---

func TestService_RunSamplingRound(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	blockHash := randomHash()
	nodeID := randomBytes(32)

	cfg := Config{
		Enable:          true,
		CustodyCount:    CustodyRequirement,
		SamplingEnabled: true,
		SampleCount:     SamplesPerSlot,
	}
	svc := NewService(cfg, nodeID, db)
	svc.SetBlockProvider(&mockBlockProvider{hash: blockHash, number: 100})

	// Store all sampled columns with valid data.
	sampleCols := SampleColumns(blockHash, cfg.SampleCount)
	for _, colIdx := range sampleCols {
		col := makeTestColumn(blockHash, colIdx)
		if err := db.Update(ctx, func(tx kv.RwTx) error {
			return StoreColumn(tx, col)
		}); err != nil {
			t.Fatalf("store column %d: %v", colIdx, err)
		}
	}

	// Run a sampling round — should pass without error.
	svc.runSamplingRound(ctx)

	// Verify lastSampledBlock was updated.
	if svc.lastSampledBlock != 100 {
		t.Errorf("lastSampledBlock: got %d, want 100", svc.lastSampledBlock)
	}

	// Running again for the same block should be a no-op (skip).
	svc.runSamplingRound(ctx)
}

// --- KZGProofLength constant test ---

func TestKZGProofLength(t *testing.T) {
	if KZGProofLength != 48 {
		t.Fatalf("KZGProofLength should be 48, got %d", KZGProofLength)
	}
}

// --- StoreDataColumn with enhanced validation ---

func TestStoreDataColumn_InvalidProofLength(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	nodeID := randomBytes(32)

	cfg := Config{
		Enable:       true,
		CustodyCount: CustodyRequirement,
	}
	svc := NewService(cfg, nodeID, db)

	col := &DataColumn{
		Index:       0,
		BlockHash:   randomHash(),
		BlockNumber: 1,
		Data:        [][]byte{{0x01}},
		KZGProof:    randomBytes(32), // wrong length
	}

	err := svc.StoreDataColumn(ctx, col)
	if err != ErrInvalidKZGProofLength {
		t.Fatalf("expected ErrInvalidKZGProofLength, got %v", err)
	}
}

func TestStoreDataColumn_EmptyData(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	nodeID := randomBytes(32)

	cfg := Config{
		Enable:       true,
		CustodyCount: CustodyRequirement,
	}
	svc := NewService(cfg, nodeID, db)

	col := &DataColumn{
		Index:       0,
		BlockHash:   randomHash(),
		BlockNumber: 1,
		Data:        nil, // empty data
		KZGProof:    randomBytes(KZGProofLength),
	}

	err := svc.StoreDataColumn(ctx, col)
	if err != ErrEmptyColumnData {
		t.Fatalf("expected ErrEmptyColumnData, got %v", err)
	}
}
