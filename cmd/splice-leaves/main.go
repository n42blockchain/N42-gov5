// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// splice-leaves rebuilds a leaves freezer table by raw-copying
// batch-64 compressed blobs from two sources at a batch-aligned cutover.
//
// Semantics:
//   - Phase 1: batches [0, cutover/64) come from src1 verbatim (raw zstd frames)
//   - Phase 2: batches [cutover/64, src2Items/64) come from src2 verbatim
//   - Destination FreezerTable applies natural 2GB file rotation to the
//     concatenated stream, producing a valid, readable freezer table.
//   - No decompression + recompression: each batch blob is byte-identical
//     to its source blob, preserving frame-level content.
//
// Usage:
//
//	splice-leaves --src1 d:/n42-eth/chain/freezer \
//	              --src2 d:/n42-eth037/chain/freezer \
//	              --dst  d:/n42-eth002/chain/freezer \
//	              --cutover 7013248

package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

const (
	indexEntrySize = 6
	batchSize      = 64 // freezer.BatchSize
	cidxHeaderSize = 16 // freezer.cidxHeaderSize — present when first 4 bytes == "NCIX"
)

var cidxMagic = [4]byte{'N', 'C', 'I', 'X'}

type cidxEntry struct {
	fileNum uint16
	offset  uint32
}

// rawSource reads raw compressed batch blobs directly from a freezer table's
// cidx + cdat files, bypassing the FreezerTable decompression path.
type rawSource struct {
	dir   string
	table string
	cidx  []byte
	files map[uint16]*os.File
	sizes map[uint16]int64
	items uint64
}

func openRawSource(dir, table string) (*rawSource, error) {
	cidxPath := filepath.Join(dir, table+".cidx")
	cidx, err := os.ReadFile(cidxPath)
	if err != nil {
		return nil, fmt.Errorf("read cidx: %w", err)
	}
	// Newer tables (e.g. witness/storcs/acctcs) prepend a 16-byte NCIX
	// header in front of the 6-byte index entries. Skip it when present so
	// indexEntry math operates on the entry array only.
	if len(cidx) >= cidxHeaderSize &&
		cidx[0] == cidxMagic[0] && cidx[1] == cidxMagic[1] &&
		cidx[2] == cidxMagic[2] && cidx[3] == cidxMagic[3] {
		cidx = cidx[cidxHeaderSize:]
	}
	// Floor to a multiple of indexEntrySize so that a concurrently-writing
	// freezer with a half-written trailing entry is handled gracefully.
	// Matches the freezer RO open path which ignores partial index entries.
	usable := (len(cidx) / indexEntrySize) * indexEntrySize
	return &rawSource{
		dir:   dir,
		table: table,
		cidx:  cidx[:usable],
		files: make(map[uint16]*os.File),
		sizes: make(map[uint16]int64),
		items: uint64(usable) / indexEntrySize,
	}, nil
}

func (s *rawSource) close() {
	for _, f := range s.files {
		f.Close()
	}
	s.files = nil
}

func (s *rawSource) indexEntry(item uint64) cidxEntry {
	off := item * indexEntrySize
	return cidxEntry{
		fileNum: binary.BigEndian.Uint16(s.cidx[off : off+2]),
		offset:  binary.BigEndian.Uint32(s.cidx[off+2 : off+6]),
	}
}

func (s *rawSource) openFile(fn uint16) (*os.File, int64, error) {
	if f, ok := s.files[fn]; ok {
		return f, s.sizes[fn], nil
	}
	path := filepath.Join(s.dir, fmt.Sprintf("%s.%04d.cdat", s.table, fn))
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, fmt.Errorf("open %s: %w", path, err)
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, 0, err
	}
	s.files[fn] = f
	s.sizes[fn] = fi.Size()
	return f, fi.Size(), nil
}

