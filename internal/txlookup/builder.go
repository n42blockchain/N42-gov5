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

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/cscompact"
	"github.com/n42blockchain/N42/lib/recsplit"
	"github.com/n42blockchain/N42/lib/recsplit/eliasfano32"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

// SegmentBuilder builds a single RecSplit segment from freezer body data.
// BodyTxHashes decodes a stored body into its transaction hashes, in block
// order. Supplied by the caller rather than imported, because the only decoder
// this builder ever needed lives in internal/ethel, which imports the root
// internal package -- and the root package now needs txlookup for its own live
// index, which would close an import cycle. Injecting the one call keeps this
// package free of that edge.
type BodyTxHashes func(bodyData []byte) ([]types.Hash, error)

type SegmentBuilder struct {
	inputFreezer *freezer.Freezer
	outputDir    string
	decodeBody   BodyTxHashes
	// RecSplit tuning. Legacy default (NewSegmentBuilder): enums=false,
	// lessFalsePositives=true — matches every txindex built before this field
	// existed.
	enums              bool
	lessFalsePositives bool
}

func NewSegmentBuilder(input *freezer.Freezer, outputDir string, decodeBody BodyTxHashes) *SegmentBuilder {
	return &SegmentBuilder{inputFreezer: input, outputDir: outputDir, decodeBody: decodeBody, lessFalsePositives: true}
}

// SetRecSplitTuning overrides the RecSplit space/correctness config.
//
//   - enums=true replaces the fixed-width per-key offset (bytesPerRec, ~28 bit
//     at 250M keys) with an Elias-Fano enumeration of the dense ordinals
//     (~2.5 bit/key). This is the main lever: ~33.7 → ~12 bit/key with LFP on.
//   - lessFalsePositives=true keeps the 8-bit existence fingerprint. With it
//     off, every out-of-set hash gets a phantom ordinal (the MPHF always maps
//     to [0,N)), so a newer segment falsely answers for a tx that lives in an
//     older one and the newest-first probe returns the wrong block. With LFP
//     off the index drops to ~4.4 bit/key.
//
//     It is NOT sufficient on its own. The fingerprint is 8 bits, so about one
//     out-of-set hash in 256 still resolves in a segment that does not hold it
//     — measured at 28 wrong blocks in 7,680 transactions across three
//     segments. Any multi-segment store therefore needs Service.SetVerifier
//     (read the candidate block, confirm the tx hash, else keep probing),
//     whatever this flag is set to. LFP only changes how often the verifier
//     has to reject, not whether it is needed.
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
	// A PARTIAL final segment — one a previous run ended mid-segment because
	// its source stopped there — must be REWRITTEN, not counted. The resume
	// arithmetic below is in whole segments, so counting a partial as full
	// resumes at the next boundary and leaves a permanent hole. This is the
	// same rewind headerc/bodyc do; it matters here from the moment txindex
	// becomes a weekly job, because every week ends mid-segment.
	partials, err := partialTailSegments(b.outputDir)
	if err != nil {
		return err
	}

	store, err := cscompact.NewSegmentStoreWriter(b.outputDir, "txindex")
	if err != nil {
		return err
	}
	defer store.Close()

	for i := 0; i < partials; i++ {
		log.Warn("txindex: final segment PARTIAL — rewinding to rewrite it",
			"segment", store.SegmentCount()-1)
		if err := store.TruncateLastSegment(); err != nil {
			return err
		}
	}

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
	//
	// A window store keeps the base it was created with, so once the requested
	// window slides past a segment boundary the store simply covers MORE than
	// was asked for — correct, but it never shrinks. Say so rather than let a
	// "one year" index quietly grow into a full-history one.
	storedBase := readSegmentBase(b.outputDir)
	if existingSegs > 0 && alignedStart > storedBase {
		log.Warn("txindex: store base is older than the requested window — it will keep growing; rebuild from scratch to re-cut it",
			"stored_base", storedBase, "requested_base", alignedStart,
			"extra_blocks", alignedStart-storedBase)
	}
	resumeBlock := storedBase + existingSegs*SegmentSize
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
		hashes, err := b.decodeBody(bodyData)
		if err != nil {
			return nil, fmt.Errorf("decode body %d: %w", blockNum, err)
		}
		txPerBlock[blockNum-startBlock] = uint32(len(hashes))
		totalTx += len(hashes)

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
		hashes, err := b.decodeBody(bodyData)
		if err != nil {
			return nil, err
		}
		for _, txHash := range hashes {
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

// partialTailSegments counts the trailing segments whose V2 dat header reports
// fewer than SegmentSize blocks. Anything it cannot read as V2 stops the scan:
// an unknown-format tail is left for a human, never silently discarded.
func partialTailSegments(dir string) (int, error) {
	st, err := cscompact.OpenSegmentStore(dir, "txindex")
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	defer st.Close()

	count := 0
	for n := st.SegmentCount(); n > 0; n-- {
		data, err := st.ReadSegmentData(n - 1)
		if err != nil || len(data) < 8 || string(data[:4]) != string(datMagicV2[:]) {
			break
		}
		if uint64(binary.LittleEndian.Uint32(data[4:8])) >= SegmentSize {
			break
		}
		count++
	}
	return count, nil
}
