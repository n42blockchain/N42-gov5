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

func (w *Warm) Close() error      { return w.fr.Close() }
func (w *Warm) Meta() Meta        { return w.meta }
func (w *Warm) BaseBlock() uint64 { return w.meta.BaseBlock }
func (w *Warm) HeadBlock() uint64 { return w.meta.HeadBlock }

// Available reports whether blk is in the retained window.
// Implements the Source interface.
func (w *Warm) Available(blk uint64) bool {
	return blk >= w.meta.BaseBlock && blk <= w.meta.HeadBlock
}

// WindowDescription implements the Source interface.
func (w *Warm) WindowDescription() string {
	return fmt.Sprintf("warm[%d, %d] (%d blocks)", w.meta.BaseBlock, w.meta.HeadBlock, w.meta.KeepBlocks)
}

// RetrieveAccount implements the Source interface.
func (w *Warm) RetrieveAccount(blk uint64) ([]byte, error) {
	return w.retrieve(freezer.TableAccountChanges, blk)
}

// RetrieveStorage implements the Source interface.
func (w *Warm) RetrieveStorage(blk uint64) ([]byte, error) {
	return w.retrieve(freezer.TableStorageChanges, blk)
}

// retrieve is shared by the typed methods and the verifier's
// table-name-flagged loop. Returns ErrDeepReorg for blocks outside
// [BaseBlock, HeadBlock] — that's what callers in the Source path
// expect; the dedicated verifier handles it the same way.
func (w *Warm) retrieve(tableName string, blk uint64) ([]byte, error) {
	if !w.Available(blk) {
		return nil, ErrDeepReorg
	}
	tbl, err := w.fr.EnsureTableCompressed(tableName, "c")
	if err != nil {
		return nil, fmt.Errorf("warm-cs: ensure table %s: %w", tableName, err)
	}
	data, err := tbl.Retrieve(blk - w.meta.BaseBlock)
	if err != nil {
		return nil, fmt.Errorf("warm-cs: retrieve blk %d: %w", blk, err)
	}
	return data, nil
}

// Retrieve is the table-name-flagged form used by the verifier tool.
// Internal callers should prefer RetrieveAccount / RetrieveStorage.
func (w *Warm) Retrieve(tableName string, blk uint64) ([]byte, error) {
	return w.retrieve(tableName, blk)
}
