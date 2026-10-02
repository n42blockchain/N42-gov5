// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package ethel

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

func TestReceiptStageRunCopiesAllBlocks(t *testing.T) {
	inDir := t.TempDir()
	inFz := buildSyntheticGethFreezer(t, inDir, 10)
	defer inFz.Close()

	outFz := ethTMkFreezer(t)
	stage := NewReceiptStage(inFz, outFz, 2)
	if err := stage.Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	tbl, err := outFz.EnsureTableCompressed("receipts", "c")
	if err != nil {
		t.Fatal(err)
	}
	if got := tbl.Items(); got != 10 {
		t.Fatalf("Items() = %d, want 10", got)
	}
}

func TestReceiptStageRunIsResumable(t *testing.T) {
	inDir := t.TempDir()
	inFz := buildSyntheticGethFreezer(t, inDir, 5)
	defer inFz.Close()

	outFz := ethTMkFreezer(t)
	stage := NewReceiptStage(inFz, outFz, 1)
	if err := stage.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Already up to date on re-run.
	if err := stage.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	tbl, err := outFz.EnsureTableCompressed("receipts", "c")
	if err != nil {
		t.Fatal(err)
	}
	if got := tbl.Items(); got != 5 {
		t.Fatalf("Items() = %d, want 5 after no-op resume", got)
	}
}

func TestReceiptStageDefaultWorkerCount(t *testing.T) {
	inDir := t.TempDir()
	inFz := buildSyntheticGethFreezer(t, inDir, 1)
	defer inFz.Close()
	outFz := ethTMkFreezer(t)
	stage := NewReceiptStage(inFz, outFz, 0)
	if stage.workers <= 0 {
		t.Fatalf("workers = %d, want > 0", stage.workers)
	}
	if err := stage.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestReceiptStageLargeRunExercisesBatchFlush(t *testing.T) {
	const n = freezer.BatchSize*2 + 3
	inDir := t.TempDir()
	inFz := buildSyntheticGethFreezer(t, inDir, n)
	defer inFz.Close()

	outFz := ethTMkFreezer(t)
	stage := NewReceiptStage(inFz, outFz, 4)
	if err := stage.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	tbl, err := outFz.EnsureTableCompressed("receipts", "c")
	if err != nil {
		t.Fatal(err)
	}
	if got := tbl.Items(); got != n {
		t.Fatalf("Items() = %d, want %d", got, n)
	}
}
