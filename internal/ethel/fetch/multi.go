// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package fetch

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/n42blockchain/N42/log"
)

// MultiSourceFetcher composes a set of per-transport Fetchers and dispatches
// each Asset.Fetch call to the highest-priority Source whose transport is
// supported. If a Source fails, it falls through to the next one in
// priority order.
//
// Typical wiring:
//
//	multi := download.NewMultiSourceFetcher(
//	    httpFetcher,
//	    torrentFetcher,
//	)
//	defer multi.Close()
//	if err := multi.Fetch(ctx, asset, "/var/lib/eth-el/leaves", progressFn); err != nil {
//	    ...
//	}
//
// The Fetcher list is small (one per transport), so dispatch is O(N) on
// every Fetch call — that's fine because the per-Asset cost is dominated
// by the actual download.
type MultiSourceFetcher struct {
	fetchers []Fetcher
}

// NewMultiSourceFetcher constructs a MultiSourceFetcher around the given
// per-transport Fetchers. The order of arguments does not matter — actual
// dispatch order is decided by Source.Priority on each Asset.
func NewMultiSourceFetcher(fetchers ...Fetcher) *MultiSourceFetcher {
	return &MultiSourceFetcher{fetchers: fetchers}
}

// Kinds returns the union of every wrapped Fetcher's Kinds. The result is
// sorted and deduplicated.
func (m *MultiSourceFetcher) Kinds() []SourceKind {
	seen := make(map[SourceKind]struct{})
	for _, f := range m.fetchers {
		for _, k := range f.Kinds() {
			seen[k] = struct{}{}
		}
	}
	out := make([]SourceKind, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Fetch tries each Source in priority order, falling back to the next on
// any non-context-cancellation error. Returns ErrNoSourcesAvailable if
// no Source could be served by any registered Fetcher and the most
// recent transport error otherwise.
func (m *MultiSourceFetcher) Fetch(ctx context.Context, asset Asset, dstDir string, progress ProgressFunc) error {
	if err := asset.Validate(); err != nil {
		return err
	}

	ordered := orderSourcesByPriority(asset.Sources)

	var (
		anyAttempted bool
		lastErr      error
	)
	for _, src := range ordered {
		fetcher := m.fetcherFor(src.Kind)
		if fetcher == nil {
			log.Debug("download: no fetcher for source kind, skipping",
				"asset", asset.Name, "kind", src.Kind, "uri", src.URI)
			continue
		}
		// Per-attempt asset: only the chosen Source is exposed to the
		// downstream Fetcher so it does not have to think about
		// dispatch. Note we keep the original SHA256 / size checks.
		attempt := asset
		attempt.Sources = []Source{src}

		anyAttempted = true
		log.Debug("download: trying source",
			"asset", asset.Name, "kind", src.Kind, "uri", src.URI, "priority", src.Priority)
		err := fetcher.Fetch(ctx, attempt, dstDir, progress)
		if err == nil {
			return nil
		}
		// Context cancellation propagates immediately — there is no
		// point trying another mirror if the caller asked us to stop.
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		log.Warn("download: source failed, falling through",
			"asset", asset.Name, "kind", src.Kind, "uri", src.URI, "err", err)
		lastErr = err
	}

	if !anyAttempted {
		return fmt.Errorf("%w: asset=%s", ErrNoSourcesAvailable, asset.Name)
	}
	return fmt.Errorf("download: all sources failed for asset %s: %w", asset.Name, lastErr)
}

// fetcherFor returns the first registered Fetcher that supports kind, or
// nil. The first-registered policy lets callers express preference (HTTP
// over BT) by registration order when priorities tie.
func (m *MultiSourceFetcher) fetcherFor(kind SourceKind) Fetcher {
	for _, f := range m.fetchers {
		if supportsKind(f, kind) {
			return f
		}
	}
	return nil
}

// orderSourcesByPriority returns a copy of in sorted by descending
// Priority. Sources with equal priority retain their input order.
func orderSourcesByPriority(in []Source) []Source {
	out := append([]Source(nil), in...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Priority > out[j].Priority })
	return out
}
