// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// builder.go constructs RecSplit segments for tx hash → block number lookup.
// Reads block bodies from the Geth ancient freezer, hashes every transaction,
// and builds a minimal perfect hash index + compact blockNumber array.

package txlookup

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/lib/recsplit"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

// SegmentBuilder builds a single RecSplit segment from freezer body data.
type SegmentBuilder struct {
	inputFreezer *freezer.Freezer
	outputDir    string
}

func NewSegmentBuilder(input *freezer.Freezer, outputDir string) *SegmentBuilder {
	return &SegmentBuilder{inputFreezer: input, outputDir: outputDir}
}

// SegmentFileName returns the file name for a segment (without extension).
func SegmentFileName(startBlock, endBlock uint64) string {
	return fmt.Sprintf("txlookup-%06d-%06d", startBlock/1000, endBlock/1000)
}

// BuildRange builds all segments for the given block range.
func (b *SegmentBuilder) BuildRange(ctx context.Context, startBlock, endBlock uint64) error {
	if err := os.MkdirAll(b.outputDir, 0755); err != nil {
		return err
	}

	for segStart := (startBlock / SegmentSize) * SegmentSize; segStart < endBlock; segStart += SegmentSize {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		segEnd := segStart + SegmentSize
		if segEnd > endBlock {
			segEnd = endBlock
		}
		if segEnd > b.inputFreezer.Frozen() {
			segEnd = b.inputFreezer.Frozen()
		}

		baseName := SegmentFileName(segStart, segEnd)
		idxPath := filepath.Join(b.outputDir, baseName+".idx")
		datPath := filepath.Join(b.outputDir, baseName+".dat")

		// Skip if already built.
		if _, err := os.Stat(idxPath); err == nil {
			log.Info("Segment exists, skipping", "segment", baseName)
			continue
		}

		if err := b.buildOne(ctx, segStart, segEnd, idxPath, datPath); err != nil {
			return fmt.Errorf("build segment %s: %w", baseName, err)
		}
	}
	return nil
}

func (b *SegmentBuilder) buildOne(ctx context.Context, startBlock, endBlock uint64, idxPath, datPath string) error {
	t0 := time.Now()

	// Pass 1: count transactions.
	txCount := 0
	for blockNum := startBlock; blockNum < endBlock; blockNum++ {
		bodyData, err := b.inputFreezer.Ancient(freezer.TableBodies, blockNum)
		if err != nil {
			return fmt.Errorf("read body %d: %w", blockNum, err)
		}
		body, err := ethel.DecodeGethBody(bodyData)
		if err != nil {
			return fmt.Errorf("decode body %d: %w", blockNum, err)
		}
		txCount += len(body.Transactions)
	}

	if txCount == 0 {
		// Create empty segment files.
		log.Info("Empty segment (no transactions)", "blocks", fmt.Sprintf("%d-%d", startBlock, endBlock-1))
		// Write empty .dat
		if err := os.WriteFile(datPath, nil, 0644); err != nil {
			return err
		}
		// Build empty RecSplit.
		logger := log2.New()
		rs, err := recsplit.NewRecSplit(recsplit.RecSplitArgs{
			KeyCount:           0,
			BucketSize:         2000,
			LeafSize:           8,
			Enums:              false,
			LessFalsePositives: true,
			IndexFile:          idxPath,
			BaseDataID:         startBlock,
			TmpDir:             os.TempDir(),
		}, logger)
		if err != nil {
			return err
		}
		return rs.Build(ctx)
	}

	log.Info("Building segment",
		"blocks", fmt.Sprintf("%d-%d", startBlock, endBlock-1),
		"txCount", txCount)

	// Create .dat file (4 bytes per tx: relative blockNum).
	datFile, err := os.Create(datPath)
	if err != nil {
		return err
	}

	// Create RecSplit builder.
	logger := log2.New()
	rs, err := recsplit.NewRecSplit(recsplit.RecSplitArgs{
		KeyCount:           txCount,
		BucketSize:         2000,
		LeafSize:           8,
		Enums:              false,
		LessFalsePositives: true,
		IndexFile:          idxPath,
		BaseDataID:         startBlock,
		TmpDir:             os.TempDir(),
	}, logger)
	if err != nil {
		datFile.Close()
		return err
	}

	// Pass 2: add keys + write .dat.
	ordinal := uint64(0)
	var buf [4]byte
	for blockNum := startBlock; blockNum < endBlock; blockNum++ {
		if ctx.Err() != nil {
			datFile.Close()
			os.Remove(datPath)
			return ctx.Err()
		}

		bodyData, err := b.inputFreezer.Ancient(freezer.TableBodies, blockNum)
		if err != nil {
			datFile.Close()
			return err
		}
		body, err := ethel.DecodeGethBody(bodyData)
		if err != nil {
			datFile.Close()
			return err
		}

		relBlock := uint32(blockNum - startBlock)
		for _, tx := range body.Transactions {
			txHash := tx.Hash()
			if err := rs.AddKey(txHash[:], ordinal); err != nil {
				datFile.Close()
				return fmt.Errorf("addKey block %d: %w", blockNum, err)
			}
			binary.LittleEndian.PutUint32(buf[:], relBlock)
			if _, err := datFile.Write(buf[:]); err != nil {
				datFile.Close()
				return err
			}
			ordinal++
		}

		if (blockNum-startBlock)%100000 == 0 && blockNum > startBlock {
			elapsed := time.Since(t0)
			pct := float64(blockNum-startBlock) / float64(endBlock-startBlock) * 100
			log.Info("Segment scan progress",
				"block", blockNum,
				"pct", fmt.Sprintf("%.0f%%", pct),
				"txs", ordinal,
				"elapsed", elapsed.Truncate(time.Second))
		}
	}
	datFile.Close()

	// Build RecSplit index.
	log.Info("Building RecSplit index", "txCount", txCount)
	if err := rs.Build(ctx); err != nil {
		os.Remove(datPath)
		return fmt.Errorf("recsplit build: %w", err)
	}

	elapsed := time.Since(t0)
	idxFi, _ := os.Stat(idxPath)
	datFi, _ := os.Stat(datPath)
	log.Info("Segment built",
		"blocks", fmt.Sprintf("%d-%d", startBlock, endBlock-1),
		"txCount", txCount,
		"idx", fmt.Sprintf("%.1f MB", float64(idxFi.Size())/1e6),
		"dat", fmt.Sprintf("%.1f MB", float64(datFi.Size())/1e6),
		"elapsed", elapsed.Truncate(time.Second))
	return nil
}
