// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package fetch

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// ManifestKind tags a Manifest by what it points at. eth-el publishes two
// kinds: bootstrap leaves journals and historical chain segments.
// Distinguishing them lets a single loader catch obvious misconfiguration
// (pointing the catch-up service at a leaves manifest, or vice versa).
type ManifestKind string

const (
	// ManifestKindLeaves is a manifest of leaves-journal Assets used by
	// the bootstrap service to recover a recent PlainState.
	ManifestKindLeaves ManifestKind = "leaves"

	// ManifestKindSegments is a manifest of EraE chain-segment Assets
	// used by the catch-up service to play forward to chain tip.
	ManifestKindSegments ManifestKind = "segments"
)

// ManifestVersion is the on-wire schema version. Bumped when a
// non-backward-compatible field is added or renamed.
const ManifestVersion = 1

// Manifest is the JSON document that describes a set of Assets and their
// transports. Both bootstrap and catch-up consume the same envelope —
// only Kind and per-Asset metadata differ.
//
// Wire format:
//
//	{
//	  "version": 1,
//	  "kind": "leaves",
//	  "chain_id": 1,
//	  "updated_at": "2026-04-10T12:00:00Z",
//	  "assets": [
//	    {
//	      "name": "leaves.000000-022000000.cdat",
//	      "size_bytes": 47185920000,
//	      "sha256": "d2a0...c1",
//	      "from_block": 0,
//	      "to_block":   22000000,
//	      "sources": [
//	        {"kind": "https", "uri": "https://cdn1.n42.network/...", "priority": 100},
//	        {"kind": "https", "uri": "https://cdn2.n42.network/...", "priority": 100},
//	        {"kind": "bt",    "uri": "magnet:?xt=urn:btih:...&ws=https://...", "priority": 50}
//	      ]
//	    }
//	  ]
//	}
type Manifest struct {
	Version   int             `json:"version"`
	Kind      ManifestKind    `json:"kind"`
	ChainID   uint64          `json:"chain_id"`
	UpdatedAt time.Time       `json:"updated_at"`
	Assets    []ManifestAsset `json:"assets"`
}

// ManifestAsset is the wire form of one Asset. SHA256 is hex-encoded
// without a 0x prefix to match the convention every other N42 hex
// field uses; encoding/decoding happens in ToAsset / FromAsset so
// callers always work with the typed Asset.
type ManifestAsset struct {
	Name      string           `json:"name"`
	SizeBytes uint64           `json:"size_bytes"`
	SHA256    string           `json:"sha256"`
	FromBlock uint64           `json:"from_block,omitempty"`
	ToBlock   uint64           `json:"to_block,omitempty"`
	Sources   []ManifestSource `json:"sources"`
}

// ManifestSource is the wire form of one Source.
type ManifestSource struct {
	Kind     SourceKind `json:"kind"`
	URI      string     `json:"uri"`
	Priority int        `json:"priority,omitempty"`
}

// Validate enforces the schema invariants enforced offline (without any
// network call):
//   - Version must match the loader's expected version,
//   - Kind must be one of the documented values,
//   - At least one Asset, every Asset.SHA256 parses to 32 bytes,
//   - Every Asset has at least one Source.
//
// Validate does NOT verify SHA256 against actual bytes — that happens at
// download time inside the Fetcher.
func (m *Manifest) Validate() error {
	if m.Version != ManifestVersion {
		return fmt.Errorf("manifest: unsupported version %d (loader expects %d)", m.Version, ManifestVersion)
	}
	switch m.Kind {
	case ManifestKindLeaves, ManifestKindSegments:
	default:
		return fmt.Errorf("manifest: unsupported kind %q", m.Kind)
	}
	if m.ChainID == 0 {
		return errors.New("manifest: chain_id is required")
	}
	if len(m.Assets) == 0 {
		return errors.New("manifest: at least one asset is required")
	}
	for i, ma := range m.Assets {
		if _, err := ma.ToAsset(); err != nil {
			return fmt.Errorf("manifest.assets[%d]: %w", i, err)
		}
	}
	return nil
}

