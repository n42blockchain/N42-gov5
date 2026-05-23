package snapshotprestart

// Strategy is the catch-up mechanism PreStartSync should use.
// Selected from (Gap, available mechanisms, operator request) at
// startup; logged so operators know which path is in play.
type Strategy int

const (
	// StrategyNone — no catch-up work. Either we're already
	// current, or the operator opted out via --catch-up-mode off.
	StrategyNone Strategy = iota

	// StrategyDelta — apply chained deltas from the publisher
	// mirror. Cheapest path; only works when the gap is inside
	// the publisher's retained delta window.
	StrategyDelta

	// StrategyLibp2p — backfill blocks from libp2p peers. Slower
	// than delta apply (per-block work) but handles arbitrary
	// gaps within the peer mesh's reach.
	StrategyLibp2p

	// StrategyFetch — gap is too wide for delta and libp2p isn't
	// available. Operator must run `n42-eth-snapshot fetch` to
	// pull a fresh archive.
	StrategyFetch
)

// String renders the strategy for log messages.
func (s Strategy) String() string {
	switch s {
	case StrategyNone:
		return "none"
	case StrategyDelta:
		return "delta"
	case StrategyLibp2p:
		return "libp2p"
	case StrategyFetch:
		return "fetch"
	}
	return "unknown"
}

// StrategyInput is everything the selector needs.
type StrategyInput struct {
	Gap             uint64 // blocks behind publisher latest; 0 if current
	ModeRequest     string // operator's --catch-up-mode (auto|off|delta|libp2p|fetch). "" == "auto"
	DeltaSourceSet  bool   // is --snapshot.source configured?
	DeltaWindow     uint64 // publisher's typical delta retention (blocks). 0 = unknown
	Libp2pAvailable bool   // is libp2p sync configured (peers + catchup pipeline)?
}

// SelectStrategy is a pure function over StrategyInput. Caller
// uses the returned Strategy to dispatch to the matching
// executor. Tests live in strategy_test.go.
//
// Rules (in order):
//
//   1. Gap == 0                       → none
//   2. Explicit ModeRequest non-auto  → honour it (caller surfaces availability errors)
//   3. Auto + DeltaSourceSet + Gap inside DeltaWindow → delta
//   4. Auto + Libp2pAvailable         → libp2p
//   5. Otherwise                      → fetch (operator hint)
func SelectStrategy(in StrategyInput) Strategy {
	if in.Gap == 0 {
		return StrategyNone
	}

	switch in.ModeRequest {
	case "off":
		return StrategyNone
	case "delta":
		return StrategyDelta
	case "libp2p":
		return StrategyLibp2p
	case "fetch":
		return StrategyFetch
	}

	// "" or "auto" or anything we don't recognise → auto rules.
	if in.DeltaSourceSet {
		// Prefer delta if the gap fits the retention window.
		// When DeltaWindow is 0 (unknown), we still try delta —
		// CatchUp will report inapplicable if no chain leads
		// from our local manifest_id.
		if in.DeltaWindow == 0 || in.Gap <= in.DeltaWindow {
			return StrategyDelta
		}
	}

	if in.Libp2pAvailable {
		return StrategyLibp2p
	}
	return StrategyFetch
}
