// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// body_compact.go — columnar body compression with freezer-style multi-file rotation.
//
// Architecture:
//   - 8192 blocks per segment (matching headers.cdat)
//   - Each segment: columnar field separation + zstd compression
//   - Output: bodyc.NNNN.cdat (≤2GB each) + bodyc.cidx (8B per segment)
//   - Reader caches current segment for sequential access

package ethel

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/golang/snappy"
	"github.com/holiman/uint256"
	"github.com/klauspost/compress/zstd"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

const bodyMaxFileSize = 2_000_000_000 // 2 GB per dat file

type bodyFlags uint16

const (
	bfPostMerge     bodyFlags = 1 << 0
	bfWithdrawals   bodyFlags = 1 << 1
	bfHasBlob       bodyFlags = 1 << 2
	bfHasSetCode    bodyFlags = 1 << 3
	bfHasAccessList bodyFlags = 1 << 4

	// bfAuthVFull marks a segment whose EIP-7702 authorization V values are
	// written as a trimmed uint256 instead of a single 0/1 byte.
	//
	// The original encoder assumed V could only be y_parity (0 or 1) and wrote
	// `1` only when V == 1, folding everything else to 0. Mainnet carries
	// authorizations with V = 27 / 28, so those were silently flattened to 0 —
	// an unrecoverable loss that changes the recovered authority and, through
	// it, the executed nonce state and the block's gas.
	//
	// Segments written before this flag existed cannot recover V from the body
	// bytes alone. The decoder keeps reading their original layout; callers that
	// also have the canonical header transaction root can disambiguate the rare
	// legacy 27/28 values (witness replay does this), otherwise the segment must
	// be regenerated from a canonical body source.
	bfAuthVFull bodyFlags = 1 << 5
)

// DecodedBlock holds a decoded body for columnar encoding.
type DecodedBlock struct {
	Txs         []*transaction.Transaction
	Withdrawals []*Withdrawal
	UncleRLP    [][]byte // raw RLP per uncle (only pre-merge)
}

// ---------- Column Helpers ----------

func encodeBitpack(buf []byte, bits []bool) []byte {
	n := (len(bits) + 7) / 8
	packed := make([]byte, n)
	for i, b := range bits {
		if b {
			packed[i/8] |= 1 << (uint(i) % 8)
		}
	}
	return append(buf, packed...)
}

func decodeBitpack(data []byte, pos, count int) ([]bool, int, error) {
	n := (count + 7) / 8
	if pos+n > len(data) {
		return nil, pos, io.ErrUnexpectedEOF
	}
	bits := make([]bool, count)
	for i := 0; i < count; i++ {
		bits[i] = data[pos+i/8]&(1<<(uint(i)%8)) != 0
	}
	return bits, pos + n, nil
}

func encodeTrimmedU256(buf []byte, v *uint256.Int) []byte {
	if v == nil || v.IsZero() {
		return append(buf, 0)
	}
	b32 := v.Bytes32()
	start := 0
	for start < 31 && b32[start] == 0 {
		start++
	}
	trimLen := 32 - start
	buf = append(buf, byte(trimLen))
	return append(buf, b32[start:]...)
}

func decodeTrimmedU256(data []byte, pos int) (*uint256.Int, int, error) {
	if pos >= len(data) {
		return nil, pos, io.ErrUnexpectedEOF
	}
	trimLen := int(data[pos])
	pos++
	if trimLen == 0 {
		return uint256.NewInt(0), pos, nil
	}
	if pos+trimLen > len(data) {
		return nil, pos, io.ErrUnexpectedEOF
	}
	var b32 [32]byte
	copy(b32[32-trimLen:], data[pos:pos+trimLen])
	v := new(uint256.Int)
	v.SetBytes32(b32[:])
	return v, pos + trimLen, nil
}

// encodeAddrDictColumn encodes a column of 20-byte addresses with adaptive dictionary.
func encodeAddrDictColumn(buf []byte, addrs []types.Address, creates []bool) []byte {
	n := len(addrs)
	seen := map[types.Address]int{}
	var dict []types.Address
	earlyRaw := false
	for i, a := range addrs {
		if creates[i] {
			continue
		}
		if _, ok := seen[a]; !ok {
			seen[a] = len(dict)
			dict = append(dict, a)
		}
		// Early exit: if after 256 non-create entries >50% are unique, raw wins.
		checked := i + 1
		if checked == 256 && len(dict) > 128 {
			earlyRaw = true
			break
		}
	}

	dictCost := len(dict)*20 + n*2
	rawCost := n * 20
	if !earlyRaw && dictCost < rawCost {
		buf = append(buf, 1) // dict mode
		buf = appendVarint(buf, uint64(len(dict)))
		for _, a := range dict {
			buf = append(buf, a[:]...)
		}
		for i, a := range addrs {
			if creates[i] {
				buf = appendVarint(buf, uint64(len(dict))) // sentinel = dictLen
			} else {
				buf = appendVarint(buf, uint64(seen[a]))
			}
		}
	} else {
		buf = append(buf, 0) // raw mode
		for _, a := range addrs {
			buf = append(buf, a[:]...)
		}
	}
	return buf
}

func decodeAddrDictColumn(data []byte, pos, n int, creates []bool) ([]types.Address, int, error) {
	if pos >= len(data) {
		return nil, pos, io.ErrUnexpectedEOF
	}
	mode := data[pos]
	pos++
	addrs := make([]types.Address, n)

	if mode == 1 {
		dictLen, vn := binary.Uvarint(data[pos:])
		if vn <= 0 {
			return nil, pos, fmt.Errorf("bad dict len")
		}
		pos += vn
		dict := make([]types.Address, dictLen)
		for i := uint64(0); i < dictLen; i++ {
			if pos+20 > len(data) {
				return nil, pos, io.ErrUnexpectedEOF
			}
			copy(dict[i][:], data[pos:pos+20])
			pos += 20
		}
		for i := 0; i < n; i++ {
			idx, vn := binary.Uvarint(data[pos:])
			if vn <= 0 {
				return nil, pos, fmt.Errorf("bad index")
			}
			pos += vn
			if idx < dictLen {
				addrs[i] = dict[idx]
			}
			// idx == dictLen → contract creation, leave zero address
		}
	} else {
		for i := 0; i < n; i++ {
			if pos+20 > len(data) {
				return nil, pos, io.ErrUnexpectedEOF
			}
			copy(addrs[i][:], data[pos:pos+20])
			pos += 20
		}
	}
	return addrs, pos, nil
}

