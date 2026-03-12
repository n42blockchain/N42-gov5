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

	"github.com/n42blockchain/N42/common/crypto/kzg"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	kv2 "github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
)

func init() {
	modules.N42Init()
	kv2.ChaindataTablesCfg = modules.N42TableCfg
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

// makeTestColumn creates a valid DataColumn with proper cell sizes.
func makeTestColumn(blockHash types.Hash, colIdx uint64) *DataColumn {
	cell := make([]byte, BytesPerCell)
	copy(cell, []byte{0x01, 0x02, 0x03})

	return &DataColumn{
		Index:       colIdx,
		BlockHash:   blockHash,
		BlockNumber: 42,
		Cells:       [][]byte{cell},
		KZGProofs:   [][]byte{randomBytes(KZGProofLength)},
		Commitments: [][]byte{randomBytes(KZGCommitmentLength)},
	}
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
	seen := make(map[uint64]bool)
	for i := 0; i < 500; i++ {
		nodeID := randomBytes(32)
		cols := CustodyColumns(nodeID, CustodyRequirement)
		for _, c := range cols {
			seen[c] = true
		}
	}
	if len(seen) < NumberOfColumns {
		t.Errorf("only %d of %d columns covered with 500 nodes", len(seen), NumberOfColumns)
	}
}

func TestCustodyColumns_MinCount(t *testing.T) {
	nodeID := randomBytes(32)
	cols := CustodyColumns(nodeID, 1)
	if uint64(len(cols)) != CustodyRequirement {
		t.Fatalf("expected %d columns, got %d", CustodyRequirement, len(cols))
	}
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

func newTestDB(t *testing.T) kv2.RwDB {
	t.Helper()
	return memdb.NewTestDB(t)
}

func TestColumnStorage_Roundtrip(t *testing.T) {
	db := newTestDB(t)
	blockHash := randomHash()
	col := makeTestColumn(blockHash, 7)

	ctx := context.Background()

	if err := db.Update(ctx, func(tx kv2.RwTx) error {
		return StoreColumn(tx, col)
	}); err != nil {
		t.Fatalf("StoreColumn: %v", err)
	}

	var got *DataColumn
	if err := db.View(ctx, func(tx kv2.Tx) error {
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
	if len(got.Cells) != len(col.Cells) {
		t.Fatalf("cells length: got %d, want %d", len(got.Cells), len(col.Cells))
	}
	for i := range got.Cells {
		if len(got.Cells[i]) != len(col.Cells[i]) {
			t.Errorf("cell[%d] length mismatch: got %d, want %d", i, len(got.Cells[i]), len(col.Cells[i]))
		}
	}
	if len(got.KZGProofs) != len(col.KZGProofs) {
		t.Fatalf("proofs length mismatch")
	}
	for i := range got.KZGProofs {
		if string(got.KZGProofs[i]) != string(col.KZGProofs[i]) {
			t.Errorf("proof[%d] mismatch", i)
		}
	}
	if len(got.Commitments) != len(col.Commitments) {
		t.Fatalf("commitments length mismatch")
	}
	for i := range got.Commitments {
		if string(got.Commitments[i]) != string(col.Commitments[i]) {
			t.Errorf("commitment[%d] mismatch", i)
		}
	}

	// Has
	var has bool
	if err := db.View(ctx, func(tx kv2.Tx) error {
		var err error
		has, err = HasColumn(tx, blockHash, 7)
		return err
	}); err != nil {
		t.Fatalf("HasColumn: %v", err)
	}
	if !has {
		t.Error("HasColumn returned false for stored column")
	}
}

func TestColumnStorage_GetNonExistent(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	blockHash := randomHash()

	var got *DataColumn
	if err := db.View(ctx, func(tx kv2.Tx) error {
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
	}
	err := db.Update(ctx, func(tx kv2.RwTx) error {
		return StoreColumn(tx, col)
	})
	if err == nil {
		t.Fatal("expected error for out-of-range column index")
	}
}

// --- Service / VerifyAvailability tests (skip KZG for unit tests) ---

func TestVerifyAvailability_AllPresent(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	blockHash := randomHash()
	nodeID := randomBytes(32)

	cfg := Config{
		Enable:              true,
		CustodyCount:        CustodyRequirement,
		SamplingEnabled:     true,
		SampleCount:         SamplesPerSlot,
		SkipKZGVerification: true,
	}
	svc := NewService(cfg, nodeID, db)

	sampleCols := SampleColumns(blockHash, cfg.SampleCount)
	for _, colIdx := range sampleCols {
		col := makeTestColumn(blockHash, colIdx)
		if err := db.Update(ctx, func(tx kv2.RwTx) error {
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
		Enable:              true,
		CustodyCount:        CustodyRequirement,
		SamplingEnabled:     true,
		SampleCount:         SamplesPerSlot,
		SkipKZGVerification: true,
	}
	svc := NewService(cfg, nodeID, db)

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
	validCell := make([]byte, BytesPerCell)
	validProof := randomBytes(KZGProofLength)
	validComm := randomBytes(KZGCommitmentLength)

	tests := []struct {
		name    string
		col     *DataColumn
		wantErr bool
	}{
		{
			name:    "nil",
			col:     nil,
			wantErr: true,
		},
		{
			name:    "index out of range",
			col:     &DataColumn{Index: NumberOfColumns, BlockHash: randomHash(), Cells: [][]byte{validCell}, KZGProofs: [][]byte{validProof}, Commitments: [][]byte{validComm}},
			wantErr: true,
		},
		{
			name:    "empty block hash",
			col:     &DataColumn{Index: 0, Cells: [][]byte{validCell}, KZGProofs: [][]byte{validProof}, Commitments: [][]byte{validComm}},
			wantErr: true,
		},
		{
			name:    "empty cells",
			col:     &DataColumn{Index: 0, BlockHash: randomHash(), KZGProofs: [][]byte{validProof}, Commitments: [][]byte{validComm}},
			wantErr: true,
		},
		{
			name:    "wrong cell size",
			col:     &DataColumn{Index: 0, BlockHash: randomHash(), Cells: [][]byte{{1, 2, 3}}, KZGProofs: [][]byte{validProof}, Commitments: [][]byte{validComm}},
			wantErr: true,
		},
		{
			name:    "proof count mismatch",
			col:     &DataColumn{Index: 0, BlockHash: randomHash(), Cells: [][]byte{validCell}, KZGProofs: [][]byte{}, Commitments: [][]byte{validComm}},
			wantErr: true,
		},
		{
			name:    "invalid proof length",
			col:     &DataColumn{Index: 0, BlockHash: randomHash(), Cells: [][]byte{validCell}, KZGProofs: [][]byte{{1, 2, 3}}, Commitments: [][]byte{validComm}},
			wantErr: true,
		},
		{
			name:    "commitment count mismatch",
			col:     &DataColumn{Index: 0, BlockHash: randomHash(), Cells: [][]byte{validCell}, KZGProofs: [][]byte{validProof}, Commitments: [][]byte{}},
			wantErr: true,
		},
		{
			name:    "invalid commitment length",
			col:     &DataColumn{Index: 0, BlockHash: randomHash(), Cells: [][]byte{validCell}, KZGProofs: [][]byte{validProof}, Commitments: [][]byte{{1, 2, 3}}},
			wantErr: true,
		},
		{
			name:    "valid",
			col:     &DataColumn{Index: 0, BlockHash: randomHash(), Cells: [][]byte{validCell}, KZGProofs: [][]byte{validProof}, Commitments: [][]byte{validComm}},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.col.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("got err=%v, wantErr=%v", err, tt.wantErr)
			}
		})
	}
}

// --- Service lifecycle tests ---

func TestService_IsCustodyColumn(t *testing.T) {
	nodeID := randomBytes(32)
	cfg := Config{
		Enable:              true,
		CustodyCount:        CustodyRequirement,
		SkipKZGVerification: true,
	}
	svc := NewService(cfg, nodeID, nil)

	cols := svc.CustodyColumnIndices()
	if len(cols) == 0 {
		t.Fatal("no custody columns assigned")
	}

	if !svc.IsCustodyColumn(cols[0]) {
		t.Error("IsCustodyColumn returned false for custodied column")
	}

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
	cfg := Config{Enable: true, CustodyCount: CustodyRequirement, SkipKZGVerification: true}
	svc := NewService(cfg, randomBytes(32), db)

	ctx := context.Background()
	if err := svc.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if err := svc.Start(ctx); err != ErrServiceAlreadyRunning {
		t.Fatalf("expected ErrServiceAlreadyRunning, got %v", err)
	}

	if err := svc.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

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
		Enable:              true,
		CustodyCount:        CustodyRequirement,
		SamplingEnabled:     true,
		SampleCount:         SamplesPerSlot,
		SkipKZGVerification: true,
	}
	svc := NewService(cfg, randomBytes(32), db)
	svc.SetBlockProvider(&mockBlockProvider{hash: randomHash(), number: 100})

	ctx := context.Background()
	if err := svc.Start(ctx); err != nil {
		t.Fatalf("Start with sampling: %v", err)
	}

	if err := svc.Stop(); err != nil {
		t.Fatalf("Stop with sampling: %v", err)
	}
}

// --- Sampling round test ---

func TestService_RunSamplingRound(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	blockHash := randomHash()
	nodeID := randomBytes(32)

	cfg := Config{
		Enable:              true,
		CustodyCount:        CustodyRequirement,
		SamplingEnabled:     true,
		SampleCount:         SamplesPerSlot,
		SkipKZGVerification: true,
	}
	svc := NewService(cfg, nodeID, db)
	svc.SetBlockProvider(&mockBlockProvider{hash: blockHash, number: 100})

	sampleCols := SampleColumns(blockHash, cfg.SampleCount)
	for _, colIdx := range sampleCols {
		col := makeTestColumn(blockHash, colIdx)
		if err := db.Update(ctx, func(tx kv2.RwTx) error {
			return StoreColumn(tx, col)
		}); err != nil {
			t.Fatalf("store column %d: %v", colIdx, err)
		}
	}

	svc.runSamplingRound(ctx)

	if svc.lastSampledBlock.Load() != 100 {
		t.Errorf("lastSampledBlock: got %d, want 100", svc.lastSampledBlock.Load())
	}

	// Running again for the same block should be a no-op.
	svc.runSamplingRound(ctx)
}

// --- KZGProofLength constant test ---

func TestKZGProofLength(t *testing.T) {
	if KZGProofLength != 48 {
		t.Fatalf("KZGProofLength should be 48, got %d", KZGProofLength)
	}
}

// --- StoreDataColumn with validation ---

func TestStoreDataColumn_InvalidProofLength(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	nodeID := randomBytes(32)

	cfg := Config{
		Enable:              true,
		CustodyCount:        CustodyRequirement,
		SkipKZGVerification: true,
	}
	svc := NewService(cfg, nodeID, db)

	col := &DataColumn{
		Index:       0,
		BlockHash:   randomHash(),
		BlockNumber: 1,
		Cells:       [][]byte{make([]byte, BytesPerCell)},
		KZGProofs:   [][]byte{randomBytes(32)}, // wrong length
		Commitments: [][]byte{randomBytes(KZGCommitmentLength)},
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
		Enable:              true,
		CustodyCount:        CustodyRequirement,
		SkipKZGVerification: true,
	}
	svc := NewService(cfg, nodeID, db)

	col := &DataColumn{
		Index:       0,
		BlockHash:   randomHash(),
		BlockNumber: 1,
		Cells:       nil,
		KZGProofs:   [][]byte{randomBytes(KZGProofLength)},
		Commitments: [][]byte{randomBytes(KZGCommitmentLength)},
	}

	err := svc.StoreDataColumn(ctx, col)
	if err != ErrEmptyColumnData {
		t.Fatalf("expected ErrEmptyColumnData, got %v", err)
	}
}

// =============================================================================
// Real KZG integration tests
// =============================================================================

// makeZeroBlob creates a blob filled with zeros (valid canonical field elements).
func makeZeroBlob() transaction.Blob {
	return transaction.Blob{}
}

func TestKZG_ProduceAndVerifyColumns(t *testing.T) {
	blob := makeZeroBlob()

	// Compute commitment for the blob.
	commitment, err := kzg.BlobToCommitment(&blob)
	if err != nil {
		t.Fatalf("BlobToCommitment: %v", err)
	}

	blockHash := randomHash()
	columns, err := ProduceColumns(
		[]transaction.Blob{blob},
		[]transaction.Commitment{commitment},
		blockHash,
		100,
	)
	if err != nil {
		t.Fatalf("ProduceColumns: %v", err)
	}

	// Verify all 128 columns have correct structure.
	for i := uint64(0); i < NumberOfColumns; i++ {
		col := columns[i]
		if col == nil {
			t.Fatalf("column %d is nil", i)
		}
		if col.Index != i {
			t.Errorf("column %d has index %d", i, col.Index)
		}
		if len(col.Cells) != 1 {
			t.Fatalf("column %d: expected 1 cell, got %d", i, len(col.Cells))
		}
		if len(col.Cells[0]) != BytesPerCell {
			t.Errorf("column %d: cell size %d, want %d", i, len(col.Cells[0]), BytesPerCell)
		}
		if err := col.Validate(); err != nil {
			t.Fatalf("column %d: Validate: %v", i, err)
		}
	}

	// KZG verify a few columns.
	verifier, err := NewVerifier()
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}

	for _, colIdx := range []uint64{0, 1, 63, 127} {
		if err := verifier.VerifyDataColumn(columns[colIdx]); err != nil {
			t.Errorf("VerifyDataColumn(%d): %v", colIdx, err)
		}
	}
}

func TestKZG_VerifyDataColumn_BadProof(t *testing.T) {
	blob := makeZeroBlob()
	commitment, err := kzg.BlobToCommitment(&blob)
	if err != nil {
		t.Fatalf("BlobToCommitment: %v", err)
	}

	columns, err := ProduceColumns(
		[]transaction.Blob{blob},
		[]transaction.Commitment{commitment},
		randomHash(),
		100,
	)
	if err != nil {
		t.Fatalf("ProduceColumns: %v", err)
	}

	verifier, err := NewVerifier()
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}

	// Corrupt a proof and verify it fails.
	col := columns[0]
	col.KZGProofs[0][0] ^= 0xff

	if err := verifier.VerifyDataColumn(col); err == nil {
		t.Error("expected verification to fail with corrupted proof")
	}
}

func TestKZG_VerifyDataColumn_BadCell(t *testing.T) {
	blob := makeZeroBlob()
	commitment, err := kzg.BlobToCommitment(&blob)
	if err != nil {
		t.Fatalf("BlobToCommitment: %v", err)
	}

	columns, err := ProduceColumns(
		[]transaction.Blob{blob},
		[]transaction.Commitment{commitment},
		randomHash(),
		100,
	)
	if err != nil {
		t.Fatalf("ProduceColumns: %v", err)
	}

	verifier, err := NewVerifier()
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}

	// Corrupt cell data and verify it fails.
	col := columns[5]
	col.Cells[0][100] ^= 0xff

	if err := verifier.VerifyDataColumn(col); err == nil {
		t.Error("expected verification to fail with corrupted cell data")
	}
}

func TestKZG_VerifyDataColumn_WrongCommitment(t *testing.T) {
	blob := makeZeroBlob()
	commitment, err := kzg.BlobToCommitment(&blob)
	if err != nil {
		t.Fatalf("BlobToCommitment: %v", err)
	}

	columns, err := ProduceColumns(
		[]transaction.Blob{blob},
		[]transaction.Commitment{commitment},
		randomHash(),
		100,
	)
	if err != nil {
		t.Fatalf("ProduceColumns: %v", err)
	}

	verifier, err := NewVerifier()
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}

	// Use a different blob's commitment.
	var blob2 transaction.Blob
	blob2[0] = 0x01 // different but still valid (< BLS modulus)
	commitment2, err := kzg.BlobToCommitment(&blob2)
	if err != nil {
		t.Fatalf("BlobToCommitment for blob2: %v", err)
	}

	col := columns[10]
	copy(col.Commitments[0], commitment2[:])

	if err := verifier.VerifyDataColumn(col); err == nil {
		t.Error("expected verification to fail with wrong commitment")
	}
}

