// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

// Package download is the unified file-fetching layer for N42 nodes.
//
// It exists so cmd/eth-el (and any future binary that needs to pull large
// files from a network) can describe what it needs in transport-agnostic
// terms — a SHA256, a size, a list of candidate sources — and have the
// runtime pick whichever transport actually works at the moment.
//
// Currently shipped fetchers:
//
//   - HTTPFetcher: parallel range downloads from CDN / HTTPS mirrors,
//     rotation across the URL list inside a Source.
//
// Planned fetchers:
//
//   - TorrentFetcher: thin wrapper around
//     internal/distributed/storage/torrent.Client (BitTorrent v1, magnet,
//     and the WebSeed BEP 19 fallback that anacrolix/torrent already
//     supports out of the box).
//   - WebRTCFetcher: pion/webrtc-based WebTorrent bridge.
//
// All fetchers implement Fetcher, and a MultiSourceFetcher composes them so
// the caller never has to know which transport is in use. See
// internal/ethel/ARCHITECTURE.md for how eth-el's bootstrap and catch-up
// services consume this package.
package fetch

import (
	"errors"
	"fmt"
)

// SourceKind tags a Source by transport so a Fetcher can decide whether
// to handle it. New transports add a new constant; the matching Fetcher
// implementation is registered with a MultiSourceFetcher in the eth-el
// node setup.
type SourceKind string

const (
	// SourceHTTPS is a plain HTTPS GET URL — typically a CDN edge or a
	// long-lived hosting service. The HTTPFetcher accepts these.
	SourceHTTPS SourceKind = "https"

	// SourceHTTP is the same as SourceHTTPS but plaintext. Allowed for
	// LAN test setups; HTTPFetcher refuses it for production manifests
	// unless the caller explicitly opts in.
	SourceHTTP SourceKind = "http"

	// SourceBT is a BitTorrent v1 magnet link or info-hash hex.
	SourceBT SourceKind = "bt"

	// SourceBTV2 is a BitTorrent v2 magnet (BEP 52) — distinct from
	// SourceBT because the underlying client must speak v2.
	SourceBTV2 SourceKind = "bt-v2"

	// SourceWebRTC is a WebTorrent / pion-based RTC link.
	SourceWebRTC SourceKind = "webrtc"
)

// Source describes one place an Asset can be fetched from. Each Source
// belongs to exactly one Asset; an Asset may carry many Sources of the
// same or different kinds. Within a single Asset, MultiSourceFetcher
// tries higher-Priority Sources first and falls back on lower-Priority
// ones if a fetch fails.
type Source struct {
	// Kind is the transport tag — see the SourceKind constants.
	Kind SourceKind

	// URI is the transport-specific resource identifier:
	//
	//   https / http  → fully-qualified URL
	//   bt            → magnet link or 40-char info-hash hex
	//   bt-v2         → BTv2 magnet (xt=urn:btmh:...)
	//   webrtc        → WebTorrent magnet/URL
	URI string

	// Priority orders Sources within the same Asset. Higher numbers are
	// tried first. Equal-priority Sources are tried in declaration order.
	Priority int
}

// Asset is one file the caller wants to materialise on local disk. The
// Sources slice may mix transports — a typical leaves segment will list
// two or three HTTPS mirrors plus a BT magnet plus an optional BT v2
// magnet.
//
// Name is the on-disk file name (relative to the destination directory),
// SizeBytes is the expected total size, and SHA256 is the content hash
// the Fetcher must verify after the download finishes. Mismatched
// content is an error and the partial file is removed.
type Asset struct {
	// Name is the relative file path under the destination directory
	// passed to Fetcher.Fetch. It must not escape the directory; the
	// Fetcher rejects "..".
	Name string

	// SizeBytes is the exact file size after download. A successful
	// Fetch writes exactly this many bytes.
	SizeBytes uint64

	// SHA256 is the verifier hash. All zero is treated as "not
	// supplied" and verification is skipped (test mode only — production
	// manifests must always carry a hash).
	SHA256 [32]byte

	// Sources lists the transports that can serve the Asset.
	Sources []Source
}

// SourcesByKind returns the subset of Sources that match the given kind.
// Used by MultiSourceFetcher to dispatch to a per-kind sub-fetcher.
func (a *Asset) SourcesByKind(k SourceKind) []Source {
	out := make([]Source, 0, len(a.Sources))
	for _, s := range a.Sources {
		if s.Kind == k {
			out = append(out, s)
		}
	}
	return out
}

// HasSourceKind reports whether any Source uses the given transport.
func (a *Asset) HasSourceKind(k SourceKind) bool {
	for _, s := range a.Sources {
		if s.Kind == k {
			return true
		}
	}
	return false
}

// Validate sanity-checks an Asset before any fetcher touches it.
// Validation enforces:
//
//   - Name is non-empty and contains no path traversal,
//   - SizeBytes is non-zero (eth-el manifests never publish empty files),
//   - at least one Source is configured.
func (a *Asset) Validate() error {
	if a.Name == "" {
		return errors.New("asset: Name is required")
	}
	if a.SizeBytes == 0 {
		return errors.New("asset: SizeBytes is required")
	}
	if len(a.Sources) == 0 {
		return errors.New("asset: at least one Source is required")
	}
	if containsTraversal(a.Name) {
		return fmt.Errorf("asset: Name %q contains path traversal", a.Name)
	}
	return nil
}

// containsTraversal returns true if s contains any "..", any leading
// slash, or a Windows drive letter. Used by Validate to refuse Asset
// names that would let a malicious manifest write outside the destination
// directory.
func containsTraversal(s string) bool {
	if s == "" {
		return false
	}
	if s[0] == '/' || s[0] == '\\' {
		return true
	}
	if len(s) >= 2 && s[1] == ':' {
		return true
	}
	for i := 0; i < len(s)-1; i++ {
		if s[i] == '.' && s[i+1] == '.' {
			return true
		}
	}
	return false
}
