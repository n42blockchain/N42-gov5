// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// header_compact.go — columnar header compression.
//
// Instead of storing each block header as a complete RLP blob, fields are
// separated into columns and compressed with the best strategy per column:
//
//   - Hash columns: adaptive — if unique count < total, use dictionary + varint
//     indices (e.g. early chain: txRoot/receiptRoot are constant for empty blocks;
//     fork transitions: withdrawalsHash all zero). Otherwise raw 32B × N.
//   - Dictionary columns (coinbase, extraData): deduplicated, varint-indexed
//   - Delta columns (gasLimit, timestamp, baseFee, …): zigzag-varint deltas
//   - Eliminated fields: parentHash (derivable), bloom (recompute from receipts),
//     number (= index), difficulty/nonce/uncleHash (constant post-merge)
//
// Segments of 8192 blocks are independently zstd-compressed.

package ethel

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/holiman/uint256"
	"github.com/klauspost/compress/zstd"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

// HeaderSegmentSize is the number of blocks per columnar segment.
const HeaderSegmentSize = 8192

type hdrFlags uint16

const (
	hfBaseFee      hdrFlags = 1 << 0 // London+
	hfWithdrawals  hdrFlags = 1 << 1 // Shanghai+
	hfBlobGas      hdrFlags = 1 << 2 // Cancun+
	hfBeaconRoot   hdrFlags = 1 << 3 // Cancun+
	hfRequestsHash hdrFlags = 1 << 4 // Pectra+
	hfPostMerge    hdrFlags = 1 << 5 // difficulty=0
)

func detectHdrFlags(headers []*block.Header) hdrFlags {
	var f hdrFlags
	postMerge := true
	for _, h := range headers {
		if h.BaseFee != nil {
			f |= hfBaseFee
		}
		if h.WithdrawalsHash != nil {
			f |= hfWithdrawals
		}
		if h.BlobGasUsed != nil {
			f |= hfBlobGas
		}
		if h.ParentBeaconRoot != nil {
			f |= hfBeaconRoot
		}
		if h.RequestsHash != nil {
			f |= hfRequestsHash
		}
		if h.Difficulty != nil && !h.Difficulty.IsZero() {
			postMerge = false
		}
	}
	if postMerge {
		f |= hfPostMerge
	}
	return f
}

// encodeHashColumn writes a column of 32-byte hashes with adaptive encoding:
//
//	[1B mode]: 0 = raw (32B × count), 1 = dictionary (varint dictLen + 32B × dictLen + varint indices)
//
// Dictionary mode is used when unique hashes < 50% of count (saves space).
// Raw mode is used for high-entropy columns where every hash is unique.
func encodeHashColumn(buf []byte, hashes []types.Hash) []byte {
	n := len(hashes)

	// Count unique hashes. Early exit to raw mode if diversity is high,
	// avoiding the cost of building a full dictionary for high-entropy columns.
	seen := map[types.Hash]int{}
	var dict []types.Hash
	earlyRaw := false
	for i, h := range hashes {
		if _, ok := seen[h]; !ok {
			seen[h] = len(dict)
			dict = append(dict, h)
		}
		// After scanning 256 entries, if >50% are unique, raw mode will win.
		if i == 255 && len(dict) > 128 {
			earlyRaw = true
			break
		}
	}

	// Dictionary mode: dictLen*32 + count*~1-2B varint vs raw: count*32.
	dictCost := len(dict)*32 + n*2
	rawCost := n * 32
	if !earlyRaw && dictCost < rawCost {
		buf = append(buf, 1) // mode = dictionary
		buf = appendVarint(buf, uint64(len(dict)))
		for _, h := range dict {
			buf = append(buf, h[:]...)
		}
		for _, h := range hashes {
			buf = appendVarint(buf, uint64(seen[h]))
		}
	} else {
		buf = append(buf, 0) // mode = raw
		for _, h := range hashes {
			buf = append(buf, h[:]...)
		}
	}
	return buf
}