func TestKZG_StoreAndVerifyWithRealKZG(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	nodeID := randomBytes(32)

	cfg := Config{
		Enable:       true,
		CustodyCount: CustodyRequirement,
		SampleCount:  SamplesPerSlot,
		// KZG verification enabled (default).
	}
	svc := NewService(cfg, nodeID, db)

	blob := makeZeroBlob()
	commitment, err := kzg.BlobToCommitment(&blob)
	if err != nil {
		t.Fatalf("BlobToCommitment: %v", err)
	}

	blockHash := randomHash()
	columns, err := ProduceColumns(
		[]transaction.Blob{blob},
		[]transaction.Commitment{commitment},
		blockHash,
		100,
	)
	if err != nil {
		t.Fatalf("ProduceColumns: %v", err)
	}

	// Store all sampled columns with real KZG verification.
	sampleCols := SampleColumns(blockHash, cfg.SampleCount)
	for _, colIdx := range sampleCols {
		if err := svc.StoreDataColumn(ctx, columns[colIdx]); err != nil {
			t.Fatalf("StoreDataColumn(%d): %v", colIdx, err)
		}
	}

	// Verify availability with real KZG.
	available, err := svc.VerifyAvailability(ctx, blockHash, 100)
	if err != nil {
		t.Fatalf("VerifyAvailability: %v", err)
	}
	if !available {
		t.Error("expected availability=true with real KZG-verified columns")
	}
}