// Assets returns the Manifest's payload as a slice of typed Assets.
// Validate() must succeed first; Assets() panics on a malformed entry
// because the loader is meant to call Validate before reaching here.
func (m *Manifest) AssetList() []Asset {
	out := make([]Asset, 0, len(m.Assets))
	for _, ma := range m.Assets {
		a, err := ma.ToAsset()
		if err != nil {
			panic("manifest.AssetList called on un-validated manifest: " + err.Error())
		}
		out = append(out, a)
	}
	return out
}

// ToAsset converts a wire-form ManifestAsset into the typed Asset the
// Fetcher consumes. SHA256 is parsed from hex; an empty SHA256 is
// allowed for tests but the resulting zero hash is treated as "not
// supplied" by the Fetcher (and therefore unsafe in production).
func (ma *ManifestAsset) ToAsset() (Asset, error) {
	a := Asset{
		Name:      ma.Name,
		SizeBytes: ma.SizeBytes,
	}
	if ma.SHA256 != "" {
		raw, err := hex.DecodeString(strings.TrimPrefix(ma.SHA256, "0x"))
		if err != nil {
			return Asset{}, fmt.Errorf("decode sha256: %w", err)
		}
		if len(raw) != 32 {
			return Asset{}, fmt.Errorf("sha256 must be 32 bytes, got %d", len(raw))
		}
		copy(a.SHA256[:], raw)
	}
	a.Sources = make([]Source, 0, len(ma.Sources))
	for _, ms := range ma.Sources {
		a.Sources = append(a.Sources, Source{
			Kind:     ms.Kind,
			URI:      ms.URI,
			Priority: ms.Priority,
		})
	}
	if err := a.Validate(); err != nil {
		return Asset{}, err
	}
	return a, nil
}

// AssetToManifest is the inverse of ToAsset, used by tooling that
// generates manifests (e.g. the publish step that runs on a beefy
// machine after building a leaves journal).
func AssetToManifest(a Asset) ManifestAsset {
	ma := ManifestAsset{
		Name:      a.Name,
		SizeBytes: a.SizeBytes,
		SHA256:    hex.EncodeToString(a.SHA256[:]),
		Sources:   make([]ManifestSource, 0, len(a.Sources)),
	}
	for _, s := range a.Sources {
		ma.Sources = append(ma.Sources, ManifestSource{
			Kind:     s.Kind,
			URI:      s.URI,
			Priority: s.Priority,
		})
	}
	return ma
}

// LoadManifest reads and decodes a Manifest from one of:
//
//	https://...     fetched via HTTP GET (the loader uses its own client
//	                so it does not depend on a configured HTTPFetcher)
//	file://path     read from local disk
//	./relative/path bare paths are treated as local files
//
// The result is validated before return; callers can immediately use
// it without re-checking.
func LoadManifest(ctx context.Context, source string) (*Manifest, error) {
	data, err := readManifest(ctx, source)
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("decode manifest: %w", err)
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// readManifest dispatches by URL scheme. Kept private so future
// transports (s3://, ipfs://) can be added without callers caring.
func readManifest(ctx context.Context, source string) ([]byte, error) {
	if source == "" {
		return nil, errors.New("manifest: empty source")
	}
	parsed, err := url.Parse(source)
	if err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") {
		return readManifestHTTP(ctx, source)
	}
	if err == nil && parsed.Scheme == "file" {
		return os.ReadFile(parsed.Path)
	}
	// Bare path → local file.
	return os.ReadFile(source)
}

// readManifestHTTP downloads the manifest body. Manifests are tiny (a few
// KB), so a hard limit and a tight timeout protect against pathological
// servers serving multi-GB JSON.
func readManifestHTTP(ctx context.Context, uri string) ([]byte, error) {
	const maxManifestBytes = 8 << 20 // 8 MiB
	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, uri, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("manifest GET %s: %w", uri, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("manifest GET %s: status %d", uri, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxManifestBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maxManifestBytes {
		return nil, fmt.Errorf("manifest GET %s: body exceeds %d bytes", uri, maxManifestBytes)
	}
	return body, nil
}
