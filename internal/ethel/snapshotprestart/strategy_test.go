package snapshotprestart

import (
	"testing"
)

// TestSelectStrategy_AlreadyCurrent: gap=0 → StrategyNone.
func TestSelectStrategy_AlreadyCurrent(t *testing.T) {
	got := SelectStrategy(StrategyInput{Gap: 0, ModeRequest: "auto", DeltaSourceSet: true})
	if got != StrategyNone {
		t.Errorf("gap=0 got=%v want=%v", got, StrategyNone)
	}
}

// TestSelectStrategy_AutoSmallGapUsesDelta: gap inside delta
// retention window with delta source available → StrategyDelta.
func TestSelectStrategy_AutoSmallGapUsesDelta(t *testing.T) {
	got := SelectStrategy(StrategyInput{
		Gap: 50_000, ModeRequest: "auto",
		DeltaSourceSet: true, DeltaWindow: 1_000_000,
	})
	if got != StrategyDelta {
		t.Errorf("small gap with delta source got=%v want=%v", got, StrategyDelta)
	}
}

// TestSelectStrategy_AutoLargeGapFallsBack: gap beyond delta
// retention but libp2p is available → StrategyLibp2p.
func TestSelectStrategy_AutoLargeGapFallsBack(t *testing.T) {
	got := SelectStrategy(StrategyInput{
		Gap: 5_000_000, ModeRequest: "auto",
		DeltaSourceSet: true, DeltaWindow: 1_000_000,
		Libp2pAvailable: true,
	})
	if got != StrategyLibp2p {
		t.Errorf("large gap with libp2p got=%v want=%v", got, StrategyLibp2p)
	}
}

// TestSelectStrategy_AutoHugeGapDemandsFetch: gap so large that
// even libp2p is impractical (or libp2p unavailable + delta
// window exceeded) → StrategyFetch (operator must intervene).
func TestSelectStrategy_AutoHugeGapDemandsFetch(t *testing.T) {
	got := SelectStrategy(StrategyInput{
		Gap: 50_000_000, ModeRequest: "auto",
		DeltaSourceSet: true, DeltaWindow: 1_000_000,
		Libp2pAvailable: false,
	})
	if got != StrategyFetch {
		t.Errorf("huge gap no libp2p got=%v want=%v", got, StrategyFetch)
	}
}

// TestSelectStrategy_AutoNoDeltaUsesLibp2pIfAvailable: delta
// source NOT set but libp2p is → StrategyLibp2p.
func TestSelectStrategy_AutoNoDeltaUsesLibp2pIfAvailable(t *testing.T) {
	got := SelectStrategy(StrategyInput{
		Gap: 10_000, ModeRequest: "auto",
		DeltaSourceSet: false, Libp2pAvailable: true,
	})
	if got != StrategyLibp2p {
		t.Errorf("no delta + libp2p got=%v want=%v", got, StrategyLibp2p)
	}
}

// TestSelectStrategy_AutoNothingAvailable: no delta source, no
// libp2p → StrategyFetch (operator hint).
func TestSelectStrategy_AutoNothingAvailable(t *testing.T) {
	got := SelectStrategy(StrategyInput{
		Gap: 10_000, ModeRequest: "auto",
		DeltaSourceSet: false, Libp2pAvailable: false,
	})
	if got != StrategyFetch {
		t.Errorf("auto with nothing got=%v want=%v", got, StrategyFetch)
	}
}

// TestSelectStrategy_ExplicitOff: even with a big gap, off means
// off — caller is responsible for sync.
func TestSelectStrategy_ExplicitOff(t *testing.T) {
	got := SelectStrategy(StrategyInput{
		Gap: 100, ModeRequest: "off",
		DeltaSourceSet: true,
	})
	if got != StrategyNone {
		t.Errorf("off got=%v want=%v", got, StrategyNone)
	}
}

// TestSelectStrategy_ExplicitDeltaIgnoresUnavailable: user
// explicitly asks for delta. We honour it and let the actual
// CatchUp call surface the "no delta available" error rather
// than silently substituting libp2p.
func TestSelectStrategy_ExplicitDelta(t *testing.T) {
	got := SelectStrategy(StrategyInput{
		Gap: 10_000, ModeRequest: "delta",
		DeltaSourceSet: false, Libp2pAvailable: true,
	})
	if got != StrategyDelta {
		t.Errorf("explicit delta got=%v want=%v", got, StrategyDelta)
	}
}

// TestSelectStrategy_ExplicitLibp2p: user asks for libp2p.
func TestSelectStrategy_ExplicitLibp2p(t *testing.T) {
	got := SelectStrategy(StrategyInput{
		Gap: 10_000, ModeRequest: "libp2p",
		DeltaSourceSet: true,
	})
	if got != StrategyLibp2p {
		t.Errorf("explicit libp2p got=%v want=%v", got, StrategyLibp2p)
	}
}

// TestSelectStrategy_DefaultModeBehavesAsAuto: empty
// ModeRequest treated as "auto".
func TestSelectStrategy_DefaultModeBehavesAsAuto(t *testing.T) {
	got := SelectStrategy(StrategyInput{
		Gap: 10_000, ModeRequest: "",
		DeltaSourceSet: true, DeltaWindow: 1_000_000,
	})
	if got != StrategyDelta {
		t.Errorf("default mode got=%v want=%v (auto-equivalent)", got, StrategyDelta)
	}
}

// TestSelectStrategy_InvalidModeFallsToAuto: any unknown mode
// string is treated as auto. This is a forward-compatible
// design: when we add new modes later, an old binary won't fail
// loud — it falls back to auto.
func TestSelectStrategy_InvalidModeFallsToAuto(t *testing.T) {
	got := SelectStrategy(StrategyInput{
		Gap: 10_000, ModeRequest: "wibble",
		DeltaSourceSet: true, DeltaWindow: 1_000_000,
	})
	if got != StrategyDelta {
		t.Errorf("invalid mode got=%v want=%v", got, StrategyDelta)
	}
}