func TestKZG_StoreRejectsInvalidProof(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	nodeID := randomBytes(32)

	cfg := Config{
		Enable:       true,
		CustodyCount: CustodyRequirement,
	}
	svc := NewService(cfg, nodeID, db)

	blob := makeZeroBlob()
	commitment, err := kzg.BlobToCommitment(&blob)
	if err != nil {
		t.Fatalf("BlobToCommitment: %v", err)
	}

	columns, err := ProduceColumns(
		[]transaction.Blob{blob},
		[]transaction.Commitment{commitment},
		randomHash(),
		100,
	)
	if err != nil {
		t.Fatalf("ProduceColumns: %v", err)
	}

	// Corrupt proof on column 0.
	col := columns[0]
	col.KZGProofs[0][0] ^= 0xff

	err = svc.StoreDataColumn(ctx, col)
	if err == nil {
		t.Error("expected StoreDataColumn to reject column with invalid KZG proof")
	}
}

func TestProduceColumns_NoBlobs(t *testing.T) {
	_, err := ProduceColumns(nil, nil, randomHash(), 1)
	if err != ErrNoBlobsProvided {
		t.Fatalf("expected ErrNoBlobsProvided, got %v", err)
	}
}

func TestProduceColumns_CommitmentMismatch(t *testing.T) {
	blob := makeZeroBlob()
	_, err := ProduceColumns([]transaction.Blob{blob}, nil, randomHash(), 1)
	if err != ErrCommitmentCountMismatch {
		t.Fatalf("expected ErrCommitmentCountMismatch, got %v", err)
	}
}