// ---------- Segment Encoder ----------

func detectBodyFlags(blocks []*DecodedBlock) bodyFlags {
	var f bodyFlags
	postMerge := true
	for _, b := range blocks {
		if len(b.UncleRLP) > 0 {
			postMerge = false
		}
		if b.Withdrawals != nil {
			f |= bfWithdrawals
		}
		for _, tx := range b.Txs {
			switch tx.Type() {
			case transaction.AccessListTxType:
				f |= bfHasAccessList
			case transaction.DynamicFeeTxType:
				f |= bfHasAccessList
			case transaction.BlobTxType:
				f |= bfHasBlob | bfHasAccessList
			case transaction.SetCodeTxType:
				// bfAuthVFull: everything this build writes carries the true
				// authorization V, not a folded 0/1 byte.
				f |= bfHasSetCode | bfHasAccessList | bfAuthVFull
			}
		}
	}
	if postMerge {
		f |= bfPostMerge
	}
	return f
}

func encodeBodySegment(blocks []*DecodedBlock, chainID uint64, enc *zstd.Encoder) []byte {
	blockCount := len(blocks)
	f := detectBodyFlags(blocks)

	// Count total txs.
	txCount := 0
	for _, b := range blocks {
		txCount += len(b.Txs)
	}

	buf := make([]byte, 0, txCount*150)

	// Segment header.
	buf = append(buf, byte(f), byte(f>>8))
	buf = append(buf, byte(blockCount), byte(blockCount>>8))
	var txcBuf [4]byte
	binary.LittleEndian.PutUint32(txcBuf[:], uint32(txCount))
	buf = append(buf, txcBuf[:]...)
	var cidBuf [8]byte
	binary.LittleEndian.PutUint64(cidBuf[:], chainID)
	buf = append(buf, cidBuf[:]...)

	// Block-level metadata.
	for _, b := range blocks {
		buf = appendVarint(buf, uint64(len(b.Txs)))
	}
	if f&bfWithdrawals != 0 {
		for _, b := range blocks {
			wdCount := 0
			if b.Withdrawals != nil {
				wdCount = len(b.Withdrawals)
			}
			buf = appendVarint(buf, uint64(wdCount))
		}
	}

	if txCount == 0 {
		// No transactions — still need to write withdrawals/uncles.
		buf = encodeNonTxColumns(buf, blocks, f)
		return enc.EncodeAll(buf, make([]byte, 0, len(buf)/2))
	}

	// ---- TX columns ----

	// Collect all tx fields into parallel arrays.
	txTypes := make([]byte, 0, txCount)
	rValues := make([]byte, 0, txCount*32)
	sValues := make([]byte, 0, txCount*32)
	vBits := make([]bool, 0, txCount)
	preEIP155Bits := make([]bool, 0, txCount)
	creates := make([]bool, 0, txCount)
	toAddrs := make([]types.Address, 0, txCount)
	nonces := make([]byte, 0, txCount*2)
	gases := make([]byte, 0, txCount*2)

	for _, b := range blocks {
		for _, tx := range b.Txs {
			txTypes = append(txTypes, tx.Type())

			v, r, s := tx.RawSignatureValues()
			var rBuf, sBuf [32]byte
			if r != nil {
				rBuf = r.Bytes32()
			}
			if s != nil {
				sBuf = s.Bytes32()
			}
			rValues = append(rValues, rBuf[:]...)
			sValues = append(sValues, sBuf[:]...)

			// V parity bit + pre-EIP-155 flag for legacy txs.
			var vBit, preEIP155 bool
			if v != nil {
				vv := v.Uint64()
				if tx.Type() == transaction.LegacyTxType {
					if vv >= 35 {
						vBit = ((vv - 35) % 2) == 1 // EIP-155
					} else {
						preEIP155 = true
						vBit = vv == 28 // pre-EIP-155: 27=even, 28=odd
					}
				} else {
					vBit = vv == 1 // EIP-2930+
				}
			}
			vBits = append(vBits, vBit)
			preEIP155Bits = append(preEIP155Bits, preEIP155)

			isCreate := tx.To() == nil
			creates = append(creates, isCreate)
			if isCreate {
				toAddrs = append(toAddrs, types.Address{})
			} else {
				toAddrs = append(toAddrs, *tx.To())
			}

			nonces = appendVarint(nonces, tx.Nonce())
			gases = appendVarint(gases, tx.Gas())
		}
	}

	// Write txType column.
	buf = append(buf, txTypes...)

	// Write R, S columns (raw 32B each).
	buf = append(buf, rValues...)
	buf = append(buf, sValues...)

	// Write V bitpack + pre-EIP-155 bitpack.
	buf = encodeBitpack(buf, vBits)
	buf = encodeBitpack(buf, preEIP155Bits)

	// Write isCreate bitpack.
	buf = encodeBitpack(buf, creates)

	// Write To address dictionary.
	buf = encodeAddrDictColumn(buf, toAddrs, creates)

	// Write nonce + gas varint columns.
	buf = append(buf, nonces...)
	buf = append(buf, gases...)

	// Value column (trimmed uint256).
	for _, b := range blocks {
		for _, tx := range b.Txs {
			buf = encodeTrimmedU256(buf, tx.Value())
		}
	}

	// GasFeeCap / GasPrice column (trimmed uint256, all txs).
	for _, b := range blocks {
		for _, tx := range b.Txs {
			buf = encodeTrimmedU256(buf, tx.GasFeeCap())
		}
	}

	// GasTipCap column (only type >= 2).
	if f&bfHasAccessList != 0 {
		for _, b := range blocks {
			for _, tx := range b.Txs {
				if tx.Type() >= transaction.DynamicFeeTxType {
					buf = encodeTrimmedU256(buf, tx.GasTipCap())
				}
			}
		}
	}

	// BlobFeeCap column (only type 3).
	if f&bfHasBlob != 0 {
		for _, b := range blocks {
			for _, tx := range b.Txs {
				if tx.Type() == transaction.BlobTxType {
					buf = encodeTrimmedU256(buf, tx.BlobFeeCap())
				}
			}
		}
	}

	// Calldata column: [varint lengths] then [concatenated bytes].
	// Two passes to avoid intermediate blob allocation (can be 100MB+).
	for _, b := range blocks {
		for _, tx := range b.Txs {
			buf = appendVarint(buf, uint64(len(tx.Data())))
		}
	}
	for _, b := range blocks {
		for _, tx := range b.Txs {
			buf = append(buf, tx.Data()...)
		}
	}

	// AccessList column.
	if f&bfHasAccessList != 0 {
		for _, b := range blocks {
			for _, tx := range b.Txs {
				if tx.Type() >= transaction.AccessListTxType {
					al := tx.AccessList()
					buf = appendVarint(buf, uint64(len(al)))
					for _, tuple := range al {
						buf = append(buf, tuple.Address[:]...)
						buf = appendVarint(buf, uint64(len(tuple.StorageKeys)))
						for _, key := range tuple.StorageKeys {
							buf = append(buf, key[:]...)
						}
					}
				}
			}
		}
	}

	// BlobHashes column (type 3 only).
	if f&bfHasBlob != 0 {
		for _, b := range blocks {
			for _, tx := range b.Txs {
				if tx.Type() == transaction.BlobTxType {
					hashes := tx.BlobHashes()
					buf = appendVarint(buf, uint64(len(hashes)))
					for _, h := range hashes {
						buf = append(buf, h[:]...)
					}
				}
			}
		}
	}

	// AuthList column (type 4 only).
	if f&bfHasSetCode != 0 {
		for _, b := range blocks {
			for _, tx := range b.Txs {
				if tx.Type() == transaction.SetCodeTxType {
					al := tx.AuthList()
					buf = appendVarint(buf, uint64(len(al)))
					for _, auth := range al {
						// ChainID as trimmed uint256.
						chainVal := auth.ChainID
						buf = encodeTrimmedU256(buf, &chainVal)
						buf = append(buf, auth.Address[:]...)
						buf = appendVarint(buf, auth.Nonce)
						// R and S as raw 32B.
						var rBuf, sBuf [32]byte
						if auth.R != nil {
							rBuf = auth.R.Bytes32()
						}
						if auth.S != nil {
							sBuf = auth.S.Bytes32()
						}
						buf = append(buf, rBuf[:]...)
						buf = append(buf, sBuf[:]...)
						// V as a trimmed uint256, not a 0/1 byte: mainnet
						// authorizations carry V = 27 / 28 as well as y_parity,
						// and flattening those to 0 silently changes the
						// recovered authority (see bfAuthVFull).
						var vVal uint256.Int
						if auth.V != nil {
							vVal = *auth.V
						}
						buf = encodeTrimmedU256(buf, &vVal)
					}
				}
			}
		}
	}

	// Non-TX columns (uncles, withdrawals).
	buf = encodeNonTxColumns(buf, blocks, f)

	return enc.EncodeAll(buf, make([]byte, 0, len(buf)/2))
}

