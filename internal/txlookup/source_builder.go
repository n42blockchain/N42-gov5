// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// source_builder.go builds txindex segments from any block source, and sizes
// them by transaction count rather than block count.
//
// The existing builder reads eth-el bodies out of a freezer and cuts a segment
// every SegmentSize (1,000,000) blocks. That sizing follows from eth-el's
// density of roughly 150 transactions a block. The n42 chain at a raised gas
// ceiling puts 22,857 transactions in a block, where a million-block segment
// would hold 22.8 billion keys -- not buildable, and far past what a live index
// could hold while it accumulates.
//
// Segments already describe their own range: the RecSplit index carries the
// start block as its BaseDataID and the v2 dat header carries the block count.
// So a variable-size segment is readable by the existing OpenSegment; what did
// not exist was a way to build one, and a way for the reader to enumerate
// segments that are not all the same width. Both are here.

package txlookup

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/cscompact"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/recsplit"
	"github.com/n42blockchain/N42/log"
)

// BlockTxSource returns the transaction hashes of one block, in the order the
// block lists them. Transaction order is what the Elias-Fano block boundaries
// are built from, so it must be the block's own order, not a sorted one.
type BlockTxSource func(blockNum uint64) ([]types.Hash, error)

// rangesFileName holds each segment's [startBlock, blockCount] so the reader
// can enumerate segments of differing widths. Its absence means every segment
// is SegmentSize wide, which is what every index built before this was.
const rangesFileName = "txindex.ranges"

// SegmentRange describes one built segment.
type SegmentRange struct {
	StartBlock uint64
	BlockCount uint64
}

// BuildSegmentFromSource builds one segment covering [startBlock, endBlock)
// from src and appends it to outputDir's segment store.
func BuildSegmentFromSource(ctx context.Context, outputDir string, startBlock, endBlock uint64, src BlockTxSource) error {
	if endBlock <= startBlock {
		return fmt.Errorf("empty range %d-%d", startBlock, endBlock)
	}
	store, err := cscompact.NewSegmentStoreWriter(outputDir, "txindex")
	if err != nil {
		return err
	}
	defer store.Close()

	tmpIdx := filepath.Join(outputDir, fmt.Sprintf("tmp_txindex_%d.ri", startBlock))
	datBytes, err := buildFromSource(ctx, startBlock, endBlock, src, tmpIdx)
	if err != nil {
		os.Remove(tmpIdx)
		return fmt.Errorf("build segment %d-%d: %w", startBlock, endBlock, err)
	}
	if _, err := store.WriteSegment(datBytes, tmpIdx); err != nil {
		return err
	}
	return appendSegmentRange(outputDir, SegmentRange{StartBlock: startBlock, BlockCount: endBlock - startBlock})
}

// buildFromSource is buildOne over a BlockTxSource, in one pass.
//
// The source is read once and the hashes are held, rather than read twice as
// the freezer builder does: a live caller's source is memory, so a second pass
// buys nothing, and re-reading a source that has since advanced would build an
// index that disagrees with itself between its two passes.
func buildFromSource(ctx context.Context, startBlock, endBlock uint64, src BlockTxSource, idxPath string) ([]byte, error) {
	blockCount := endBlock - startBlock
	txPerBlock := make([]uint32, blockCount)
	perBlock := make([][]types.Hash, blockCount)
	totalTx := 0
	for n := startBlock; n < endBlock; n++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		hashes, err := src(n)
		if err != nil {
			return nil, fmt.Errorf("source block %d: %w", n, err)
		}
		perBlock[n-startBlock] = hashes
		txPerBlock[n-startBlock] = uint32(len(hashes))
		totalTx += len(hashes)
	}

	rs, err := recsplit.NewRecSplit(recsplit.RecSplitArgs{
		KeyCount:   totalTx,
		BucketSize: 2000,
		LeafSize:   8,
		// Enums halves the index: the per-key ordinal becomes an Elias-Fano
		// enumeration instead of a fixed-width field. LessFalsePositives keeps
		// the 8-bit existence fingerprint, which multi-segment lookup needs --
		// without it every out-of-set hash gets a phantom ordinal in every
		// segment and a newer segment answers for a transaction that lives in
		// an older one.
		Enums:              true,
		LessFalsePositives: true,
		IndexFile:          idxPath,
		BaseDataID:         startBlock,
		TmpDir:             etlTmpDir(),
	}, log2.New())
	if err != nil {
		return nil, err
	}
	if totalTx == 0 {
		if err := rs.Build(ctx); err != nil {
			return nil, err
		}
		return buildEmptyDatV2(blockCount), nil
	}

	// Add keys in BLOCK order, not sorted order.
	//
	// With Enums on, RecSplit's reader returns a key's INSERTION index, not the
	// value passed to AddKey. The Elias-Fano block boundaries then map that
	// index to a block by counting transactions per block, which only works
	// while insertion order is block order. Sorting the keys first -- the
	// obvious thing to do for a batch write, and what makes the MDBX table
	// cheap -- silently returns the wrong block here: a transaction in block
	// 1036 resolved to 1029, because its sorted position landed in another
	// block's ordinal range. Sorting belongs on the random-key B-tree write
	// path, where key order is what costs pages; it is actively wrong on this
	// one, where position carries meaning.
	ordinal := uint64(0)
	for _, hashes := range perBlock {
		for i := range hashes {
			if err := rs.AddKey(hashes[i][:], ordinal); err != nil {
				return nil, fmt.Errorf("addKey: %w", err)
			}
			ordinal++
		}
	}
	if err := rs.Build(ctx); err != nil {
		return nil, fmt.Errorf("recsplit build: %w", err)
	}
	log.Info("txindex segment built from source",
		"blocks", fmt.Sprintf("%d-%d", startBlock, endBlock-1), "txs", totalTx)
	return buildDatV2Bytes(blockCount, uint64(totalTx), txPerBlock), nil
}

// appendSegmentRange records a segment's range so a reader can enumerate
// segments that are not all SegmentSize wide.
func appendSegmentRange(dir string, r SegmentRange) error {
	f, err := os.OpenFile(filepath.Join(dir, rangesFileName), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "%d %d\n", r.StartBlock, r.BlockCount)
	return err
}

// readSegmentRanges returns the recorded ranges, or nil when the file is
// absent — which means the store predates variable sizing and every segment is
// SegmentSize wide.
func readSegmentRanges(dir string) []SegmentRange {
	data, err := os.ReadFile(filepath.Join(dir, rangesFileName))
	if err != nil {
		return nil
	}
	var out []SegmentRange
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var r SegmentRange
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		if r.StartBlock, err = strconv.ParseUint(f[0], 10, 64); err != nil {
			continue
		}
		if r.BlockCount, err = strconv.ParseUint(f[1], 10, 64); err != nil {
			continue
		}
		out = append(out, r)
	}
	return out
}

// datBlockCount reads the block count out of v2 dat bytes; 0 when it is not v2.
func datBlockCount(dat []byte) uint64 {
	if len(dat) < 16 || !bytes.Equal(dat[:4], datMagicV2[:]) {
		return 0
	}
	return uint64(binary.LittleEndian.Uint32(dat[4:8]))
}

// SealedEnd returns the first block NOT covered by a segment, or 0 when the
// store has no recorded ranges. A live tier uses it to know where its tail has
// to start after a restart.
func SealedEnd(dir string) uint64 {
	var end uint64
	for _, r := range readSegmentRanges(dir) {
		if e := r.StartBlock + r.BlockCount; e > end {
			end = e
		}
	}
	return end
}
