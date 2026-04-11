// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// fetcher.go — Fetcher interface and shared sentinel errors.
//
// Fetcher is the transport-agnostic contract implemented by HTTPFetcher,
// TorrentFetcher and WebRTCFetcher. Kinds() advertises which Source
// kinds the fetcher can handle and Fetch downloads exactly one Source
// to a local path with SHA256 verification and progress callbacks.
// ErrNoSourcesAvailable is returned when no fetcher matches any Source
// on an Asset; ErrChecksumMismatch is returned on SHA256 failure and
// the implementation must remove the partial file before returning it.

package fetch

import (
	"context"
	"errors"
)

// ErrNoSourcesAvailable is returned when none of the Sources on an Asset
// matched a registered Fetcher (or every match failed). Callers should
// treat it as a hard failure — there is no transport left to try.
var ErrNoSourcesAvailable = errors.New("download: no sources available for asset")

// ErrChecksumMismatch is returned by Fetcher implementations when a
// completed download's SHA256 does not match the value declared in the
// Asset. The implementation is responsible for removing the partial file
// before returning this error so a retry starts clean.
var ErrChecksumMismatch = errors.New("download: SHA256 mismatch")

// Progress carries periodic status updates from a Fetcher to its caller.
// Total may be 0 when the transport does not know the total size up
// front (rare for HTTP/BT, possible for WebRTC streams). When unknown,
// callers should display Bytes alone.
type Progress struct {
	// Asset is the file the progress refers to. Stable across all
	// updates for a single Fetch call.
	Asset string

	// Bytes is the cumulative byte count downloaded so far.
	Bytes uint64

	// Total is the expected total size in bytes. May be 0 when the
	// transport cannot determine the size up front.
	Total uint64

	// Source is the URI currently being read from. Useful for log lines
	// that say "switched to mirror X after timeout".
	Source string
}

// ProgressFunc is a non-blocking callback the Fetcher invokes to publish
// download progress. Implementations should call it at most a few times
// per second to avoid overwhelming the caller. A nil ProgressFunc is
// treated as a no-op.
type ProgressFunc func(Progress)

// Fetcher is the contract every transport implementation satisfies. A
// Fetcher knows how to fetch one or more SourceKinds; the
// MultiSourceFetcher dispatches Asset fetches to the right backend.
//
// Implementations must be safe for concurrent use: eth-el's bootstrap
// and catch-up services may share a single Fetcher across multiple
// in-flight Assets.
type Fetcher interface {
	// Kinds returns the SourceKinds this Fetcher can handle. The
	// MultiSourceFetcher uses this to route an Asset's Sources to the
	// right backend.
	Kinds() []SourceKind

	// Fetch downloads asset into dstDir, verifying its SHA256 on
	// completion. progress may be nil.
	//
	// On success the file at filepath.Join(dstDir, asset.Name) is
	// guaranteed to exist, be exactly asset.SizeBytes bytes long, and
	// hash to asset.SHA256.
	//
	// On failure the implementation must clean up any partial file it
	// wrote and return a meaningful error wrapping the underlying
	// transport error. ErrChecksumMismatch and ErrNoSourcesAvailable
	// are valid sentinel returns.
	Fetch(ctx context.Context, asset Asset, dstDir string, progress ProgressFunc) error
}

// supportsKind reports whether f handles k. Used by MultiSourceFetcher
// dispatch.
func supportsKind(f Fetcher, k SourceKind) bool {
	for _, kk := range f.Kinds() {
		if kk == k {
			return true
		}
	}
	return false
}
