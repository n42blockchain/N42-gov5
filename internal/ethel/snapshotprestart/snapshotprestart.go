// Package snapshotprestart wires the n42-eth-snapshot client
// library into cmd/eth-el's startup sequence. Before opening the
// Engine API, eth-el can opt into a pre-start sync that:
//
//   1. Asks the publisher mirror for its latest manifest_id.
//   2. If the local datadir is behind, applies the available
//      delta chain via snapshot.CatchUp.
//   3. Reports the result so eth-el can log + decide whether to
//      proceed.
//
// The auto-catchup is bounded: if the gap is > MaxBlocks the
// helper bails with a clear error instead of a multi-hour
// catchup loop. Operators handle large gaps with an explicit
// `n42-eth-snapshot fetch`.
package snapshotprestart

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/n42blockchain/N42/cmd/n42-eth-snapshot/snapshot"
)

// Config is the input to PreStartSync.
type Config struct {
	Datadir   string        // local datadir
	Source    string        // publisher mirror, file:// or http(s)://
	Mode      string        // minimal | full | archive
	MaxBlocks uint64        // refuse auto-catchup if gap > this (0 = no cap)
	MaxIter   int           // per-CatchUp max-iterations cap (0 = no cap)
	Timeout   time.Duration // total time budget (0 = no cap)

	// G4 strategy selection. ModeRequest is the operator's
	// --catch-up-mode (auto|off|delta|libp2p|fetch); empty == "auto".
	// Libp2pAvailable + DeltaWindow let auto pick the right path.
	ModeRequest     string
	Libp2pAvailable bool
	DeltaWindow     uint64

	// AutoFetch — when datadir has no local manifest (first boot),
	// run snapshot.Fetch(latest, mode) to lay down the initial
	// archive before attempting catch-up. Default false because
	// the initial fetch can be many GB and we don't want operators
	// to trigger that by accident; opt-in via --snapshot.auto-fetch.
	AutoFetch     bool
	FetchParallel int // workers for snapshot.Fetch; 0 → snapshot default
}

// Result captures what PreStartSync did so eth-el can log it.
type Result struct {
	Skipped       bool   // Source empty → no work attempted
	WasCurrent    bool   // local already matched publisher latest
	StartHeight   uint64
	FinalHeight   uint64
	GapAtStart    uint64
	DeltasApplied int
	ElapsedMS     int64
	WarnMessage   string // populated when we choose to keep starting despite an issue
	Strategy      Strategy // which path was selected (G4)

	// InitialFetched is true when PreStartSync ran the first-boot
	// snapshot.Fetch (AutoFetch path). Helps logs distinguish
	// "first-boot bootstrap" from "delta catch-up".
	InitialFetched     bool
	InitialFetchBytes  int64
	InitialFetchFiles  int
}