// encodeHeaderSegment encodes headers in columnar format, returns zstd-compressed data.
//
// Layout (before zstd):
//
//	[2B LE: flags] [2B LE: count]
//	Hash columns (adaptive dict/raw): stateRoot, txRoot, receiptRoot, mixDigest,
//	  [withdrawalsHash], [parentBeaconRoot], [requestsHash], [uncleHash if pre-merge]
//	Coinbase dict: [varint dictLen][20B × dictLen][varint indices × count]
//	ExtraData dict: [varint dictLen][per entry: varint len + data][varint indices × count]
//	Delta columns: gasLimit, gasUsed, timestamp, [difficulty], [baseFee],
//	  [blobGasUsed, excessBlobGas]
//	Pre-merge nonce: [8B × count]
//
// NOT stored: parentHash (derivable), bloom (recompute), number (= index),
//
//	difficulty/nonce/uncleHash (post-merge = constant).
func encodeHeaderSegment(headers []*block.Header, enc *zstd.Encoder) []byte {
	n := len(headers)
	f := detectHdrFlags(headers)

	buf := make([]byte, 0, n*200)

	// Segment header.
	buf = append(buf, byte(f), byte(f>>8))
	buf = append(buf, byte(n), byte(n>>8))

	// ---- Hash columns (adaptive dict/raw) ----
	stateRoots := make([]types.Hash, n)
	txRoots := make([]types.Hash, n)
	receiptRoots := make([]types.Hash, n)
	mixDigests := make([]types.Hash, n)
	for i, h := range headers {
		stateRoots[i] = h.Root
		txRoots[i] = h.TxHash
		receiptRoots[i] = h.ReceiptHash
		mixDigests[i] = h.MixDigest
	}
	buf = encodeHashColumn(buf, stateRoots)
	buf = encodeHashColumn(buf, txRoots)
	buf = encodeHashColumn(buf, receiptRoots)
	buf = encodeHashColumn(buf, mixDigests)

	if f&hfWithdrawals != 0 {
		col := make([]types.Hash, n)
		for i, h := range headers {
			if h.WithdrawalsHash != nil {
				col[i] = *h.WithdrawalsHash
			}
		}
		buf = encodeHashColumn(buf, col)
	}
	if f&hfBeaconRoot != 0 {
		col := make([]types.Hash, n)
		for i, h := range headers {
			if h.ParentBeaconRoot != nil {
				col[i] = *h.ParentBeaconRoot
			}
		}
		buf = encodeHashColumn(buf, col)
	}
	if f&hfRequestsHash != 0 {
		col := make([]types.Hash, n)
		for i, h := range headers {
			if h.RequestsHash != nil {
				col[i] = *h.RequestsHash
			}
		}
		buf = encodeHashColumn(buf, col)
	}
	if f&hfPostMerge == 0 {
		col := make([]types.Hash, n)
		for i, h := range headers {
			col[i] = h.UncleHash
		}
		buf = encodeHashColumn(buf, col)
	}

	// ---- Coinbase dictionary ----
	addrMap := map[types.Address]int{}
	var addrs []types.Address
	for _, h := range headers {
		if _, ok := addrMap[h.Coinbase]; !ok {
			addrMap[h.Coinbase] = len(addrs)
			addrs = append(addrs, h.Coinbase)
		}
	}
	buf = appendVarint(buf, uint64(len(addrs)))
	for _, a := range addrs {
		buf = append(buf, a[:]...)
	}
	for _, h := range headers {
		buf = appendVarint(buf, uint64(addrMap[h.Coinbase]))
	}

	// ---- ExtraData dictionary ----
	extraMap := map[string]int{}
	var extras [][]byte
	for _, h := range headers {
		key := string(h.Extra)
		if _, ok := extraMap[key]; !ok {
			extraMap[key] = len(extras)
			extras = append(extras, h.Extra)
		}
	}
	buf = appendVarint(buf, uint64(len(extras)))
	for _, e := range extras {
		buf = appendVarint(buf, uint64(len(e)))
		buf = append(buf, e...)
	}
	for _, h := range headers {
		buf = appendVarint(buf, uint64(extraMap[string(h.Extra)]))
	}

	// ---- Delta columns ----
	gasLimits := make([]uint64, n)
	gasUseds := make([]uint64, n)
	timestamps := make([]uint64, n)
	for i, h := range headers {
		gasLimits[i] = h.GasLimit
		gasUseds[i] = h.GasUsed
		timestamps[i] = h.Time
	}
	buf = encodeDeltaU64(buf, gasLimits)
	buf = encodeDeltaU64(buf, gasUseds)
	buf = encodeDeltaU64(buf, timestamps)

	if f&hfPostMerge == 0 {
		diffs := make([]uint64, n)
		for i, h := range headers {
			if h.Difficulty != nil {
				diffs[i] = h.Difficulty.Uint64()
			}
		}
		buf = encodeDeltaU64(buf, diffs)
	}
	if f&hfBaseFee != 0 {
		baseFees := make([]uint64, n)
		for i, h := range headers {
			if h.BaseFee != nil {
				baseFees[i] = h.BaseFee.Uint64()
			}
		}
		buf = encodeDeltaU64(buf, baseFees)
	}
	if f&hfBlobGas != 0 {
		blobUsed := make([]uint64, n)
		excessBlob := make([]uint64, n)
		for i, h := range headers {
			if h.BlobGasUsed != nil {
				blobUsed[i] = *h.BlobGasUsed
			}
			if h.ExcessBlobGas != nil {
				excessBlob[i] = *h.ExcessBlobGas
			}
		}
		buf = encodeDeltaU64(buf, blobUsed)
		buf = encodeDeltaU64(buf, excessBlob)
	}

	// ---- Pre-merge nonce (8B × count) ----
	if f&hfPostMerge == 0 {
		for _, h := range headers {
			buf = append(buf, h.Nonce[:]...)
		}
	}

	return enc.EncodeAll(buf, make([]byte, 0, len(buf)/2))
}