func encodeNonTxColumns(buf []byte, blocks []*DecodedBlock, f bodyFlags) []byte {
	// Uncles (pre-merge only).
	if f&bfPostMerge == 0 {
		for _, b := range blocks {
			buf = appendVarint(buf, uint64(len(b.UncleRLP)))
			for _, u := range b.UncleRLP {
				buf = appendVarint(buf, uint64(len(u)))
				buf = append(buf, u...)
			}
		}
	}

	// Withdrawals.
	if f&bfWithdrawals != 0 {
		for _, b := range blocks {
			if b.Withdrawals == nil {
				continue
			}
			for _, w := range b.Withdrawals {
				buf = appendVarint(buf, w.Index)
				buf = appendVarint(buf, w.Validator)
				buf = append(buf, w.Address[:]...)
				buf = appendVarint(buf, w.Amount)
			}
		}
	}
	return buf
}

func firstChainID(blocks []*DecodedBlock) uint64 {
	for _, b := range blocks {
		for _, tx := range b.Txs {
			if cid := tx.ChainId(); cid != nil && !cid.IsZero() {
				return cid.Uint64()
			}
		}
	}
	return 1 // default mainnet
}

// ---------- Stage ----------

type BodyCompactStage struct {
	inputFreezer *freezer.Freezer
	outputDir    string
}

func NewBodyCompactStage(input *freezer.Freezer, outputDir string) *BodyCompactStage {
	return &BodyCompactStage{inputFreezer: input, outputDir: outputDir}
}

type bodyIdxEntry struct {
	fileNum uint16
	offset  uint64 // 48-bit on disk: low 32 in [4:8], high 16 in [2:4]
}

func encodeBodyIdx(e bodyIdxEntry) []byte {
	// 48-bit offset: low 32 bits stay in [4:8] and the previously reserved
	// [2:4] carries the high 16 bits, so every legacy entry (high == 0)
	// decodes unchanged. uint32 offsets silently WRAPPED once a dat file
	// grew past 4.29 GB (legacy orphan bytes pushed appends there), making
	// entries point into stale bytes — the "magic number mismatch" class.
	var buf [8]byte
	binary.LittleEndian.PutUint16(buf[0:2], e.fileNum)
	binary.LittleEndian.PutUint16(buf[2:4], uint16(e.offset>>32))
	binary.LittleEndian.PutUint32(buf[4:8], uint32(e.offset))
	return buf[:]
}

func decodeBodyIdx(buf []byte) bodyIdxEntry {
	return bodyIdxEntry{
		fileNum: binary.LittleEndian.Uint16(buf[0:2]),
		offset: uint64(binary.LittleEndian.Uint32(buf[4:8])) |
			uint64(binary.LittleEndian.Uint16(buf[2:4]))<<32,
	}
}

