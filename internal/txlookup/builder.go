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
	"strconv"
	"time"

	"github.com/n42blockchain/N42/internal/cscompact"
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
	// RecSplit tuning. Legacy default (NewSegmentBuilder): enums=false,
	// lessFalsePositives=true — matches every txindex built before this field
	// existed.
	enums              bool
	lessFalsePositives bool
}

func NewSegmentBuilder(input *freezer.Freezer, outputDir string) *SegmentBuilder {
	return &SegmentBuilder{inputFreezer: input, outputDir: outputDir, lessFalsePositives: true}
}

// SetRecSplitTuning overrides the RecSplit space/correctness config.
//
//   - enums=true replaces the fixed-width per-key offset (bytesPerRec, ~28 bit
//     at 250M keys) with an Elias-Fano enumeration of the dense ordinals
//     (~2.5 bit/key). This is the main lever: ~33.7 → ~12 bit/key with LFP on.
//   - lessFalsePositives=true keeps the 8-bit existence fingerprint. It is
//     REQUIRED for correct multi-segment lookup: with it off, every out-of-set
//     hash gets a phantom ordinal (the MPHF always maps to [0,N)), so a newer
//     segment will falsely answer for a tx that lives in an older one and the
//     newest-first probe returns the wrong block. Only disable it together with
//     a verify-and-continue lookup (read the candidate block, confirm the tx
//     hash, else keep probing). With LFP off + that lookup change the index
//     drops to ~4.4 bit/key.
func (b *SegmentBuilder) SetRecSplitTuning(enums, lessFalsePositives bool) {
	b.enums = enums
	b.lessFalsePositives = lessFalsePositives
}

// SegmentFileName returns the file name for a segment (without extension).
func SegmentFileName(startBlock, endBlock uint64) string {
	return fmt.Sprintf("txlookup-%06d-%06d", startBlock/1000, endBlock/1000)
}

// etlTmpDir returns N42_ETL_TMPDIR when set (RecSplit spill for a full
// segment is GBs — keep it off the small system drive), else the OS temp.
func etlTmpDir() string {
	if d := os.Getenv("N42_ETL_TMPDIR"); d != "" {
		if err := os.MkdirAll(d, 0755); err == nil {
			return d
		}
	}
	return os.TempDir()
}

// BuildRange builds all segments for the given block range using freezer-style storage.
func (b *SegmentBuilder) BuildRange(ctx context.Context, startBlock, endBlock uint64) error {
	store, err := cscompact.NewSegmentStoreWriter(b.outputDir, "txindex")
	if err != nil {
		return err
	}
	defer store.Close()

	// A store that does not begin at block 0 must record its base block so
	// the reader (readSegmentBase) maps segment N to base+N*SegmentSize —
	// without the marker a range-built index silently resolves every hash to
	// a block number offset by the base (found by query verification).
	alignedStart := (startBlock / SegmentSize) * SegmentSize
	existingSegs := store.SegmentCount()
	if existingSegs == 0 && alignedStart > 0 {
		basePath := filepath.Join(b.outputDir, "txindex.base")
		if err := os.WriteFile(basePath, []byte(strconv.FormatUint(alignedStart, 10)), 0644); err != nil {
			return fmt.Errorf("write txindex.base: %w", err)
		}
		log.Info("Recorded txindex base", "base", alignedStart)
	}

	// Resume from existing segments, honoring a previously recorded base
	// (0 for legacy full builds without a base file).
	resumeBlock := readSegmentBase(b.outputDir) + existingSegs*SegmentSize
	if existingSegs > 0 && resumeBlock > startBlock {
		startBlock = resumeBlock
		log.Info("Resuming txlookup build", "from", startBlock, "segments", existingSegs)
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

		tmpIdx := filepath.Join(b.outputDir, fmt.Sprintf("tmp_txindex_%d.ri", segStart))
		datBytes, err := b.buildOne(ctx, segStart, segEnd, tmpIdx)
		if err != nil {
			os.Remove(tmpIdx)
			return fmt.Errorf("build segment %d-%d: %w", segStart, segEnd, err)
		}

		if _, err := store.WriteSegment(datBytes, tmpIdx); err != nil {
			return err
		}
	}
	return nil
}

