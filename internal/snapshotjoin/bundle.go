// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

// Package snapshotjoin writes and reads the profile-1 offline snapshot
// bundle (gate 4a, agreed with n42-rs): the chain's state at a committed
// pivot P as reth init-state JSONL plus the portable QMDB snapshot, anchored
// by a commit QC. Both clients label the state "P" and check it against
// header(P+1).Root (deferred execution, k = 1, SYNC §5.3.1), and verify the
// QC before trusting that root.
package snapshotjoin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Bundle file names, all in one directory.
const (
	// FileState is reth init-state JSONL: {"root": R} with R the pivot's
	// post-state root (= header(P+1).Root), then one account per line in
	// ascending address order.
	FileState = "state.jsonl"
	// FileHeader is header(P), RLP: the pivot's own header (its Root is the
	// state after P-1).
	FileHeader = "state.jsonl.header.rlp"
	// FileQMDB is the portable QMDB snapshot, tombstone layout (N42QMDB\x03),
	// labelled (P, hash(P)) with root R.
	FileQMDB = "qmdb.bin"
	// FileNextHeader is header(P+1), RLP: its Root is R.
	FileNextHeader = "next-header.rlp"
	// FileAnchor is the commit_qc/1 payload: a commit QC on a canonical
	// block A >= P+1 and the headers A down to max(P-255, 1).
	FileAnchor = "commit-qc.bin"
	// FileBlock is block P (header and body), gov5 RLP. gov5's importer
	// stores it as the head block; n42-rs may ignore it.
	FileBlock = "block.rlp"
	// FileManifest describes the bundle (Manifest).
	FileManifest = "bundle.json"
)

// ManifestFormat names the bundle layout.
const ManifestFormat = "n42-profile1-snapshot-bundle/1"

// Manifest is bundle.json. It is descriptive: an importer authenticates the
// bundle from the anchor, not from the manifest, and checks the file digests
// only to catch a damaged copy early.
type Manifest struct {
	Format        string            `json:"format"`
	ChainID       uint64            `json:"chain_id"`
	GenesisHash   string            `json:"genesis_hash"`
	PivotNumber   uint64            `json:"pivot_number"`
	PivotHash     string            `json:"pivot_hash"`
	PostStateRoot string            `json:"post_state_root"`
	DeferredDepth uint64            `json:"deferred_depth"`
	RootLabel     string            `json:"root_label"`
	AnchorQCView  uint64            `json:"anchor_qc_view"`
	AnchorNumber  uint64            `json:"anchor_block_number"`
	AnchorHash    string            `json:"anchor_block_hash"`
	AnchorDomain  string            `json:"anchor_domain"`
	AnchorLow     uint64            `json:"anchor_lowest_header"`
	Accounts      uint64            `json:"accounts"`
	StorageSlots  uint64            `json:"storage_slots"`
	LiveLeaves    uint64            `json:"qmdb_live_leaves"`
	NextSlot      uint64            `json:"qmdb_next_slot"`
	Files         map[string]string `json:"files_sha256"`
}

// rootLabelText is the manifest's statement of the k = 1 label rule.
const rootLabelText = "post_state_root is the state after executing pivot_number; it equals header(pivot_number+1).Root (deferred depth 1); header(pivot_number).Root is the state after pivot_number-1"

// anchorDomainText names the QC's signing domain.
const anchorDomainText = "H2-v4 commit (phase 3, changesHash 0), genesis validator set, >= n-f signers"

// Bundle is a bundle directory read into memory (except the JSONL, which an
// importer streams).
type Bundle struct {
	Dir        string
	Manifest   *Manifest
	Header     []byte
	NextHeader []byte
	QMDB       []byte
	Anchor     []byte
	Block      []byte
}

// ReadBundle reads dir and checks every file against the manifest digests.
func ReadBundle(dir string) (*Bundle, error) {
	b := &Bundle{Dir: dir}
	raw, err := os.ReadFile(filepath.Join(dir, FileManifest))
	if err != nil {
		return nil, fmt.Errorf("bundle manifest: %w", err)
	}
	m := new(Manifest)
	if err := json.Unmarshal(raw, m); err != nil {
		return nil, fmt.Errorf("bundle manifest: %w", err)
	}
	if m.Format != ManifestFormat {
		return nil, fmt.Errorf("bundle manifest: format %q, this importer reads %q", m.Format, ManifestFormat)
	}
	b.Manifest = m
	for _, f := range []struct {
		name string
		dst  *[]byte
	}{{FileHeader, &b.Header}, {FileNextHeader, &b.NextHeader}, {FileQMDB, &b.QMDB}, {FileAnchor, &b.Anchor}, {FileBlock, &b.Block}} {
		data, err := os.ReadFile(filepath.Join(dir, f.name))
		if err != nil {
			return nil, fmt.Errorf("bundle file: %w", err)
		}
		if err := checkDigest(m, f.name, data); err != nil {
			return nil, err
		}
		*f.dst = data
	}
	return b, nil
}

func checkDigest(m *Manifest, name string, data []byte) error {
	want, ok := m.Files[name]
	if !ok {
		return fmt.Errorf("bundle manifest lists no digest for %s", name)
	}
	got := sha256.Sum256(data)
	if hex.EncodeToString(got[:]) != want {
		return fmt.Errorf("bundle file %s: sha256 %x, manifest %s (damaged copy)", name, got, want)
	}
	return nil
}

// fileDigest returns the hex sha256 of a file.
func fileDigest(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// errBundleExists: the export directory already holds a bundle file.
var errBundleExists = errors.New("the export directory already holds bundle files; use an empty directory")