// encodeDeltaU64 writes a uint64 column as base value + zigzag-varint deltas.
func encodeDeltaU64(buf []byte, values []uint64) []byte {
	if len(values) == 0 {
		return buf
	}
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], values[0])
	buf = append(buf, b[:]...)
	for i := 1; i < len(values); i++ {
		delta := int64(values[i]) - int64(values[i-1])
		u := uint64((delta << 1) ^ (delta >> 63)) // zigzag encode
		buf = appendVarint(buf, u)
	}
	return buf
}

// ---------- Stage ----------

// HeaderCompactStage reads Geth ancient headers and writes columnar-compressed segments.
type HeaderCompactStage struct {
	inputFreezer *freezer.Freezer
	outputPath   string
}

func NewHeaderCompactStage(input *freezer.Freezer, outputPath string) *HeaderCompactStage {
	return &HeaderCompactStage{inputFreezer: input, outputPath: outputPath}
}

func (s *HeaderCompactStage) Run(ctx context.Context) error {
	endBlock := s.inputFreezer.Frozen()

	// Open data file (headers.cdat).
	out, err := os.Create(s.outputPath)
	if err != nil {
		return err
	}
	defer out.Close()

	// Open index file (headers.cidx) — one 8B LE offset per segment.
	// To read block N: seg = N/8192, offset = idx[seg*8 .. seg*8+8],
	// seek headers.cdat to offset, read [4B size][zstd data].
	idxPath := strings.TrimSuffix(s.outputPath, ".cdat") + ".cidx"
	idx, err := os.Create(idxPath)
	if err != nil {
		return err
	}
	defer idx.Close()

	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBestCompression))
	if err != nil {
		return err
	}
	defer enc.Close()

	log.Info("Header compact starting", "blocks", endBlock,
		"segmentSize", HeaderSegmentSize,
		"data", s.outputPath, "index", idxPath)

	var totalGethBytes, totalCompactBytes int64
	var fileOffset int64
	var segCount int
	t0 := time.Now()

	for startBlock := uint64(0); startBlock < endBlock; startBlock += HeaderSegmentSize {
		if ctx.Err() != nil {
			break
		}

		count := uint64(HeaderSegmentSize)
		if startBlock+count > endBlock {
			count = endBlock - startBlock
		}

		// Read and decode headers.
		headers := make([]*block.Header, 0, count)
		for i := uint64(0); i < count; i++ {
			data, err := s.inputFreezer.Ancient(freezer.TableHeaders, startBlock+i)
			if err != nil {
				return fmt.Errorf("read header %d: %w", startBlock+i, err)
			}
			totalGethBytes += int64(len(data))

			h, err := DecodeGethHeader(data)
			if err != nil {
				return fmt.Errorf("decode header %d: %w", startBlock+i, err)
			}
			headers = append(headers, h)
		}

		// Encode columnar segment.
		compressed := encodeHeaderSegment(headers, enc)

		// Write index entry: offset of this segment in headers.cdat.
		var offBuf [8]byte
		binary.LittleEndian.PutUint64(offBuf[:], uint64(fileOffset))
		if _, err := idx.Write(offBuf[:]); err != nil {
			return err
		}

		// Write framed data: [4B LE size][zstd data].
		var sizeBuf [4]byte
		binary.LittleEndian.PutUint32(sizeBuf[:], uint32(len(compressed)))
		if _, err := out.Write(sizeBuf[:]); err != nil {
			return err
		}
		if _, err := out.Write(compressed); err != nil {
			return err
		}
		segSize := int64(4 + len(compressed))
		fileOffset += segSize
		totalCompactBytes += segSize
		segCount++

		if segCount%100 == 0 {
			elapsed := time.Since(t0)
			pct := float64(startBlock+count) / float64(endBlock) * 100
			ratio := float64(totalCompactBytes) / float64(totalGethBytes) * 100
			blkPerSec := float64(startBlock+count) / elapsed.Seconds()
			log.Info("Header compact",
				"block", startBlock+count-1,
				"pct", fmt.Sprintf("%.1f%%", pct),
				"ratio", fmt.Sprintf("%.1f%%", ratio),
				"blk/s", fmt.Sprintf("%.0f", blkPerSec))
		}
	}

	if totalGethBytes == 0 {
		log.Info("Header compact: no blocks to process")
		return nil
	}
	ratio := float64(totalCompactBytes) / float64(totalGethBytes) * 100
	log.Info("Header compact complete",
		"blocks", endBlock, "segments", segCount,
		"geth", fmt.Sprintf("%.2f GB", float64(totalGethBytes)/1e9),
		"compact", fmt.Sprintf("%.2f GB", float64(totalCompactBytes)/1e9),
		"ratio", fmt.Sprintf("%.1f%%", ratio),
		"saved", fmt.Sprintf("%.1f%%", 100-ratio),
		"idx", fmt.Sprintf("%d entries, %d bytes", segCount, segCount*8),
		"elapsed", time.Since(t0).Truncate(time.Second))
	return nil
}

