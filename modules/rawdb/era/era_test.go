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

package era

import (
	"bytes"
	"errors"
	mrand "math/rand"
	"os"
	"path/filepath"
	"testing"
)

// makeTestData generates deterministic test block and receipt data for a block number.
func makeTestData(blockNumber uint64) (blockData, receiptsData []byte) {
	r := mrand.New(mrand.NewSource(int64(blockNumber)))
	blockData = make([]byte, 128+r.Intn(256))
	receiptsData = make([]byte, 64+r.Intn(128))
	// Fill with pseudo-random data seeded by block number for reproducibility.
	for i := range blockData {
		blockData[i] = byte(r.Intn(256))
	}
	for i := range receiptsData {
		receiptsData[i] = byte(r.Intn(256))
	}
	return blockData, receiptsData
}

func TestEraRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.era")

	const numRecords = 100
	const networkID = 42

	// Write archive.
	w, err := NewWriter(path, networkID)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	for i := uint64(0); i < numRecords; i++ {
		bd, rd := makeTestData(i)
		if err := w.Append(i, bd, rd); err != nil {
			t.Fatalf("Append block %d: %v", i, err)
		}
	}
	if err := w.Finalize(); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	// Read archive and verify.
	r, err := OpenReader(path)
	if err != nil {
		t.Fatalf("OpenReader: %v", err)
	}
	defer r.Close()

	if r.Header().NetworkID != networkID {
		t.Errorf("NetworkID: got %d, want %d", r.Header().NetworkID, networkID)
	}

	first, last := r.BlockRange()
	if first != 0 || last != numRecords-1 {
		t.Errorf("BlockRange: got [%d, %d], want [0, %d]", first, last, numRecords-1)
	}
	if r.Count() != numRecords {
		t.Errorf("Count: got %d, want %d", r.Count(), numRecords)
	}

	for i := uint64(0); i < numRecords; i++ {
		gotBlock, gotReceipts, err := r.ReadRecord(i)
		if err != nil {
			t.Fatalf("ReadRecord(%d): %v", i, err)
		}
		wantBlock, wantReceipts := makeTestData(i)
		if !bytes.Equal(gotBlock, wantBlock) {
			t.Errorf("block %d data mismatch", i)
		}
		if !bytes.Equal(gotReceipts, wantReceipts) {
			t.Errorf("block %d receipts mismatch", i)
		}
	}
}

func TestEraRandomAccess(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "random.era")

	const numRecords = 1000
	const networkID = 1

	// Write archive.
	w, err := NewWriter(path, networkID)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	for i := uint64(0); i < numRecords; i++ {
		bd, rd := makeTestData(i)
		if err := w.Append(i, bd, rd); err != nil {
			t.Fatalf("Append block %d: %v", i, err)
		}
	}
	if err := w.Finalize(); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	// Open and read random blocks.
	r, err := OpenReader(path)
	if err != nil {
		t.Fatalf("OpenReader: %v", err)
	}
	defer r.Close()

	// Generate random block numbers to access.
	rng := mrand.New(mrand.NewSource(12345))
	for trial := 0; trial < 200; trial++ {
		bn := uint64(rng.Intn(numRecords))
		gotBlock, gotReceipts, err := r.ReadRecord(bn)
		if err != nil {
			t.Fatalf("ReadRecord(%d): %v", bn, err)
		}
		wantBlock, wantReceipts := makeTestData(bn)
		if !bytes.Equal(gotBlock, wantBlock) {
			t.Errorf("trial %d: block %d data mismatch", trial, bn)
		}
		if !bytes.Equal(gotReceipts, wantReceipts) {
			t.Errorf("trial %d: block %d receipts mismatch", trial, bn)
		}
	}

	// Accessing a non-existent block should fail.
	_, _, err = r.ReadRecord(numRecords + 1)
	if err == nil {
		t.Error("expected error for non-existent block, got nil")
	}
	if !errors.Is(err, ErrBlockNotFound) {
		t.Errorf("expected ErrBlockNotFound, got: %v", err)
	}
}

func TestEraEmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.era")

	// Write an archive with no records.
	w, err := NewWriter(path, 1)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	if err := w.Finalize(); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	// Opening should return ErrEmptyArchive.
	_, err = OpenReader(path)
	if err == nil {
		t.Fatal("expected error for empty archive, got nil")
	}
	if !errors.Is(err, ErrEmptyArchive) {
		t.Errorf("expected ErrEmptyArchive, got: %v", err)
	}
}

func TestEraInvalidMagic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "corrupt.era")

	// Create a file with invalid magic bytes.
	data := make([]byte, HeaderSize+FooterSize)
	copy(data[0:4], "XXXX") // wrong magic
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := OpenReader(path)
	if err == nil {
		t.Fatal("expected error for invalid magic, got nil")
	}
	if !errors.Is(err, ErrInvalidMagic) {
		t.Errorf("expected ErrInvalidMagic, got: %v", err)
	}

	// Also test DecodeHeader directly.
	_, err = DecodeHeader([]byte("XXXX000000000000"))
	if !errors.Is(err, ErrInvalidMagic) {
		t.Errorf("DecodeHeader: expected ErrInvalidMagic, got: %v", err)
	}

	// Test too-short header.
	_, err = DecodeHeader([]byte("era"))
	if !errors.Is(err, ErrHeaderTooShort) {
		t.Errorf("DecodeHeader: expected ErrHeaderTooShort, got: %v", err)
	}
}