func (s *BodyCompactStage) Run(ctx context.Context) error {
	// Ensure bodies table is opened (not in coreTableSpecs).
	if _, err := s.inputFreezer.EnsureTable(freezer.TableBodies, "c"); err != nil {
		return fmt.Errorf("open bodies table: %w", err)
	}

	endBlock := s.inputFreezer.Frozen()
	if err := os.MkdirAll(s.outputDir, 0755); err != nil {
		return err
	}

	// Determine resume point from existing idx.
	idxPath := filepath.Join(s.outputDir, "bodyc.cidx")
	idxFile, err := os.OpenFile(idxPath, os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		return err
	}
	defer idxFile.Close()

	idxInfo, err := idxFile.Stat()
	if err != nil {
		return err
	}
	existingSegments := uint64(idxInfo.Size()) / 8
	// A PARTIAL final segment must be REWRITTEN, not counted — same hole
	// class as header_compact (see its resume comment): probing the last
	// expected index detects it; rewind + truncate rewrites it from source.
	for existingSegments > 0 {
		rd, rerr := OpenBodyCompact(s.outputDir)
		if rerr != nil {
			break
		}
		lastFull := existingSegments*HeaderSegmentSize - 1
		_, perr := rd.ReadBody(lastFull)
		rd.Close()
		if perr == nil {
			break // final segment is full — resume after it
		}
		// Historical sessions each ended mid-segment, so partials can stack:
		// peel them one at a time (each truncation exposes the previous
		// session's tail segment as the new final).
		log.Warn("Body compact: final segment PARTIAL — rewinding to rewrite it",
			"segment", existingSegments-1, "probe", lastFull, "err", perr)
		existingSegments--
		if err := idxFile.Truncate(int64(existingSegments) * 8); err != nil {
			return fmt.Errorf("truncate partial idx entry: %w", err)
		}
	}
	startBlock := existingSegments * HeaderSegmentSize
	if startBlock >= endBlock {
		log.Info("Body compact: already up to date", "at", startBlock)
		return nil
	}
	idxFile.Seek(0, io.SeekEnd)
	idxBuf := bufio.NewWriter(idxFile)

	// Determine head dat file and size from last idx entry.
	var headFile uint16
	var headSize int64
	if existingSegments > 0 {
		var lastEntry [8]byte
		idxFile.ReadAt(lastEntry[:], int64(existingSegments-1)*8)
		e := decodeBodyIdx(lastEntry[:])
		headFile = e.fileNum
		datPath := filepath.Join(s.outputDir, fmt.Sprintf("bodyc.%04d.cdat", headFile))
		if fi, err := os.Stat(datPath); err == nil {
			headSize = fi.Size()
		}
	}

	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBestCompression))
	if err != nil {
		return err
	}
	defer enc.Close()

	// Open current dat file.
	var datFile *os.File
	var datBuf *bufio.Writer
	openDat := func() error {
		if datFile != nil {
			datBuf.Flush()
			datFile.Close()
			datFile = nil
			datBuf = nil
		}
		path := filepath.Join(s.outputDir, fmt.Sprintf("bodyc.%04d.cdat", headFile))
		f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0644)
		if err != nil {
			return err
		}
		f.Seek(0, io.SeekEnd)
		datFile = f
		datBuf = bufio.NewWriterSize(f, 256*1024)
		return nil
	}
	if err := openDat(); err != nil {
		return err
	}
	defer func() {
		if datBuf != nil {
			datBuf.Flush()
		}
		if datFile != nil {
			datFile.Close()
		}
	}()

	log.Info("Body compact starting",
		"from", startBlock, "to", endBlock-1,
		"blocks", endBlock-startBlock,
		"resumeSegments", existingSegments)

	var totalGethBytes, totalRawBytes, totalCompactBytes int64
	var segCount int
	t0 := time.Now()

	for segStart := startBlock; segStart < endBlock; segStart += HeaderSegmentSize {
		if ctx.Err() != nil {
			break
		}

		count := uint64(HeaderSegmentSize)
		if segStart+count > endBlock {
			count = endBlock - segStart
		}

		// Read and decode bodies. Tail-truncation tolerance — see the
		// matching block in header_compact.go for rationale.
		blocks := make([]*DecodedBlock, 0, count)
		var tailCorrupt bool
		for i := uint64(0); i < count; i++ {
			bodyData, err := s.inputFreezer.Ancient(freezer.TableBodies, segStart+i)
			if err != nil {
				log.Warn("Body compact: trailing read error — capping range",
					"block", segStart+i, "captured", len(blocks), "err", err)
				tailCorrupt = true
				break
			}
			totalGethBytes += int64(len(bodyData))
			if rawLen, err := snappy.DecodedLen(bodyData); err == nil {
				totalRawBytes += int64(rawLen)
			}

			body, err := DecodeGethBody(bodyData)
			if err != nil {
				log.Warn("Body compact: trailing decode error — capping range",
					"block", segStart+i, "captured", len(blocks), "err", err)
				tailCorrupt = true
				break
			}

			db := &DecodedBlock{
				Txs:         body.Transactions,
				Withdrawals: body.Withdrawals,
				UncleRLP:    body.UncleRaw,
			}
			blocks = append(blocks, db)
		}
		if len(blocks) == 0 {
			break
		}

		chainID := firstChainID(blocks)

		compressed := encodeBodySegment(blocks, chainID, enc)

		// File rotation.
		segSize := int64(4 + len(compressed))
		if headSize+segSize > bodyMaxFileSize {
			datBuf.Flush()
			datFile.Close()
			headFile++
			headSize = 0
			if err := openDat(); err != nil {
				return err
			}
			// The rotated-to file may NOT be fresh (orphan bytes from killed
			// sessions): appends land at its END, so the entry offset must
			// reflect the real size — assuming 0 pointed entries into the
			// orphan prefix (observed on bodyc.0341 at 5.4 GB).
			if fi, ferr := os.Stat(filepath.Join(s.outputDir, fmt.Sprintf("bodyc.%04d.cdat", headFile))); ferr == nil {
				headSize = fi.Size()
			}
		}

		// Write idx entry.
		idxEntry := encodeBodyIdx(bodyIdxEntry{fileNum: headFile, offset: uint64(headSize)})
		if _, err := idxBuf.Write(idxEntry); err != nil {
			return err
		}

		// Write framed data.
		var sizeBuf [4]byte
		binary.LittleEndian.PutUint32(sizeBuf[:], uint32(len(compressed)))
		if _, err := datBuf.Write(sizeBuf[:]); err != nil {
			return err
		}
		if _, err := datBuf.Write(compressed); err != nil {
			return err
		}
		headSize += segSize
		totalCompactBytes += segSize
		segCount++

		// Flush idx periodically for crash safety.
		if segCount%10 == 0 {
			idxBuf.Flush()
			datBuf.Flush()
		}

		if tailCorrupt {
			log.Warn("Body compact: stopped at last good block due to trailing corruption",
				"lastBlock", segStart+uint64(len(blocks))-1)
			break
		}

		if segCount%100 == 0 {
			elapsed := time.Since(t0)
			pct := float64(segStart+count-startBlock) / float64(endBlock-startBlock) * 100
			ratioSnappy := float64(totalCompactBytes) / float64(totalGethBytes) * 100
			ratioRaw := float64(0)
			if totalRawBytes > 0 {
				ratioRaw = float64(totalCompactBytes) / float64(totalRawBytes) * 100
			}
			blkPerSec := float64(segStart+count-startBlock) / elapsed.Seconds()
			log.Info("Body compact",
				"block", segStart+count-1,
				"pct", fmt.Sprintf("%.1f%%", pct),
				"vs_snappy", fmt.Sprintf("%.1f%%", ratioSnappy),
				"vs_raw", fmt.Sprintf("%.1f%%", ratioRaw),
				"blk/s", fmt.Sprintf("%.0f", blkPerSec),
				"file", headFile)
		}
	}

	idxBuf.Flush()
	datBuf.Flush()

	if totalGethBytes == 0 {
		log.Info("Body compact: no blocks to process")
		return nil
	}
	ratioSnappy := float64(totalCompactBytes) / float64(totalGethBytes) * 100
	ratioRaw := float64(0)
	if totalRawBytes > 0 {
		ratioRaw = float64(totalCompactBytes) / float64(totalRawBytes) * 100
	}
	log.Info("Body compact complete",
		"blocks", endBlock-startBlock, "segments", segCount,
		"raw_rlp", fmt.Sprintf("%.2f GB", float64(totalRawBytes)/1e9),
		"geth_snappy", fmt.Sprintf("%.2f GB", float64(totalGethBytes)/1e9),
		"compact", fmt.Sprintf("%.2f GB", float64(totalCompactBytes)/1e9),
		"vs_snappy", fmt.Sprintf("%.1f%%", ratioSnappy),
		"vs_raw", fmt.Sprintf("%.1f%%", ratioRaw),
		"files", headFile+1,
		"elapsed", time.Since(t0).Truncate(time.Second))
	return nil
}