// ---------- Reader ----------

// HeaderCompactReader provides random and sequential access to headers
// stored in headers.cdat + headers.cidx. The current segment is cached so
// that consecutive reads within the same 8192-block segment are free.
type HeaderCompactReader struct {
	dataFile *os.File
	idxFile  *os.File
	dec      *zstd.Decoder
	segments uint64 // total segments in index

	// Segment cache — holds the last decompressed segment.
	cachedSeg     int64           // -1 = none
	cachedHeaders []*block.Header // decoded headers for cachedSeg
}

// OpenHeaderCompact opens a headers.cdat + headers.cidx pair for reading.
func OpenHeaderCompact(dataPath string) (*HeaderCompactReader, error) {
	df, err := os.Open(dataPath)
	if err != nil {
		return nil, fmt.Errorf("open data: %w", err)
	}
	idxPath := strings.TrimSuffix(dataPath, ".cdat") + ".cidx"
	idf, err := os.Open(idxPath)
	if err != nil {
		df.Close()
		return nil, fmt.Errorf("open index: %w", err)
	}
	fi, err := idf.Stat()
	if err != nil {
		df.Close()
		idf.Close()
		return nil, err
	}
	dec, err := zstd.NewReader(nil)
	if err != nil {
		df.Close()
		idf.Close()
		return nil, err
	}
	return &HeaderCompactReader{
		dataFile:  df,
		idxFile:   idf,
		dec:       dec,
		segments:  uint64(fi.Size()) / 8,
		cachedSeg: -1,
	}, nil
}