// buildOne builds a single segment, returns dat bytes and writes RecSplit to idxPath.
func (b *SegmentBuilder) buildOne(ctx context.Context, startBlock, endBlock uint64, idxPath string) ([]byte, error) {
	t0 := time.Now()
	blockCount := endBlock - startBlock

	log.Info("Building txindex segment",
		"blocks", fmt.Sprintf("%d-%d", startBlock, endBlock-1),
		"blockCount", blockCount)

	// Pass 1: count transactions per block.
	txPerBlock := make([]uint32, blockCount)
	totalTx := 0
	for blockNum := startBlock; blockNum < endBlock; blockNum++ {
		bodyData, err := b.inputFreezer.Ancient(freezer.TableBodies, blockNum)
		if err != nil {
			return nil, fmt.Errorf("read body %d: %w", blockNum, err)
		}
		body, err := ethel.DecodeGethBody(bodyData)
		if err != nil {
			return nil, fmt.Errorf("decode body %d: %w", blockNum, err)
		}
		txPerBlock[blockNum-startBlock] = uint32(len(body.Transactions))
		totalTx += len(body.Transactions)

		if (blockNum-startBlock)%50000 == 0 && blockNum > startBlock {
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
			Enums:              b.enums,
			LessFalsePositives: b.lessFalsePositives,
			IndexFile:          idxPath,
			BaseDataID:         startBlock,
			TmpDir:             etlTmpDir(),
		}, log2.New())
	}

	if totalTx == 0 {
		log.Info("Empty segment (no transactions)", "blocks", fmt.Sprintf("%d-%d", startBlock, endBlock-1))
		rs, err := newRecSplit(0)
		if err != nil {
			return nil, err
		}
		if err := rs.Build(ctx); err != nil {
			return nil, err
		}
		return buildEmptyDatV2(blockCount), nil
	}

	log.Info("Building segment",
		"blocks", fmt.Sprintf("%d-%d", startBlock, endBlock-1),
		"txCount", totalTx)

	rs, err := newRecSplit(totalTx)
	if err != nil {
		return nil, err
	}

	// Pass 2: add tx hashes to RecSplit.
	ordinal := uint64(0)
	for blockNum := startBlock; blockNum < endBlock; blockNum++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		bodyData, err := b.inputFreezer.Ancient(freezer.TableBodies, blockNum)
		if err != nil {
			return nil, err
		}
		body, err := ethel.DecodeGethBody(bodyData)
		if err != nil {
			return nil, err
		}
		for _, tx := range body.Transactions {
			txHash := tx.Hash()
			if err := rs.AddKey(txHash[:], ordinal); err != nil {
				return nil, fmt.Errorf("addKey block %d: %w", blockNum, err)
			}
			ordinal++
		}
	}

	log.Info("Building RecSplit index", "txCount", totalTx)
	if err := rs.Build(ctx); err != nil {
		return nil, fmt.Errorf("recsplit build: %w", err)
	}

	// Build Elias-Fano block boundaries as dat bytes.
	datBytes := buildDatV2Bytes(blockCount, uint64(totalTx), txPerBlock)

	elapsed := time.Since(t0)
	log.Info("Segment built",
		"blocks", fmt.Sprintf("%d-%d", startBlock, endBlock-1),
		"txCount", totalTx,
		"dat", fmt.Sprintf("%.1f KB", float64(len(datBytes))/1e3),
		"elapsed", elapsed.Truncate(time.Second))
	return datBytes, nil
}

// buildDatV2Bytes returns Elias-Fano encoded dat as bytes (no file write).
func buildDatV2Bytes(blockCount, totalTx uint64, txPerBlock []uint32) []byte {
	ef := eliasfano32.NewEliasFano(blockCount+1, totalTx)
	cumTx := uint64(0)
	ef.AddOffset(0)
	for _, cnt := range txPerBlock {
		cumTx += uint64(cnt)
		ef.AddOffset(cumTx)
	}
	ef.Build()

	var header [16]byte
	copy(header[:4], datMagicV2[:])
	binary.LittleEndian.PutUint32(header[4:8], uint32(blockCount))
	binary.LittleEndian.PutUint64(header[8:16], totalTx)

	var buf []byte
	buf = append(buf, header[:]...)
	buf = ef.AppendBytes(buf)
	return buf
}

// buildEmptyDatV2 returns empty V2 dat bytes.
func buildEmptyDatV2(blockCount uint64) []byte {
	var header [16]byte
	copy(header[:4], datMagicV2[:])
	binary.LittleEndian.PutUint32(header[4:8], uint32(blockCount))
	return header[:]
}

// Legacy file-writing wrappers (used by old tests).
func writeDatV2(path string, blockCount, totalTx uint64, txPerBlock []uint32) error {
	return os.WriteFile(path, buildDatV2Bytes(blockCount, totalTx, txPerBlock), 0644)
}

func writeEmptyDatV2(path string, blockCount uint64) error {
	return os.WriteFile(path, buildEmptyDatV2(blockCount), 0644)
}