// ---------- Reader ----------

// BodyCompactReader provides random and sequential access to bodies
// stored in bodyc.NNNN.cdat + bodyc.cidx. Caches current segment.
// ErrBodyTrimmed is returned by ReadBody when the block's segment cdat is absent
// from the store — e.g. a trimmed post-merge-only body store that keeps just the
// high-numbered cdat segments (bodyc.0097.cdat..) plus the full bodyc.cidx. The
// cdat files need not start at 0000; the reader opens by the cidx's fileNum.
var ErrBodyTrimmed = errors.New("ethel: body segment trimmed (not in this store)")

// ColdResolver resolves a cold (offloaded) body segment on demand. When
// loadSegment finds a segment's cdat absent (trimmed under EIP-4444 retention),
// it asks the resolver to make the covering cold archive locally available and
// return its path; the reader then opens that path instead of failing with
// ErrBodyTrimmed. Resolve is called with the first block of the missing segment.
type ColdResolver interface {
	Resolve(blockNum uint64) (cdatPath string, err error)
}

type BodyCompactReader struct {
	dir          string
	idxFile      *os.File
	dataFiles    map[uint16]*os.File
	dec          *zstd.Decoder
	segments     uint64
	coldResolver ColdResolver

	cachedSeg    int64
	cachedBlocks []*DecodedBlock
}

// SetColdResolver installs a resolver consulted when a segment's cdat is absent
// (cold/trimmed). With no resolver, an absent segment yields ErrBodyTrimmed.
func (r *BodyCompactReader) SetColdResolver(cr ColdResolver) { r.coldResolver = cr }

func OpenBodyCompact(dir string) (*BodyCompactReader, error) {
	idxPath := filepath.Join(dir, "bodyc.cidx")
	idf, err := os.Open(idxPath)
	if err != nil {
		return nil, fmt.Errorf("open index: %w", err)
	}
	fi, err := idf.Stat()
	if err != nil {
		idf.Close()
		return nil, err
	}
	dec, err := zstd.NewReader(nil)
	if err != nil {
		idf.Close()
		return nil, err
	}
	return &BodyCompactReader{
		dir:       dir,
		idxFile:   idf,
		dataFiles: make(map[uint16]*os.File),
		dec:       dec,
		segments:  uint64(fi.Size()) / 8,
		cachedSeg: -1,
	}, nil
}

func (r *BodyCompactReader) Close() {
	r.dec.Close()
	r.idxFile.Close()
	for _, f := range r.dataFiles {
		f.Close()
	}
}

func (r *BodyCompactReader) Segments() uint64 { return r.segments }
func (r *BodyCompactReader) MaxBlock() uint64 { return r.segments * HeaderSegmentSize }

func (r *BodyCompactReader) ReadBody(blockNum uint64) (*DecodedBlock, error) {
	seg := int64(blockNum / HeaderSegmentSize)
	idx := int(blockNum % HeaderSegmentSize)

	if seg != r.cachedSeg {
		if err := r.loadSegment(seg); err != nil {
			return nil, fmt.Errorf("block %d: %w", blockNum, err)
		}
	}
	if idx >= len(r.cachedBlocks) {
		return nil, fmt.Errorf("block %d: index %d out of range (%d)",
			blockNum, idx, len(r.cachedBlocks))
	}
	return r.cachedBlocks[idx], nil
}

// TakeBody is the destructive sequential-read variant used by bulk pipelines.
// Once the returned block has been handed to its worker, the segment cache no
// longer needs to retain a second reference to it. Clearing the slot keeps an
// 8192-block bodyc segment from pinning every already-dispatched transaction,
// calldata and access list until the next segment is decoded. Random-access
// callers must continue to use ReadBody.
func (r *BodyCompactReader) TakeBody(blockNum uint64) (*DecodedBlock, error) {
	body, err := r.ReadBody(blockNum)
	if err != nil {
		return nil, err
	}
	idx := int(blockNum % HeaderSegmentSize)
	r.cachedBlocks[idx] = nil
	return body, nil
}

