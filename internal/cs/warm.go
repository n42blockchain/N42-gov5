// Package cs provides utilities for the CS warm tier — a small,
// recent slice of the acctcs/storcs freezer that supports unwind
// queries for the last N blocks. Older changesets are redundant
// once history (cmd/n42-history-build) is built and verified.
//
// Layout produced by cmd/n42-cs-prune:
//
//	<dst-dir>/
//	├── acctcs.cidx + acctcs.NNNN.cdat   ← fresh freezer, items 0..N
//	├── storcs.cidx + storcs.NNNN.cdat
//	└── meta.json                         ← {"base_block": H₀-N, "head_block": H₀, ...}
//
// To read: open as a normal freezer, but translate absolute block →
// item via `(block - base_block)`. The Warm type wraps this.
package cs

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

// Meta is the sidecar metadata describing what slice of the full
// chain a warm freezer holds.
type Meta struct {
	BaseBlock  uint64    `json:"base_block"`  // item 0 in warm freezer = this block
	HeadBlock  uint64    `json:"head_block"`  // last block included (inclusive)
	KeepBlocks uint64    `json:"keep_blocks"` // = HeadBlock - BaseBlock + 1
	CreatedAt  time.Time `json:"created_at"`
	// SrcFreezerPath is the original full freezer this was pruned from;
	// recorded so an audit can re-derive the same range if needed.
	SrcFreezerPath string `json:"src_freezer_path"`
}

const metaFileName = "meta.json"

// WriteMeta persists Meta to <dir>/meta.json.
func WriteMeta(dir string, m Meta) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	f, err := os.Create(filepath.Join(dir, metaFileName))
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(m)
}

// ReadMeta loads Meta from <dir>/meta.json.
func ReadMeta(dir string) (Meta, error) {
	var m Meta
	data, err := os.ReadFile(filepath.Join(dir, metaFileName))
	if err != nil {
		return m, err
	}
	return m, json.Unmarshal(data, &m)
}

// Warm wraps a freezer.Freezer and translates absolute block numbers
// to item indices via base_block from meta.json. Construct via Open.
type Warm struct {
	fr   *freezer.Freezer
	meta Meta
}

// Open opens a warm freezer (read-only) and loads its sidecar meta.
func Open(dir string) (*Warm, error) {
	meta, err := ReadMeta(dir)
	if err != nil {
		return nil, fmt.Errorf("warm-cs: read meta: %w", err)
	}
	fr, err := freezer.NewReadOnly(dir)
	if err != nil {
		return nil, fmt.Errorf("warm-cs: open freezer: %w", err)
	}
	return &Warm{fr: fr, meta: meta}, nil
}

func (w *Warm) Close() error          { return w.fr.Close() }
func (w *Warm) Meta() Meta            { return w.meta }
func (w *Warm) BaseBlock() uint64     { return w.meta.BaseBlock }
func (w *Warm) HeadBlock() uint64     { return w.meta.HeadBlock }
func (w *Warm) Contains(blk uint64) bool {
	return blk >= w.meta.BaseBlock && blk <= w.meta.HeadBlock
}

// Retrieve returns the CS blob for the given absolute block number.
// Returns ErrOutOfWindow if blk is outside [BaseBlock, HeadBlock].
func (w *Warm) Retrieve(tableName string, blk uint64) ([]byte, error) {
	if !w.Contains(blk) {
		return nil, ErrOutOfWindow
	}
	tbl, err := w.fr.EnsureTableCompressed(tableName, "c")
	if err != nil {
		return nil, fmt.Errorf("warm-cs: ensure table %s: %w", tableName, err)
	}
	item := blk - w.meta.BaseBlock
	data, err := tbl.Retrieve(item)
	if err != nil {
		return nil, fmt.Errorf("warm-cs: retrieve item %d (blk %d): %w", item, blk, err)
	}
	return data, nil
}

// ErrOutOfWindow signals a block query falls outside the retained
// warm-tier window. Callers should fall back to history (built by
// cmd/n42-history-build) for older blocks.
var ErrOutOfWindow = fmt.Errorf("warm-cs: block outside retention window")