// PreStartSync runs the optional auto-catchup. Returns (Result,
// nil) on success — including when there's nothing to do — and
// (Result, err) only for HARD failures (source unreachable,
// gap-too-wide). eth-el is expected to log the result and
// continue unless err is non-nil.
func PreStartSync(ctx context.Context, cfg Config) (*Result, error) {
	rep := &Result{}
	if cfg.Source == "" {
		rep.Skipped = true
		return rep, nil
	}
	if cfg.Datadir == "" {
		return nil, fmt.Errorf("snapshotprestart: --datadir required")
	}
	if cfg.Mode == "" {
		cfg.Mode = "archive"
	}
	if cfg.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cfg.Timeout)
		defer cancel()
	}

	t0 := time.Now()

	st, err := snapshot.Status(cfg.Datadir, cfg.Source, cfg.Mode)
	if err != nil {
		return rep, fmt.Errorf("snapshotprestart: status: %w", err)
	}
	rep.StartHeight = st.LocalHeight
	rep.GapAtStart = st.BehindBlocks

	// First-boot path: no local manifest. Either auto-fetch the
	// initial archive or fail loud so the operator runs the fetch
	// step explicitly.
	if st.LocalManifestID == "" && st.LocalHeight == 0 {
		if !cfg.AutoFetch {
			return rep, fmt.Errorf(
				"snapshotprestart: no local manifest-%s.json in %s — pass AutoFetch=true or run "+
					"`n42-eth-snapshot fetch --source %s/%d/%s --datadir %s --mode %s` first",
				cfg.Mode, cfg.Datadir, cfg.Source, st.RemoteHeight, cfg.Mode, cfg.Datadir, cfg.Mode)
		}
		fetchSrc := fmt.Sprintf("%s/%d/%s", strings.TrimRight(cfg.Source, "/"), st.RemoteHeight, cfg.Mode)
		fr, ferr := snapshot.Fetch(fetchSrc, cfg.Datadir, cfg.Mode, false, false, cfg.FetchParallel)
		if ferr != nil {
			return rep, fmt.Errorf("snapshotprestart: initial fetch: %w", ferr)
		}
		if fr == nil || !fr.OK {
			return rep, fmt.Errorf("snapshotprestart: initial fetch reported FAIL (downloaded=%d failed=%d)",
				fr.Downloaded, fr.Failed)
		}
		rep.InitialFetched = true
		rep.InitialFetchBytes = fr.BytesXfer
		rep.InitialFetchFiles = fr.Downloaded
		// Re-status after the initial fetch so we know whether
		// delta catch-up is still needed (publisher may have
		// rolled forward while we were downloading).
		st, err = snapshot.Status(cfg.Datadir, cfg.Source, cfg.Mode)
		if err != nil {
			return rep, fmt.Errorf("snapshotprestart: post-fetch status: %w", err)
		}
		rep.StartHeight = st.LocalHeight
		rep.GapAtStart = st.BehindBlocks
	}

	if st.UpToDate {
		rep.WasCurrent = true
		rep.FinalHeight = st.LocalHeight
		rep.Strategy = StrategyNone
		rep.ElapsedMS = time.Since(t0).Milliseconds()
		return rep, nil
	}

	if cfg.MaxBlocks > 0 && st.BehindBlocks > cfg.MaxBlocks {
		return rep, fmt.Errorf("snapshotprestart: gap %d > MaxBlocks %d — run `n42-eth-snapshot fetch` explicitly",
			st.BehindBlocks, cfg.MaxBlocks)
	}

	// G4 strategy selection: pick the catch-up mechanism that
	// fits the gap + available infrastructure.
	rep.Strategy = SelectStrategy(StrategyInput{
		Gap:             st.BehindBlocks,
		ModeRequest:     cfg.ModeRequest,
		DeltaSourceSet:  cfg.Source != "",
		DeltaWindow:     cfg.DeltaWindow,
		Libp2pAvailable: cfg.Libp2pAvailable,
	})

	switch rep.Strategy {
	case StrategyNone:
		// Operator opted out via ModeRequest=off. Nothing to do.
		rep.FinalHeight = st.LocalHeight
		rep.ElapsedMS = time.Since(t0).Milliseconds()
		return rep, nil
	case StrategyLibp2p:
		// Defer to the in-node libp2p catchup pipeline. We don't
		// invoke it here; eth-el's node startup runs catchup
		// after PreStartSync returns. Just log + carry on.
		rep.FinalHeight = st.LocalHeight
		rep.WarnMessage = fmt.Sprintf("gap=%d resolved by in-node libp2p catchup (not delta)", st.BehindBlocks)
		rep.ElapsedMS = time.Since(t0).Milliseconds()
		return rep, nil
	case StrategyFetch:
		return rep, fmt.Errorf("snapshotprestart: gap %d too wide for available mechanisms — run `n42-eth-snapshot fetch` explicitly",
			st.BehindBlocks)
	}

	// StrategyDelta — the existing behaviour.
	cur, err := snapshot.CatchUp(cfg.Datadir, cfg.Source, cfg.Mode, cfg.MaxIter)
	rep.ElapsedMS = time.Since(t0).Milliseconds()
	if cur != nil {
		rep.DeltasApplied = cur.Iterations
		rep.FinalHeight = cur.FinalHeight
	}
	if err != nil {
		return rep, fmt.Errorf("snapshotprestart: catchup: %w", err)
	}
	if cur != nil && !cur.UpToDate {
		// Catchup ran but didn't fully converge (e.g. ran out of
		// chained deltas). Not a startup blocker — log + proceed.
		rep.WarnMessage = fmt.Sprintf("catchup stopped at height %d, publisher at %d — node will start partially-current",
			cur.FinalHeight, cur.RemoteHeight)
	}
	return rep, nil
}
