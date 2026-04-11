// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Exporter writes frozen chain data from the KV store to EraE segment
// files under eraDir. NewExporter configures a default segmentSize of
// 8192 blocks and ExportRange walks the rawdb archive producing the
// EraE files consumed by torrent-based distribution.

package torrentsync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/rawdb/era"
)

// Exporter exports frozen chain data to EraE segment files.
type Exporter struct {
	db          kv.RwDB
	eraDir      string
	segmentSize uint64
}

// NewExporter creates an Exporter that writes EraE segments into eraDir.
func NewExporter(db kv.RwDB, eraDir string, segmentSize uint64) *Exporter {
	if segmentSize == 0 {
		segmentSize = 8192
	}
	return &Exporter{
		db:          db,
		eraDir:      eraDir,
		segmentSize: segmentSize,
	}
}

// ExportRange exports blocks [from, to] as one or more EraE segments.
// Blocks are read from the database; each segment covers up to segmentSize blocks.
// Returns a list of SegmentInfo describing the written files.
func (e *Exporter) ExportRange(ctx context.Context, from, to uint64) ([]SegmentInfo, error) {
	if to < from {
		return nil, fmt.Errorf("torrentsync: invalid range [%d, %d]", from, to)
	}
	if err := os.MkdirAll(e.eraDir, 0755); err != nil {
		return nil, fmt.Errorf("torrentsync: mkdir: %w", err)
	}

	var segments []SegmentInfo

	for segStart := from; segStart <= to; segStart += e.segmentSize {
		select {
		case <-ctx.Done():
			return segments, ctx.Err()
		default:
		}

		segEnd := segStart + e.segmentSize - 1
		if segEnd > to {
			segEnd = to
		}

		seg, err := e.exportSegment(ctx, segStart, segEnd)
		if err != nil {
			return segments, fmt.Errorf("torrentsync: export segment [%d, %d]: %w", segStart, segEnd, err)
		}
		segments = append(segments, *seg)

		log.Info("Exported EraE segment",
			"from", segStart, "to", segEnd,
			"file", seg.FileName, "size", seg.Size)
	}

	return segments, nil
}

// exportSegment writes a single EraE file covering blocks [from, to].
func (e *Exporter) exportSegment(ctx context.Context, from, to uint64) (*SegmentInfo, error) {
	fileName := EraFileName(from, to)
	filePath := filepath.Join(e.eraDir, fileName)

	// Use networkID 1 as default; in production this would come from chain config.
	w, err := era.NewWriter(filePath, 1)
	if err != nil {
		return nil, fmt.Errorf("create era writer: %w", err)
	}

	var written uint64

	err = e.db.View(ctx, func(tx kv.Tx) error {
		for num := from; num <= to; num++ {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}

			blk, err := rawdb.ReadBlockByNumber(tx, num)
			if err != nil {
				return fmt.Errorf("read block %d: %w", num, err)
			}
			if blk == nil {
				return fmt.Errorf("block %d not found", num)
			}

			blockData, err := blk.Marshal()
			if err != nil {
				return fmt.Errorf("marshal block %d: %w", num, err)
			}

			// We store empty receipts data — the EraE format requires it.
			if err := w.Append(num, blockData, nil); err != nil {
				return fmt.Errorf("append block %d: %w", num, err)
			}
			written++
		}
		return nil
	})
	if err != nil {
		w.Close()
		os.Remove(filePath)
		return nil, err
	}

	if err := w.Finalize(); err != nil {
		w.Close() // Finalize may fail before closing the file handle.
		os.Remove(filePath)
		return nil, fmt.Errorf("finalize era: %w", err)
	}

	// Compute SHA256 of the written file.
	fileHash, fileSize, err := hashFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("hash file: %w", err)
	}

	return &SegmentInfo{
		FromBlock:  from,
		ToBlock:    to,
		FileName:   fileName,
		Size:       fileSize,
		BlockCount: written,
		SHA256:     fileHash,
	}, nil
}

// hashFile computes the SHA256 hash and size of a file using streaming
// to avoid loading the entire file into memory.
func hashFile(path string) (hexHash string, size int64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()

	h := sha256.New()
	size, err = io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), size, nil
}
