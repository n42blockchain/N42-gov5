// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// sender_reader.go reads pre-computed senders from SegmentStore
// (chain/senders.cidx + senders.NNNN.cdat) in batch-64 format.

package ethel

import (
	"encoding/binary"

	"github.com/klauspost/compress/zstd"

	"github.com/n42blockchain/N42/internal/cscompact"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

// SenderSegmentReader reads senders from SegmentStore batch-64 format.
type SenderSegmentReader struct {
	store     *cscompact.SegmentStoreReader
	dec       *zstd.Decoder
	cachedBatch uint64
	cachedData  [][]byte // decoded entries for current batch
}

// OpenSenderStore opens a senders SegmentStore for reading.
func OpenSenderStore(chainDir string) (*SenderSegmentReader, error) {
	store, err := cscompact.OpenSegmentStore(chainDir, "senders")
	if err != nil {
		return nil, err
	}
	if store.SegmentCount() == 0 {
		store.Close()
		return nil, nil
	}
	dec, _ := zstd.NewReader(nil)
	return &SenderSegmentReader{
		store:       store,
		dec:         dec,
		cachedBatch: ^uint64(0), // invalid, forces first load
	}, nil
}

// MaxBlock returns the highest block number covered by this store.
func (r *SenderSegmentReader) MaxBlock() uint64 {
	return r.store.SegmentCount() * uint64(freezer.BatchSize)
}

// ReadBlock returns the concatenated sender addresses (20B each) for a block.
func (r *SenderSegmentReader) ReadBlock(blockNum uint64) ([]byte, error) {
	batchNum := blockNum / uint64(freezer.BatchSize)
	if batchNum >= r.store.SegmentCount() {
		return nil, nil
	}

	// Cache: if same batch, reuse decoded data.
	if batchNum != r.cachedBatch {
		if err := r.loadBatch(batchNum); err != nil {
			return nil, err
		}
	}

	idx := blockNum % uint64(freezer.BatchSize)
	if int(idx) >= len(r.cachedData) {
		return nil, nil
	}
	return r.cachedData[idx], nil
}

func (r *SenderSegmentReader) loadBatch(batchNum uint64) error {
	raw, err := r.store.ReadSegmentData(batchNum)
	if err != nil {
		return err
	}

	// Try zstd decompress.
	decompressed, err := r.dec.DecodeAll(raw, nil)
	if err != nil {
		decompressed = raw // not compressed
	}

	// Parse EncodeBatch format: [4B len][data][4B len][data]...
	r.cachedData = r.cachedData[:0]
	pos := 0
	for pos+4 <= len(decompressed) {
		entryLen := int(binary.LittleEndian.Uint32(decompressed[pos : pos+4]))
		pos += 4
		if pos+entryLen > len(decompressed) {
			break
		}
		if entryLen == 0 {
			r.cachedData = append(r.cachedData, nil)
		} else {
			r.cachedData = append(r.cachedData, decompressed[pos:pos+entryLen])
		}
		pos += entryLen
	}

	r.cachedBatch = batchNum
	return nil
}

func (r *SenderSegmentReader) Close() {
	if r.store != nil {
		r.store.Close()
	}
	if r.dec != nil {
		r.dec.Close()
	}
}
