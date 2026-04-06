// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// builder.go constructs RecSplit segments for tx hash → block number lookup.
// V2 format: Elias-Fano encoded block boundaries replace raw uint32 arrays.
// Compression: ~500:1 for tx-dense segments (496 MB → 1 MB).

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
	"github.com/n42blockchain/N42/lib/recsplit/eliasfano32"
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
	blockCount := endBlock - startBlock

	// Pass 1: count transactions per block.
	txPerBlock := make([]uint32, blockCount)
	totalTx := 0
	for blockNum := startBlock; blockNum < endBlock; blockNum++ {
		bodyData, err := b.inputFreezer.Ancient(freezer.TableBodies, blockNum)
		if err != nil {
			return fmt.Errorf("read body %d: %w", blockNum, err)
		}
		body, err := ethel.DecodeGethBody(bodyData)
		if err != nil {
			return fmt.Errorf("decode body %d: %w", blockNum, err)
		}
		txPerBlock[blockNum-startBlock] = uint32(len(body.Transactions))
		totalTx += len(body.Transactions)

		if (blockNum-startBlock)%100000 == 0 && blockNum > startBlock {
			elapsed := time.Since(t0)
			pct := float64(blockNum-startBlock) / float64(blockCount) * 100
			log.Info("Segment scan progress",
				"block", blockNum,
				"pct", fmt.Sprintf("%.0f%%", pct),
				"txs", totalTx,
				"elapsed", elapsed.Truncate(time.Second))
		}
	}

	newRecSplit := func(keyCount int) (*recsplit.RecSplit, error) {
		return recsplit.NewRecSplit(recsplit.RecSplitArgs{
			KeyCount:           keyCount,
			BucketSize:         2000,
			LeafSize:           8,
			Enums:              false,
			LessFalsePositives: true,
			IndexFile:          idxPath,
			BaseDataID:         startBlock,
			TmpDir:             os.TempDir(),
		}, log2.New())
	}

	if totalTx == 0 {
		log.Info("Empty segment (no transactions)", "blocks", fmt.Sprintf("%d-%d", startBlock, endBlock-1))
		if err := writeEmptyDatV2(datPath, blockCount); err != nil {
			return err
		}
		rs, err := newRecSplit(0)
		if err != nil {
			return err
		}
		return rs.Build(ctx)
	}

	log.Info("Building segment",
		"blocks", fmt.Sprintf("%d-%d", startBlock, endBlock-1),
		"txCount", totalTx)

	rs, err := newRecSplit(totalTx)
	if err != nil {
		return err
	}

	// Pass 2: add tx hashes to RecSplit (no .dat writes needed per-tx).
	ordinal := uint64(0)
	for blockNum := startBlock; blockNum < endBlock; blockNum++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		bodyData, err := b.inputFreezer.Ancient(freezer.TableBodies, blockNum)
		if err != nil {
			return err
		}
		body, err := ethel.DecodeGethBody(bodyData)
		if err != nil {
			return err
		}

		for _, tx := range body.Transactions {
			txHash := tx.Hash()
			if err := rs.AddKey(txHash[:], ordinal); err != nil {
				return fmt.Errorf("addKey block %d: %w", blockNum, err)
			}
			ordinal++
		}
	}

	// Build RecSplit index.
	log.Info("Building RecSplit index", "txCount", totalTx)
	if err := rs.Build(ctx); err != nil {
		return fmt.Errorf("recsplit build: %w", err)
	}

	// Build Elias-Fano block boundaries and write V2 .dat.
	if err := writeDatV2(datPath, blockCount, uint64(totalTx), txPerBlock); err != nil {
		os.Remove(idxPath)
		return fmt.Errorf("write dat v2: %w", err)
	}

	elapsed := time.Since(t0)
	idxFi, _ := os.Stat(idxPath)
	datFi, _ := os.Stat(datPath)
	log.Info("Segment built",
		"blocks", fmt.Sprintf("%d-%d", startBlock, endBlock-1),
		"txCount", totalTx,
		"idx", fmt.Sprintf("%.1f MB", float64(idxFi.Size())/1e6),
		"dat", fmt.Sprintf("%.1f KB", float64(datFi.Size())/1e3),
		"compression", fmt.Sprintf("%.0fx", float64(totalTx)*4/float64(datFi.Size())),
		"elapsed", elapsed.Truncate(time.Second))
	return nil
}

// writeDatV2 writes Elias-Fano encoded block boundaries.
// Format: [4]magic + [4]blockCount + [8]txCount + [variable]EliasFano
//
// The EF sequence has blockCount+1 entries storing cumulative tx counts:
//
//	ef[0] = 0
//	ef[i] = sum of txPerBlock[0..i-1]
//	ef[blockCount] = totalTx
func writeDatV2(path string, blockCount, totalTx uint64, txPerBlock []uint32) error {
	// Build Elias-Fano: blockCount+1 entries, max value = totalTx.
	ef := eliasfano32.NewEliasFano(blockCount+1, totalTx)
	cumTx := uint64(0)
	ef.AddOffset(0)
	for _, cnt := range txPerBlock {
		cumTx += uint64(cnt)
		ef.AddOffset(cumTx)
	}
	ef.Build()

	// Serialize.
	var header [16]byte
	copy(header[:4], datMagicV2[:])
	binary.LittleEndian.PutUint32(header[4:8], uint32(blockCount))
	binary.LittleEndian.PutUint64(header[8:16], totalTx)

	var buf []byte
	buf = append(buf, header[:]...)
	buf = ef.AppendBytes(buf)

	return os.WriteFile(path, buf, 0644)
}

func writeEmptyDatV2(path string, blockCount uint64) error {
	var header [16]byte
	copy(header[:4], datMagicV2[:])
	binary.LittleEndian.PutUint32(header[4:8], uint32(blockCount))
	binary.LittleEndian.PutUint64(header[8:16], 0)
	return os.WriteFile(path, header[:], 0644)
}