// Close releases all resources.
func (r *HeaderCompactReader) Close() {
	r.dec.Close()
	r.idxFile.Close()
	r.dataFile.Close()
}

// Segments returns the number of segments in the file.
func (r *HeaderCompactReader) Segments() uint64 { return r.segments }

// MaxBlock returns the maximum block number available (exclusive upper bound).
func (r *HeaderCompactReader) MaxBlock() uint64 { return r.segments * HeaderSegmentSize }

// ReadHeader returns the header for blockNum. Sequential reads within the
// same segment hit the cache (zero IO, zero decompression).
func (r *HeaderCompactReader) ReadHeader(blockNum uint64) (*block.Header, error) {
	seg := int64(blockNum / HeaderSegmentSize)
	idx := int(blockNum % HeaderSegmentSize)

	if seg != r.cachedSeg {
		if err := r.loadSegment(seg); err != nil {
			return nil, fmt.Errorf("block %d: %w", blockNum, err)
		}
	}
	if idx >= len(r.cachedHeaders) {
		return nil, fmt.Errorf("block %d: index %d out of range (segment has %d)",
			blockNum, idx, len(r.cachedHeaders))
	}
	return r.cachedHeaders[idx], nil
}

// loadSegment reads and decodes segment segNum, populating the cache.
func (r *HeaderCompactReader) loadSegment(segNum int64) error {
	if uint64(segNum) >= r.segments {
		return fmt.Errorf("segment %d out of range (%d segments)", segNum, r.segments)
	}

	// Read offset from index.
	var offBuf [8]byte
	if _, err := r.idxFile.ReadAt(offBuf[:], segNum*8); err != nil {
		return fmt.Errorf("read index: %w", err)
	}
	offset := int64(binary.LittleEndian.Uint64(offBuf[:]))

	// Read framed data: [4B size][compressed].
	var sizeBuf [4]byte
	if _, err := r.dataFile.ReadAt(sizeBuf[:], offset); err != nil {
		return fmt.Errorf("read size: %w", err)
	}
	compSize := binary.LittleEndian.Uint32(sizeBuf[:])

	compressed := make([]byte, compSize)
	if _, err := r.dataFile.ReadAt(compressed, offset+4); err != nil {
		return fmt.Errorf("read data: %w", err)
	}

	raw, err := r.dec.DecodeAll(compressed, nil)
	if err != nil {
		return fmt.Errorf("decompress segment %d: %w", segNum, err)
	}

	headers, err := decodeHeaderSegment(raw)
	if err != nil {
		return fmt.Errorf("decode segment %d: %w", segNum, err)
	}
	// Fill in Number field (not stored in columnar format, = segment_start + index).
	baseBlock := uint64(segNum) * HeaderSegmentSize
	for i, h := range headers {
		if h.Number == nil {
			h.Number = new(uint256.Int).SetUint64(baseBlock + uint64(i))
		}
	}

	r.cachedSeg = segNum
	r.cachedHeaders = headers
	return nil
}

// ---------- Segment Decoder ----------