// readBatchBlob returns the raw compressed bytes of the given batch.
// The batch contains items [b*batchSize, (b+1)*batchSize).
func (s *rawSource) readBatchBlob(b uint64) ([]byte, error) {
	startItem := b * batchSize
	if startItem >= s.items {
		return nil, fmt.Errorf("batch %d start item %d out of range (items=%d)", b, startItem, s.items)
	}
	startEntry := s.indexEntry(startItem)
	startFile, startFileSize, err := s.openFile(startEntry.fileNum)
	if err != nil {
		return nil, err
	}

	// Determine end offset for this batch in its data file.
	//
	// Batches are atomic: AppendBatchBlob never splits a batch across files.
	// So the blob spans [startEntry.offset, endOffset) within startEntry.fileNum.
	//
	// endOffset is:
	//   a) next batch's cidx offset, if the next batch is in the same file;
	//   b) the start file's size, if the next batch starts a new file (this
	//      batch is the LAST batch in its file); or
	//   c) the start file's size, if this is the final batch in the table.
	var endOffset uint32
	nextItem := startItem + batchSize
	if nextItem < s.items {
		nextEntry := s.indexEntry(nextItem)
		if nextEntry.fileNum == startEntry.fileNum {
			endOffset = nextEntry.offset
		} else {
			endOffset = uint32(startFileSize)
		}
	} else {
		// Last batch overall: blob extends to EOF of start file.
		endOffset = uint32(startFileSize)
	}

	if endOffset < startEntry.offset {
		return nil, fmt.Errorf("batch %d: endOffset %d < startOffset %d (file %d)",
			b, endOffset, startEntry.offset, startEntry.fileNum)
	}
	if int64(endOffset) > startFileSize {
		return nil, fmt.Errorf("batch %d: endOffset %d exceeds file %d size %d",
			b, endOffset, startEntry.fileNum, startFileSize)
	}

	blobLen := endOffset - startEntry.offset
	if blobLen == 0 {
		return []byte{}, nil
	}
	blob := make([]byte, blobLen)
	if _, err := startFile.ReadAt(blob, int64(startEntry.offset)); err != nil {
		return nil, fmt.Errorf("read blob from file %d at offset %d: %w",
			startEntry.fileNum, startEntry.offset, err)
	}
	return blob, nil
}