func (r *BodyCompactReader) loadSegment(segNum int64) error {
	if uint64(segNum) >= r.segments {
		return fmt.Errorf("segment %d out of range (%d)", segNum, r.segments)
	}

	// Read idx entry.
	var entryBuf [8]byte
	if _, err := r.idxFile.ReadAt(entryBuf[:], segNum*8); err != nil {
		return fmt.Errorf("read index: %w", err)
	}
	e := decodeBodyIdx(entryBuf[:])

	// Open/cache dat file. The cdat files need NOT start at bodyc.0000: a trimmed
	// post-merge store carries only the high-numbered segments (e.g. 0097..) plus
	// the full bodyc.cidx, so the reader opens by the cidx's fileNum on demand.
	// A missing cdat means that block's body was trimmed (pre-merge / below the
	// retained range) — surface that as ErrBodyTrimmed, not a raw open error.
	df, ok := r.dataFiles[e.fileNum]
	if !ok {
		path := filepath.Join(r.dir, fmt.Sprintf("bodyc.%04d.cdat", e.fileNum))
		f, err := os.Open(path)
		if err != nil && os.IsNotExist(err) && r.coldResolver != nil {
			// Segment is trimmed locally — ask the cold resolver to fetch the
			// covering cold archive and open it from wherever it landed.
			if cp, rerr := r.coldResolver.Resolve(uint64(segNum) * HeaderSegmentSize); rerr == nil {
				f, err = os.Open(cp)
			}
		}
		if err != nil {
			if os.IsNotExist(err) {
				return fmt.Errorf("%w: segment %d (block ~%d), cdat %04d absent",
					ErrBodyTrimmed, segNum, uint64(segNum)*HeaderSegmentSize, e.fileNum)
			}
			return fmt.Errorf("open dat %d: %w", e.fileNum, err)
		}
		r.dataFiles[e.fileNum] = f
		df = f
	}

	// Read framed data.
	var sizeBuf [4]byte
	if _, err := df.ReadAt(sizeBuf[:], int64(e.offset)); err != nil {
		return fmt.Errorf("read size: %w", err)
	}
	compSize := binary.LittleEndian.Uint32(sizeBuf[:])

	compressed := make([]byte, compSize)
	if _, err := df.ReadAt(compressed, int64(e.offset)+4); err != nil {
		return fmt.Errorf("read data: %w", err)
	}

	raw, err := r.dec.DecodeAll(compressed, nil)
	if err != nil {
		return fmt.Errorf("decompress: %w", err)
	}

	blocks, err := decodeBodySegment(raw)
	if err != nil {
		return fmt.Errorf("decode segment %d: %w", segNum, err)
	}

	r.cachedSeg = segNum
	r.cachedBlocks = blocks
	return nil
}

// ---------- Segment Decoder (stub — full decode in next iteration) ----------