// --- VerifyDataColumnLight test ---

func TestVerifyDataColumnLight(t *testing.T) {
	validCell := make([]byte, BytesPerCell)
	validProof := randomBytes(KZGProofLength)
	validComm := randomBytes(KZGCommitmentLength)

	col := &DataColumn{
		Index:       0,
		BlockHash:   randomHash(),
		Cells:       [][]byte{validCell},
		KZGProofs:   [][]byte{validProof},
		Commitments: [][]byte{validComm},
	}
	if err := VerifyDataColumnLight(col); err != nil {
		t.Errorf("expected no error, got %v", err)
	}

	// Out of range index.
	col.Index = NumberOfColumns
	if err := VerifyDataColumnLight(col); err != ErrColumnIndexOutOfRange {
		t.Errorf("expected ErrColumnIndexOutOfRange, got %v", err)
	}
}

// --- Benchmarks ---

func BenchmarkProduceColumns(b *testing.B) {
	blob := makeZeroBlob()
	commitment, err := kzg.BlobToCommitment(&blob)
	if err != nil {
		b.Fatalf("BlobToCommitment: %v", err)
	}
	blockHash := randomHash()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := ProduceColumns(
			[]transaction.Blob{blob},
			[]transaction.Commitment{commitment},
			blockHash,
			uint64(i),
		)
		if err != nil {
			b.Fatalf("ProduceColumns: %v", err)
		}
	}
}

func BenchmarkVerifyDataColumn(b *testing.B) {
	blob := makeZeroBlob()
	commitment, err := kzg.BlobToCommitment(&blob)
	if err != nil {
		b.Fatalf("BlobToCommitment: %v", err)
	}

	columns, err := ProduceColumns(
		[]transaction.Blob{blob},
		[]transaction.Commitment{commitment},
		randomHash(),
		100,
	)
	if err != nil {
		b.Fatalf("ProduceColumns: %v", err)
	}

	verifier, err := NewVerifier()
	if err != nil {
		b.Fatalf("NewVerifier: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		col := columns[i%NumberOfColumns]
		if err := verifier.VerifyDataColumn(col); err != nil {
			b.Fatalf("VerifyDataColumn: %v", err)
		}
	}
}