func main() {
	var (
		src1Path = flag.String("src1", "", "source 1 freezer dir (provides batches [0, cutover/64))")
		src2Path = flag.String("src2", "", "source 2 freezer dir (provides batches [cutover/64, src2End/64))")
		dstPath  = flag.String("dst", "", "destination freezer dir (created if missing; refuses overwrite)")
		table    = flag.String("table", "leaves", "table name (default: leaves)")
		cutover  = flag.Uint64("cutover", 0, "batch-aligned cutover: src1 provides items [0, cutover), src2 provides items [cutover, ...]")
		src2End  = flag.Uint64("src2-end", 0, "batch-aligned upper bound on src2 items to copy (0 = use src2's full cidx floor-to-batch). Pin this when src2 is being actively written to freeze the splice endpoint.")
	)
	flag.Parse()

	if *src1Path == "" || *src2Path == "" || *dstPath == "" || *cutover == 0 {
		flag.Usage()
		os.Exit(2)
	}
	if *cutover%batchSize != 0 {
		fmt.Fprintf(os.Stderr, "error: cutover %d must be batch-aligned (multiple of %d)\n", *cutover, batchSize)
		os.Exit(2)
	}
	if *src2End != 0 && *src2End%batchSize != 0 {
		fmt.Fprintf(os.Stderr, "error: src2-end %d must be batch-aligned\n", *src2End)
		os.Exit(2)
	}

	if err := run(*src1Path, *src2Path, *dstPath, *table, *cutover, *src2End); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run(src1Path, src2Path, dstPath, table string, cutover, src2End uint64) error {
	t0 := time.Now()

	src1, err := openRawSource(src1Path, table)
	if err != nil {
		return fmt.Errorf("open src1: %w", err)
	}
	defer src1.close()

	src2, err := openRawSource(src2Path, table)
	if err != nil {
		return fmt.Errorf("open src2: %w", err)
	}
	defer src2.close()

	// Resolve effective src2 item count.
	src2Usable := (src2.items / batchSize) * batchSize
	if src2End > 0 {
		if src2End > src2Usable {
			return fmt.Errorf("src2-end %d exceeds src2's batch-aligned item count %d", src2End, src2Usable)
		}
		src2Usable = src2End
	}

	cutoverBatch := cutover / batchSize
	endBatch := src2Usable / batchSize

	fmt.Printf("src1: %s  items=%d  batches=%d\n", src1Path, src1.items, src1.items/batchSize)
	fmt.Printf("src2: %s  items(total)=%d  items(used)=%d  batches(used)=%d\n", src2Path, src2.items, src2Usable, endBatch)
	fmt.Printf("cutover: item %d (batch %d)\n", cutover, cutoverBatch)
	fmt.Printf("src1 contributes batches [0, %d)  = items [0, %d)\n", cutoverBatch, cutover)
	fmt.Printf("src2 contributes batches [%d, %d) = items [%d, %d)\n", cutoverBatch, endBatch, cutover, src2Usable)
	fmt.Printf("expected dst items: %d\n", src2Usable)

	if cutoverBatch*batchSize > src1.items {
		return fmt.Errorf("src1 has only %d items, cannot provide up to item %d", src1.items, cutover)
	}
	if cutover >= src2Usable {
		return fmt.Errorf("src2 has only %d usable items, cutover %d would leave nothing", src2Usable, cutover)
	}

	if err := os.MkdirAll(dstPath, 0o755); err != nil {
		return fmt.Errorf("mkdir dst: %w", err)
	}
	cidxPath := filepath.Join(dstPath, table+".cidx")
	if info, err := os.Stat(cidxPath); err == nil && info.Size() > 0 {
		return fmt.Errorf("dst already contains %s (size=%d); remove it first to rebuild", cidxPath, info.Size())
	}

	dst, err := freezer.NewFreezerTableCompressed(dstPath, table, "c")
	if err != nil {
		return fmt.Errorf("open dst: %w", err)
	}
	defer dst.Close()

	// Phase 1: src1 batches [0, cutoverBatch).
	fmt.Println()
	fmt.Printf("=== Phase 1: src1 batches [0, %d) ===\n", cutoverBatch)
	p1Start := time.Now()
	if err := copyBatches(src1, dst, 0, cutoverBatch, "src1"); err != nil {
		return fmt.Errorf("phase1: %w", err)
	}
	fmt.Printf("Phase 1 done: %d batches in %s\n", cutoverBatch, time.Since(p1Start).Truncate(time.Millisecond))

	// Phase 2: src2 batches [cutoverBatch, endBatch).
	fmt.Println()
	fmt.Printf("=== Phase 2: src2 batches [%d, %d) ===\n", cutoverBatch, endBatch)
	p2Start := time.Now()
	if err := copyBatches(src2, dst, cutoverBatch, endBatch, "src2"); err != nil {
		return fmt.Errorf("phase2: %w", err)
	}
	fmt.Printf("Phase 2 done: %d batches in %s\n", endBatch-cutoverBatch, time.Since(p2Start).Truncate(time.Millisecond))

	if err := dst.Sync(); err != nil {
		return fmt.Errorf("dst sync: %w", err)
	}

	fmt.Println()
	fmt.Printf("=== Done ===\n")
	fmt.Printf("dst items: %d\n", dst.Items())
	fmt.Printf("total elapsed: %s\n", time.Since(t0).Truncate(time.Millisecond))

	if dst.Items() != src2Usable {
		return fmt.Errorf("item count mismatch: dst=%d, expected=%d", dst.Items(), src2Usable)
	}
	return nil
}

// copyBatches reads raw batch blobs from src[startBatch, endBatch) and appends
// them verbatim to dst via AppendBatchBlob.
func copyBatches(src *rawSource, dst *freezer.FreezerTable, startBatch, endBatch uint64, label string) error {
	var (
		bytes   uint64
		lastLog = time.Now()
		t0      = time.Now()
	)
	for b := startBatch; b < endBatch; b++ {
		blob, err := src.readBatchBlob(b)
		if err != nil {
			return fmt.Errorf("%s read batch %d: %w", label, b, err)
		}
		// AppendBatchBlob handles 2GB rotation and writes one cidx entry per item.
		startItem := b * batchSize
		if err := dst.AppendBatchBlob(startItem, batchSize, blob); err != nil {
			return fmt.Errorf("%s dst append batch %d: %w", label, b, err)
		}
		bytes += uint64(len(blob))

		if time.Since(lastLog) > 5*time.Second {
			copied := b - startBatch + 1
			total := endBatch - startBatch
			rate := float64(copied) / time.Since(t0).Seconds()
			remaining := total - copied
			eta := time.Duration(float64(remaining)/rate) * time.Second
			var ms runtime.MemStats
			runtime.ReadMemStats(&ms)
			fmt.Printf("  [%s] %d/%d batches (%.1f%%) %.0f batches/s  bytes=%.2fGB  allocMB=%d  eta=%s\n",
				label, copied, total, 100*float64(copied)/float64(total),
				rate, float64(bytes)/1e9, ms.Alloc/1e6, eta.Truncate(time.Second))
			lastLog = time.Now()
		}
	}
	return nil
}