func decodeBodySegment(data []byte) ([]*DecodedBlock, error) {
	if len(data) < 16 {
		return nil, fmt.Errorf("segment too short")
	}
	f := bodyFlags(binary.LittleEndian.Uint16(data[0:2]))
	blockCount := int(binary.LittleEndian.Uint16(data[2:4]))
	txCount := int(binary.LittleEndian.Uint32(data[4:8]))
	chainID := binary.LittleEndian.Uint64(data[8:16])
	pos := 16

	// Block-level metadata.
	txPerBlock := make([]int, blockCount)
	for i := 0; i < blockCount; i++ {
		v, vn := binary.Uvarint(data[pos:])
		if vn <= 0 {
			return nil, fmt.Errorf("bad txCount varint at block %d", i)
		}
		pos += vn
		txPerBlock[i] = int(v)
	}

	wdPerBlock := make([]int, blockCount)
	if f&bfWithdrawals != 0 {
		for i := 0; i < blockCount; i++ {
			v, vn := binary.Uvarint(data[pos:])
			if vn <= 0 {
				return nil, fmt.Errorf("bad wdCount varint at block %d", i)
			}
			pos += vn
			wdPerBlock[i] = int(v)
		}
	}

	// Build result blocks.
	blocks := make([]*DecodedBlock, blockCount)
	for i := range blocks {
		blocks[i] = &DecodedBlock{}
	}

	if txCount == 0 {
		// Decode non-TX columns and return.
		pos, err := decodeNonTxColumns(data, pos, blocks, f, wdPerBlock)
		if err != nil {
			return nil, err
		}
		_ = pos
		return blocks, nil
	}

	// ---- TX columns ----

	// txType.
	if pos+txCount > len(data) {
		return nil, io.ErrUnexpectedEOF
	}
	txTypes := data[pos : pos+txCount]
	pos += txCount

	// R column.
	if pos+txCount*32 > len(data) {
		return nil, io.ErrUnexpectedEOF
	}
	rData := data[pos : pos+txCount*32]
	pos += txCount * 32

	// S column.
	if pos+txCount*32 > len(data) {
		return nil, io.ErrUnexpectedEOF
	}
	sData := data[pos : pos+txCount*32]
	pos += txCount * 32

	// V bitpack + pre-EIP-155 bitpack.
	vBits, newPos, err := decodeBitpack(data, pos, txCount)
	if err != nil {
		return nil, fmt.Errorf("V bits: %w", err)
	}
	pos = newPos

	preEIP155Bits, newPos, err := decodeBitpack(data, pos, txCount)
	if err != nil {
		return nil, fmt.Errorf("preEIP155 bits: %w", err)
	}
	pos = newPos

	// isCreate bitpack.
	creates, newPos, err := decodeBitpack(data, pos, txCount)
	if err != nil {
		return nil, fmt.Errorf("create bits: %w", err)
	}
	pos = newPos

	// To addresses.
	toAddrs, newPos, err := decodeAddrDictColumn(data, pos, txCount, creates)
	if err != nil {
		return nil, fmt.Errorf("To addrs: %w", err)
	}
	pos = newPos

	// Nonce varint column.
	nonces := make([]uint64, txCount)
	for i := 0; i < txCount; i++ {
		v, vn := binary.Uvarint(data[pos:])
		if vn <= 0 {
			return nil, fmt.Errorf("bad nonce varint %d", i)
		}
		pos += vn
		nonces[i] = v
	}

	// Gas varint column.
	gasVals := make([]uint64, txCount)
	for i := 0; i < txCount; i++ {
		v, vn := binary.Uvarint(data[pos:])
		if vn <= 0 {
			return nil, fmt.Errorf("bad gas varint %d", i)
		}
		pos += vn
		gasVals[i] = v
	}

	// Value column.
	values := make([]*uint256.Int, txCount)
	for i := 0; i < txCount; i++ {
		v, newPos, err := decodeTrimmedU256(data, pos)
		if err != nil {
			return nil, fmt.Errorf("value %d: %w", i, err)
		}
		pos = newPos
		values[i] = v
	}

	// GasFeeCap column.
	gasFeeCaps := make([]*uint256.Int, txCount)
	for i := 0; i < txCount; i++ {
		v, newPos, err := decodeTrimmedU256(data, pos)
		if err != nil {
			return nil, fmt.Errorf("gasFeeCap %d: %w", i, err)
		}
		pos = newPos
		gasFeeCaps[i] = v
	}

	// GasTipCap column (type >= 2 only).
	gasTipCaps := make([]*uint256.Int, txCount) // nil for types < 2
	if f&bfHasAccessList != 0 {
		for i := 0; i < txCount; i++ {
			if txTypes[i] >= transaction.DynamicFeeTxType {
				v, newPos, err := decodeTrimmedU256(data, pos)
				if err != nil {
					return nil, fmt.Errorf("gasTipCap %d: %w", i, err)
				}
				pos = newPos
				gasTipCaps[i] = v
			}
		}
	}

	// BlobFeeCap column (type 3 only).
	blobFeeCaps := make([]*uint256.Int, txCount)
	if f&bfHasBlob != 0 {
		for i := 0; i < txCount; i++ {
			if txTypes[i] == transaction.BlobTxType {
				v, newPos, err := decodeTrimmedU256(data, pos)
				if err != nil {
					return nil, fmt.Errorf("blobFeeCap %d: %w", i, err)
				}
				pos = newPos
				blobFeeCaps[i] = v
			}
		}
	}

	// Calldata column.
	calldataLens := make([]int, txCount)
	for i := 0; i < txCount; i++ {
		v, vn := binary.Uvarint(data[pos:])
		if vn <= 0 {
			return nil, fmt.Errorf("bad calldata len %d", i)
		}
		pos += vn
		calldataLens[i] = int(v)
	}
	calldatas := make([][]byte, txCount)
	for i := 0; i < txCount; i++ {
		l := calldataLens[i]
		if pos+l > len(data) {
			return nil, fmt.Errorf("calldata %d: truncated", i)
		}
		if l > 0 {
			calldatas[i] = make([]byte, l)
			copy(calldatas[i], data[pos:pos+l])
		}
		pos += l
	}

	// AccessList column.
	accessLists := make([]transaction.AccessList, txCount)
	if f&bfHasAccessList != 0 {
		for i := 0; i < txCount; i++ {
			if txTypes[i] >= transaction.AccessListTxType {
				tupleCount, vn := binary.Uvarint(data[pos:])
				if vn <= 0 {
					return nil, fmt.Errorf("bad accessList count %d", i)
				}
				pos += vn
				al := make(transaction.AccessList, tupleCount)
				for j := uint64(0); j < tupleCount; j++ {
					if pos+20 > len(data) {
						return nil, io.ErrUnexpectedEOF
					}
					var addr types.Address
					copy(addr[:], data[pos:pos+20])
					pos += 20
					keyCount, vn := binary.Uvarint(data[pos:])
					if vn <= 0 {
						return nil, fmt.Errorf("bad key count")
					}
					pos += vn
					keys := make([]types.Hash, keyCount)
					for k := uint64(0); k < keyCount; k++ {
						if pos+32 > len(data) {
							return nil, io.ErrUnexpectedEOF
						}
						copy(keys[k][:], data[pos:pos+32])
						pos += 32
					}
					al[j] = transaction.AccessTuple{Address: addr, StorageKeys: keys}
				}
				accessLists[i] = al
			}
		}
	}

	// BlobHashes (type 3).
	blobHashes := make([][]types.Hash, txCount)
	if f&bfHasBlob != 0 {
		for i := 0; i < txCount; i++ {
			if txTypes[i] == transaction.BlobTxType {
				hc, vn := binary.Uvarint(data[pos:])
				if vn <= 0 {
					return nil, fmt.Errorf("bad blobHash count")
				}
				pos += vn
				hashes := make([]types.Hash, hc)
				for j := uint64(0); j < hc; j++ {
					if pos+32 > len(data) {
						return nil, io.ErrUnexpectedEOF
					}
					copy(hashes[j][:], data[pos:pos+32])
					pos += 32
				}
				blobHashes[i] = hashes
			}
		}
	}

	// AuthList (type 4).
	authLists := make([]transaction.AuthorizationList, txCount)
	if f&bfHasSetCode != 0 {
		for i := 0; i < txCount; i++ {
			if txTypes[i] == transaction.SetCodeTxType {
				authCount, vn := binary.Uvarint(data[pos:])
				if vn <= 0 {
					return nil, fmt.Errorf("bad authList count")
				}
				pos += vn
				al := make(transaction.AuthorizationList, authCount)
				for j := uint64(0); j < authCount; j++ {
					auth := &transaction.Authorization{}
					// ChainID
					chainVal, newPos, err := decodeTrimmedU256(data, pos)
					if err != nil {
						return nil, fmt.Errorf("auth chainID: %w", err)
					}
					pos = newPos
					if chainVal != nil {
						auth.ChainID = *chainVal
					}
					// Address
					if pos+20 > len(data) {
						return nil, io.ErrUnexpectedEOF
					}
					copy(auth.Address[:], data[pos:pos+20])
					pos += 20
					// Nonce
					auth.Nonce, vn = binary.Uvarint(data[pos:])
					if vn <= 0 {
						return nil, fmt.Errorf("bad auth nonce")
					}
					pos += vn
					// R (32B), S (32B), V (1B)
					if pos+65 > len(data) {
						return nil, io.ErrUnexpectedEOF
					}
					var rBuf, sBuf [32]byte
					copy(rBuf[:], data[pos:pos+32])
					pos += 32
					copy(sBuf[:], data[pos:pos+32])
					pos += 32
					auth.R = new(uint256.Int).SetBytes32(rBuf[:])
					auth.S = new(uint256.Int).SetBytes32(sBuf[:])
					if f&bfAuthVFull != 0 {
						vVal, np, verr := decodeTrimmedU256(data, pos)
						if verr != nil {
							return nil, fmt.Errorf("auth V: %w", verr)
						}
						auth.V, pos = vVal, np
					} else {
						// Legacy segment: V was folded to a single 0/1 byte, so
						// this low-level decoder cannot recover 27/28 by itself.
						// Higher-level readers may disambiguate with the canonical
						// header transaction root; otherwise regenerate the segment.
						auth.V = uint256.NewInt(uint64(data[pos]))
						pos++
					}
					al[j] = auth
				}
				authLists[i] = al
			}
		}
	}

	// Non-TX columns.
	pos, err = decodeNonTxColumns(data, pos, blocks, f, wdPerBlock)
	if err != nil {
		return nil, err
	}

	// Assemble transactions from decoded columns.
	chainIDU256 := uint256.NewInt(chainID)
	tipIdx := 0  // index into gasTipCaps (only type>=2 txs)
	blobIdx := 0 // index into blobFeeCaps/blobHashes (only type=3 txs)

	txIdx := 0
	for i, b := range blocks {
		b.Txs = make([]*transaction.Transaction, txPerBlock[i])
		for j := range b.Txs {
			ti := txIdx
			txIdx++

			// Common fields.
			var to *types.Address
			if !creates[ti] {
				a := toAddrs[ti]
				to = &a
			}

			// Reconstruct R, S as uint256.
			var rBuf, sBuf [32]byte
			copy(rBuf[:], rData[ti*32:(ti+1)*32])
			copy(sBuf[:], sData[ti*32:(ti+1)*32])
			rVal := new(uint256.Int).SetBytes32(rBuf[:])
			sVal := new(uint256.Int).SetBytes32(sBuf[:])

			// Reconstruct V from parity bit + pre-EIP-155 flag.
			var vVal *uint256.Int
			switch txTypes[ti] {
			case transaction.LegacyTxType:
				if preEIP155Bits[ti] {
					// Pre-EIP-155: V = 27 + parity
					if vBits[ti] {
						vVal = uint256.NewInt(28)
					} else {
						vVal = uint256.NewInt(27)
					}
				} else {
					// EIP-155: V = chainID*2 + 35 + parity
					v155 := chainID*2 + 35
					if vBits[ti] {
						v155++
					}
					vVal = uint256.NewInt(v155)
				}
			default: // typed tx: V = 0 or 1
				if vBits[ti] {
					vVal = uint256.NewInt(1)
				} else {
					vVal = uint256.NewInt(0)
				}
			}

			var inner transaction.TxData
			switch txTypes[ti] {
			case transaction.LegacyTxType:
				inner = &transaction.LegacyTx{
					Nonce:    nonces[ti],
					GasPrice: gasFeeCaps[ti],
					Gas:      gasVals[ti],
					To:       to,
					Value:    values[ti],
					Data:     calldatas[ti],
					V:        vVal,
					R:        rVal,
					S:        sVal,
				}
			case transaction.AccessListTxType:
				inner = &transaction.AccessListTx{
					ChainID:    chainIDU256.Clone(),
					Nonce:      nonces[ti],
					GasPrice:   gasFeeCaps[ti],
					Gas:        gasVals[ti],
					To:         to,
					Value:      values[ti],
					Data:       calldatas[ti],
					AccessList: accessLists[ti],
					V:          vVal,
					R:          rVal,
					S:          sVal,
				}
			case transaction.DynamicFeeTxType:
				inner = &transaction.DynamicFeeTx{
					ChainID:    chainIDU256.Clone(),
					Nonce:      nonces[ti],
					GasTipCap:  gasTipCaps[ti],
					GasFeeCap:  gasFeeCaps[ti],
					Gas:        gasVals[ti],
					To:         to,
					Value:      values[ti],
					Data:       calldatas[ti],
					AccessList: accessLists[ti],
					V:          vVal,
					R:          rVal,
					S:          sVal,
				}
				tipIdx++
			case transaction.BlobTxType:
				blobTo := types.Address{}
				if to != nil {
					blobTo = *to
				}
				inner = &transaction.BlobTx{
					ChainID:    chainIDU256.Clone(),
					Nonce:      nonces[ti],
					GasTipCap:  gasTipCaps[ti],
					GasFeeCap:  gasFeeCaps[ti],
					Gas:        gasVals[ti],
					To:         blobTo,
					Value:      values[ti],
					Data:       calldatas[ti],
					AccessList: accessLists[ti],
					BlobFeeCap: blobFeeCaps[ti],
					BlobHashes: blobHashes[ti],
					V:          vVal,
					R:          rVal,
					S:          sVal,
				}
				tipIdx++
				blobIdx++
			case transaction.SetCodeTxType:
				inner = &transaction.SetCodeTx{
					ChainID:    chainIDU256.Clone(),
					Nonce:      nonces[ti],
					GasTipCap:  gasTipCaps[ti],
					GasFeeCap:  gasFeeCaps[ti],
					Gas:        gasVals[ti],
					To:         to,
					Value:      values[ti],
					Data:       calldatas[ti],
					AccessList: accessLists[ti],
					AuthList:   authLists[ti],
					V:          vVal,
					R:          rVal,
					S:          sVal,
				}
				tipIdx++
			default:
				return nil, fmt.Errorf("unknown tx type %d at index %d", txTypes[ti], ti)
			}
			// NewTxOwned: the columnar decoder above just built `inner`
			// and owns the only reference, so NewTx's defensive copy
			// would be pure waste.
			b.Txs[j] = transaction.NewTxOwned(inner)
		}
	}

	return blocks, nil
}