// decodeHeaderSegment is the inverse of encodeHeaderSegment.
func decodeHeaderSegment(data []byte) ([]*block.Header, error) {
	if len(data) < 4 {
		return nil, fmt.Errorf("segment too short")
	}
	f := hdrFlags(binary.LittleEndian.Uint16(data[0:2]))
	n := int(binary.LittleEndian.Uint16(data[2:4]))
	pos := 4

	headers := make([]*block.Header, n)
	for i := range headers {
		headers[i] = &block.Header{}
	}

	// ---- Hash columns ----
	readHashCol := func(name string) ([]types.Hash, error) {
		col, newPos, err := decodeHashColumn(data, pos, n)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		pos = newPos
		return col, nil
	}

	stateRoots, err := readHashCol("stateRoot")
	if err != nil {
		return nil, err
	}
	txRoots, err := readHashCol("txRoot")
	if err != nil {
		return nil, err
	}
	receiptRoots, err := readHashCol("receiptRoot")
	if err != nil {
		return nil, err
	}
	mixDigests, err := readHashCol("mixDigest")
	if err != nil {
		return nil, err
	}
	for i, h := range headers {
		h.Root = stateRoots[i]
		h.TxHash = txRoots[i]
		h.ReceiptHash = receiptRoots[i]
		h.MixDigest = mixDigests[i]
	}

	if f&hfWithdrawals != 0 {
		col, err := readHashCol("withdrawalsHash")
		if err != nil {
			return nil, err
		}
		for i, h := range headers {
			v := col[i]
			h.WithdrawalsHash = &v
		}
	}
	if f&hfBeaconRoot != 0 {
		col, err := readHashCol("parentBeaconRoot")
		if err != nil {
			return nil, err
		}
		for i, h := range headers {
			v := col[i]
			h.ParentBeaconRoot = &v
		}
	}
	if f&hfRequestsHash != 0 {
		col, err := readHashCol("requestsHash")
		if err != nil {
			return nil, err
		}
		for i, h := range headers {
			v := col[i]
			h.RequestsHash = &v
		}
	}
	if f&hfPostMerge == 0 {
		col, err := readHashCol("uncleHash")
		if err != nil {
			return nil, err
		}
		for i, h := range headers {
			h.UncleHash = col[i]
		}
	}

	// ---- Coinbase dictionary ----
	pos, err = decodeDictAddr(data, pos, n, headers)
	if err != nil {
		return nil, fmt.Errorf("coinbase: %w", err)
	}

	// ---- ExtraData dictionary ----
	pos, err = decodeDictExtra(data, pos, n, headers)
	if err != nil {
		return nil, fmt.Errorf("extraData: %w", err)
	}

	// ---- Delta columns ----
	readDelta := func(name string) ([]uint64, error) {
		vals, newPos, err := decodeDeltaU64(data, pos, n)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		pos = newPos
		return vals, nil
	}

	gasLimits, err := readDelta("gasLimit")
	if err != nil {
		return nil, err
	}
	gasUseds, err := readDelta("gasUsed")
	if err != nil {
		return nil, err
	}
	timestamps, err := readDelta("timestamp")
	if err != nil {
		return nil, err
	}
	for i, h := range headers {
		h.GasLimit = gasLimits[i]
		h.GasUsed = gasUseds[i]
		h.Time = timestamps[i]
	}

	if f&hfPostMerge == 0 {
		diffs, err := readDelta("difficulty")
		if err != nil {
			return nil, err
		}
		for i, h := range headers {
			h.Difficulty = uint256.NewInt(diffs[i])
		}
	} else {
		for _, h := range headers {
			h.Difficulty = uint256.NewInt(0)
		}
	}

	if f&hfBaseFee != 0 {
		baseFees, err := readDelta("baseFee")
		if err != nil {
			return nil, err
		}
		for i, h := range headers {
			h.BaseFee = uint256.NewInt(baseFees[i])
		}
	}

	if f&hfBlobGas != 0 {
		blobUsed, err := readDelta("blobGasUsed")
		if err != nil {
			return nil, err
		}
		excessBlob, err := readDelta("excessBlobGas")
		if err != nil {
			return nil, err
		}
		for i, h := range headers {
			bu := blobUsed[i]
			eb := excessBlob[i]
			h.BlobGasUsed = &bu
			h.ExcessBlobGas = &eb
		}
	}

	// ---- Pre-merge nonce ----
	if f&hfPostMerge == 0 {
		for i, h := range headers {
			if pos+8 > len(data) {
				return nil, fmt.Errorf("nonce %d: truncated", i)
			}
			copy(h.Nonce[:], data[pos:pos+8])
			pos += 8
		}
	}

	return headers, nil
}

