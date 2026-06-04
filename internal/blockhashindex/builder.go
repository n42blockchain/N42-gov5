// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// builder.go builds blockHash → blockNumber RecSplit segments from geth ancient
// headers. Each block contributes one key: AddKey(header.Hash(), relBlock).
// There is no dat — RecSplit (Enums off) returns the stored relBlock directly.

package blockhashindex

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/n42blockchain/N42/internal/cscompact"
	"github.com/n42blockchain/N42/internal/ethel"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/recsplit"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

// Builder builds blockhash segments from a geth ancient header source.
type Builder struct {
	in                 *freezer.Freezer
	outDir             string
	lessFalsePositives bool
}

// NewBuilder returns a builder. lessFalsePositives=false yields the smallest
// index (~3.25 B/key) but REQUIRES the reader to install a header-hash verifier;
// =true keeps an 8-bit existence fp (correct without a verifier, ~4.25 B/key).
func NewBuilder(in *freezer.Freezer, outDir string, lessFalsePositives bool) *Builder {
	return &Builder{in: in, outDir: outDir, lessFalsePositives: lessFalsePositives}
}

// BuildRange builds ONE immutable cold segment covering [startBlock, endBlock)
// into outDir (cscompact SegmentStore "blockhash"). Per the design rule the cold
// tier is a single segment built once — new blocks are served by the hot MDBX
// tier, not by appending more segments — so there is no per-segment fanout and
// no LessFalsePositives fingerprint is needed (the reader self-verifies by
// recomputing the header hash). A windowed build narrows the range; the reader
// recovers blockNum = base + relBlock from blockhash.base.
func (b *Builder) BuildRange(ctx context.Context, startBlock, endBlock uint64) error {
	if endBlock > b.in.Frozen() {
		endBlock = b.in.Frozen()
	}
	if endBlock <= startBlock {
		return fmt.Errorf("empty range %d..%d", startBlock, endBlock)
	}
	store, err := cscompact.NewSegmentStoreWriter(b.outDir, "blockhash")
	if err != nil {
		return err
	}
	defer store.Close()
	if store.SegmentCount() > 0 {
		log.Info("blockhashindex: cold segment already present — nothing to do", "segments", store.SegmentCount())
		return nil
	}

	tmpIdx := filepath.Join(b.outDir, "tmp_blockhash.ri")
	if err := b.buildOne(ctx, startBlock, endBlock, tmpIdx); err != nil {
		os.Remove(tmpIdx)
		return fmt.Errorf("build cold segment %d-%d: %w", startBlock, endBlock, err)
	}
	// No dat: blockHash→relBlock is 1:1, RecSplit returns relBlock directly.
	if _, err := store.WriteSegment(nil, tmpIdx); err != nil {
		return err
	}
	return nil
}

func (b *Builder) buildOne(ctx context.Context, startBlock, endBlock uint64, idxPath string) error {
	t0 := time.Now()
	blockCount := endBlock - startBlock
	rs, err := recsplit.NewRecSplit(recsplit.RecSplitArgs{
		KeyCount:           int(blockCount),
		BucketSize:         2000,
		LeafSize:           8,
		Enums:              false, // Lookup returns the stored relBlock directly
		LessFalsePositives: b.lessFalsePositives,
		IndexFile:          idxPath,
		BaseDataID:         startBlock,
		TmpDir:             os.TempDir(),
	}, log2.New())
	if err != nil {
		return err
	}
	for blockNum := startBlock; blockNum < endBlock; blockNum++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		data, err := b.in.Ancient(freezer.TableHeaders, blockNum)
		if err != nil {
			return fmt.Errorf("read header %d: %w", blockNum, err)
		}
		hdr, err := ethel.DecodeGethHeader(data)
		if err != nil {
			return fmt.Errorf("decode header %d: %w", blockNum, err)
		}
		h := hdr.Hash()
		if err := rs.AddKey(h[:], blockNum-startBlock); err != nil {
			return fmt.Errorf("addkey %d: %w", blockNum, err)
		}
	}
	if err := rs.Build(ctx); err != nil {
		return fmt.Errorf("build: %w", err)
	}
	log.Info("blockhashindex: segment built",
		"blocks", fmt.Sprintf("%d-%d", startBlock, endBlock-1),
		"keys", blockCount,
		"elapsed", time.Since(t0).Truncate(time.Second))
	return nil
}