func decodeNonTxColumns(data []byte, pos int, blocks []*DecodedBlock, f bodyFlags, wdPerBlock []int) (int, error) {
	// Uncles.
	if f&bfPostMerge == 0 {
		for _, b := range blocks {
			uncleCount, vn := binary.Uvarint(data[pos:])
			if vn <= 0 {
				return pos, fmt.Errorf("bad uncle count")
			}
			pos += vn
			b.UncleRLP = make([][]byte, uncleCount)
			for j := uint64(0); j < uncleCount; j++ {
				uLen, vn := binary.Uvarint(data[pos:])
				if vn <= 0 {
					return pos, fmt.Errorf("bad uncle len")
				}
				pos += vn
				if pos+int(uLen) > len(data) {
					return pos, io.ErrUnexpectedEOF
				}
				b.UncleRLP[j] = make([]byte, uLen)
				copy(b.UncleRLP[j], data[pos:pos+int(uLen)])
				pos += int(uLen)
			}
		}
	}

	// Withdrawals.
	if f&bfWithdrawals != 0 {
		for i, b := range blocks {
			wdCount := wdPerBlock[i]
			if wdCount > 0 {
				b.Withdrawals = make([]*Withdrawal, wdCount)
				for j := 0; j < wdCount; j++ {
					w := &Withdrawal{}
					var vn int
					w.Index, vn = binary.Uvarint(data[pos:])
					if vn <= 0 {
						return pos, fmt.Errorf("bad wd index")
					}
					pos += vn
					w.Validator, vn = binary.Uvarint(data[pos:])
					if vn <= 0 {
						return pos, fmt.Errorf("bad wd validator")
					}
					pos += vn
					if pos+20 > len(data) {
						return pos, io.ErrUnexpectedEOF
					}
					copy(w.Address[:], data[pos:pos+20])
					pos += 20
					w.Amount, vn = binary.Uvarint(data[pos:])
					if vn <= 0 {
						return pos, fmt.Errorf("bad wd amount")
					}
					pos += vn
					b.Withdrawals[j] = w
				}
			}
		}
	}
	return pos, nil
}
