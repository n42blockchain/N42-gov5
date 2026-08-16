// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.

package ancientera

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"lukechampine.com/blake3"
)

// ManifestName is the fixed manifest file name inside an era directory.
const ManifestName = "MANIFEST.json"

// EntryStatus is the manifest ledger state of one era file.
type EntryStatus string

const (
	StatusSealed EntryStatus = "sealed"
	StatusPruned EntryStatus = "pruned"
)

// ManifestEntry describes one era file.
type ManifestEntry struct {
	Class         string      `json:"class"`
	Era           uint64      `json:"era"`
	File          string      `json:"file"`
	Size          int64       `json:"size"`
	PayloadBlake3 string      `json:"payload_blake3"`
	FirstHash     string      `json:"first_hash"`
	LastHash      string      `json:"last_hash"`
	Status        EntryStatus `json:"status"`
	PrunedAt      string      `json:"pruned_at,omitempty"`
}

// Manifest is the per-store ledger: cache + prune record. Era footers are
// the root of trust; a missing or damaged manifest is rebuilt from them.
type Manifest struct {
	Version     int             `json:"version"`
	Span        uint64          `json:"span"`
	ChainID     uint64          `json:"chain_id"`
	GenesisHash string          `json:"genesis_hash"`
	Generation  string          `json:"generation,omitempty"`
	Entries     []ManifestEntry `json:"entries"`
}

// Lookup returns the entry for (class, era), or nil.
func (m *Manifest) Lookup(class Class, era uint64) *ManifestEntry {
	for i := range m.Entries {
		if m.Entries[i].Class == class.String() && m.Entries[i].Era == era {
			return &m.Entries[i]
		}
	}
	return nil
}

func (m *Manifest) sortEntries() {
	sort.Slice(m.Entries, func(i, j int) bool {
		if m.Entries[i].Era != m.Entries[j].Era {
			return m.Entries[i].Era < m.Entries[j].Era
		}
		return m.Entries[i].Class < m.Entries[j].Class
	})
}

// errManifestParse wraps JSON/shape failures so OpenStore can fall back
// to a footer rebuild (footers are the root of trust).
var errManifestParse = fmt.Errorf("era manifest parse")

func isManifestParseErr(err error) bool { return errors.Is(err, errManifestParse) }

// LoadManifest reads dir/MANIFEST.json.
func LoadManifest(dir string) (*Manifest, error) {
	b, err := os.ReadFile(filepath.Join(dir, ManifestName))
	if err != nil {
		return nil, err
	}
	m := new(Manifest)
	if err := json.Unmarshal(b, m); err != nil {
		return nil, fmt.Errorf("%w: %v", errManifestParse, err)
	}
	return m, nil
}

// Save writes the manifest atomically (temp + rename) and returns the
// blake3-256 of the written bytes, for anchoring in the hot database.
func (m *Manifest) Save(dir string) ([32]byte, error) {
	var zero [32]byte
	m.sortEntries()
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return zero, err
	}
	tmp := filepath.Join(dir, ManifestName+".tmp")
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return zero, err
	}
	f, err := os.OpenFile(tmp, os.O_RDWR, 0)
	if err == nil {
		f.Sync()
		f.Close()
	}
	if err := os.Rename(tmp, filepath.Join(dir, ManifestName)); err != nil {
		return zero, err
	}
	return blake3.Sum256(b), nil
}

// ManifestHash hashes the on-disk manifest bytes (for comparing against
// the anchor stored in the hot database).
func ManifestHash(dir string) ([32]byte, error) {
	var zero [32]byte
	b, err := os.ReadFile(filepath.Join(dir, ManifestName))
	if err != nil {
		return zero, err
	}
	return blake3.Sum256(b), nil
}

// RebuildManifest scans dir for *.era files and reconstructs the manifest
// from their self-authenticating footers. Pruned entries cannot be
// recovered (their files are gone) — callers get a sealed-only ledger.
func RebuildManifest(dir string) (*Manifest, error) {
	des, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	m := &Manifest{Version: 1, Generation: "rebuilt-" + time.Now().UTC().Format(time.RFC3339)}
	seen := make(map[string]string) // "class-era" → file
	for _, de := range des {
		name := de.Name()
		if de.IsDir() || !strings.HasSuffix(name, FileExt) {
			continue
		}
		r, err := OpenReader(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("rebuild: %s: %w", name, err)
		}
		st, _ := os.Stat(filepath.Join(dir, name))
		var size int64
		if st != nil {
			size = st.Size()
		}
		if m.Span == 0 {
			m.Span, m.ChainID, m.GenesisHash = r.Meta.Span, r.Meta.ChainID, r.Meta.GenesisHash
		} else if m.Span != r.Meta.Span || m.ChainID != r.Meta.ChainID || m.GenesisHash != r.Meta.GenesisHash {
			r.Close()
			return nil, fmt.Errorf("rebuild: %s belongs to a different store (span/chain/genesis)", name)
		}
		key := r.Meta.Class + "-" + fmt.Sprint(r.Meta.Era)
		if prev, dup := seen[key]; dup {
			r.Close()
			return nil, fmt.Errorf("rebuild: duplicate era files for %s: %s and %s — a stale generation must be removed first", key, prev, name)
		}
		seen[key] = name
		m.Entries = append(m.Entries, ManifestEntry{
			Class: r.Meta.Class, Era: r.Meta.Era, File: name, Size: size,
			PayloadBlake3: r.Meta.PayloadBlake3,
			FirstHash:     r.Meta.FirstHash, LastHash: r.Meta.LastHash,
			Status: StatusSealed,
		})
		r.Close()
	}
	m.sortEntries()
	return m, nil
}