// decodeHashColumn reads a hash column (mode byte + dict/raw data).
func decodeHashColumn(data []byte, pos, n int) ([]types.Hash, int, error) {
	if pos >= len(data) {
		return nil, pos, io.ErrUnexpectedEOF
	}
	mode := data[pos]
	pos++

	hashes := make([]types.Hash, n)

	if mode == 1 {
		// Dictionary mode.
		dictLen, vn := binary.Uvarint(data[pos:])
		if vn <= 0 {
			return nil, pos, fmt.Errorf("bad dict len varint")
		}
		pos += vn
		dict := make([]types.Hash, dictLen)
		for i := uint64(0); i < dictLen; i++ {
			if pos+32 > len(data) {
				return nil, pos, io.ErrUnexpectedEOF
			}
			copy(dict[i][:], data[pos:pos+32])
			pos += 32
		}
		for i := 0; i < n; i++ {
			idx, vn := binary.Uvarint(data[pos:])
			if vn <= 0 {
				return nil, pos, fmt.Errorf("bad dict index varint")
			}
			pos += vn
			if idx >= dictLen {
				return nil, pos, fmt.Errorf("dict index %d out of range %d", idx, dictLen)
			}
			hashes[i] = dict[idx]
		}
	} else {
		// Raw mode.
		for i := 0; i < n; i++ {
			if pos+32 > len(data) {
				return nil, pos, io.ErrUnexpectedEOF
			}
			copy(hashes[i][:], data[pos:pos+32])
			pos += 32
		}
	}
	return hashes, pos, nil
}

// decodeDictAddr reads a coinbase address dictionary column.
func decodeDictAddr(data []byte, pos, n int, headers []*block.Header) (int, error) {
	dictLen, vn := binary.Uvarint(data[pos:])
	if vn <= 0 {
		return pos, fmt.Errorf("bad dict len")
	}
	pos += vn
	dict := make([]types.Address, dictLen)
	for i := uint64(0); i < dictLen; i++ {
		if pos+20 > len(data) {
			return pos, io.ErrUnexpectedEOF
		}
		copy(dict[i][:], data[pos:pos+20])
		pos += 20
	}
	for i := 0; i < n; i++ {
		idx, vn := binary.Uvarint(data[pos:])
		if vn <= 0 {
			return pos, fmt.Errorf("bad index varint")
		}
		pos += vn
		if idx >= dictLen {
			return pos, fmt.Errorf("addr index %d out of range %d", idx, dictLen)
		}
		headers[i].Coinbase = dict[idx]
	}
	return pos, nil
}

// decodeDictExtra reads an extraData dictionary column.
func decodeDictExtra(data []byte, pos, n int, headers []*block.Header) (int, error) {
	dictLen, vn := binary.Uvarint(data[pos:])
	if vn <= 0 {
		return pos, fmt.Errorf("bad dict len")
	}
	pos += vn
	dict := make([][]byte, dictLen)
	for i := uint64(0); i < dictLen; i++ {
		dataLen, vn := binary.Uvarint(data[pos:])
		if vn <= 0 {
			return pos, fmt.Errorf("bad extra len varint")
		}
		pos += vn
		if pos+int(dataLen) > len(data) {
			return pos, io.ErrUnexpectedEOF
		}
		dict[i] = make([]byte, dataLen)
		copy(dict[i], data[pos:pos+int(dataLen)])
		pos += int(dataLen)
	}
	for i := 0; i < n; i++ {
		idx, vn := binary.Uvarint(data[pos:])
		if vn <= 0 {
			return pos, fmt.Errorf("bad index varint")
		}
		pos += vn
		if idx >= dictLen {
			return pos, fmt.Errorf("extra index %d out of range %d", idx, dictLen)
		}
		headers[i].Extra = dict[idx]
	}
	return pos, nil
}

// decodeDeltaU64 reads a delta-encoded uint64 column.
func decodeDeltaU64(data []byte, pos, n int) ([]uint64, int, error) {
	if n == 0 {
		return nil, pos, nil
	}
	if pos+8 > len(data) {
		return nil, pos, io.ErrUnexpectedEOF
	}
	values := make([]uint64, n)
	values[0] = binary.LittleEndian.Uint64(data[pos : pos+8])
	pos += 8

	for i := 1; i < n; i++ {
		u, vn := binary.Uvarint(data[pos:])
		if vn <= 0 {
			return nil, pos, fmt.Errorf("bad delta varint at %d", i)
		}
		pos += vn
		// zigzag decode
		delta := int64((u >> 1) ^ -(u & 1))
		values[i] = uint64(int64(values[i-1]) + delta)
	}
	return values, pos, nil
}
