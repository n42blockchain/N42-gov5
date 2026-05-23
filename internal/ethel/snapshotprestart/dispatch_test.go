package snapshotprestart

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/n42blockchain/N42/cmd/n42-eth-snapshot/snapshot"
)

// TestPreStartSync_OffReturnsNone: ModeRequest=off means do
// nothing regardless of gap.
func TestPreStartSync_OffReturnsNone(t *testing.T) {
	srcA := makeArchive(t, 1000)
	srcB := makeArchive(t, 25000)
	mirror := t.TempDir()
	publishToMirror(t, srcA, mirror, "minimal", 1000)
	publishToMirror(t, srcB, mirror, "minimal", 25000)

	client := t.TempDir()
	_, _ = snapshot.Fetch("file://"+filepath.Join(mirror, "simnet", "1000", "minimal"),
		client, "minimal", false, false, 2)

	rep, err := PreStartSync(context.Background(), Config{
		Datadir:     client,
		Source:      "file://" + filepath.Join(mirror, "simnet"),
		Mode:        "minimal",
		ModeRequest: "off",
	})
	if err != nil {
		t.Fatalf("PreStartSync: %v", err)
	}
	if rep.Strategy != StrategyNone {
		t.Errorf("Strategy=%v want StrategyNone", rep.Strategy)
	}
	if rep.DeltasApplied != 0 {
		t.Errorf("DeltasApplied=%d want 0 (off)", rep.DeltasApplied)
	}
}

// TestPreStartSync_AutoPicksLibp2pOnWideGap: gap > DeltaWindow,
// libp2p available → strategy=libp2p, no catchup invoked, warn
// logged.
func TestPreStartSync_AutoPicksLibp2pOnWideGap(t *testing.T) {
	srcA := makeArchive(t, 1000)
	srcB := makeArchive(t, 25000)
	mirror := t.TempDir()
	publishToMirror(t, srcA, mirror, "minimal", 1000)
	publishToMirror(t, srcB, mirror, "minimal", 25000)

	client := t.TempDir()
	_, _ = snapshot.Fetch("file://"+filepath.Join(mirror, "simnet", "1000", "minimal"),
		client, "minimal", false, false, 2)

	rep, err := PreStartSync(context.Background(), Config{
		Datadir:         client,
		Source:          "file://" + filepath.Join(mirror, "simnet"),
		Mode:            "minimal",
		ModeRequest:     "auto",
		DeltaWindow:     5_000, // gap is 24000, window 5000
		Libp2pAvailable: true,
		MaxBlocks:       1_000_000,
	})
	if err != nil {
		t.Fatalf("PreStartSync: %v", err)
	}
	if rep.Strategy != StrategyLibp2p {
		t.Errorf("Strategy=%v want StrategyLibp2p", rep.Strategy)
	}
	if rep.WarnMessage == "" {
		t.Errorf("expected WarnMessage to be populated for libp2p deferral")
	}
	if rep.DeltasApplied != 0 {
		t.Errorf("DeltasApplied=%d want 0 (deferred to libp2p)", rep.DeltasApplied)
	}
}

// TestPreStartSync_AutoFetchHintErrors: gap > DeltaWindow and
// libp2p NOT available → strategy=fetch, returns error so
// eth-el fails loud.
func TestPreStartSync_AutoFetchHintErrors(t *testing.T) {
	srcA := makeArchive(t, 1000)
	srcB := makeArchive(t, 25000)
	mirror := t.TempDir()
	publishToMirror(t, srcA, mirror, "minimal", 1000)
	publishToMirror(t, srcB, mirror, "minimal", 25000)

	client := t.TempDir()
	_, _ = snapshot.Fetch("file://"+filepath.Join(mirror, "simnet", "1000", "minimal"),
		client, "minimal", false, false, 2)

	rep, err := PreStartSync(context.Background(), Config{
		Datadir:         client,
		Source:          "file://" + filepath.Join(mirror, "simnet"),
		Mode:            "minimal",
		ModeRequest:     "auto",
		DeltaWindow:     5_000,
		Libp2pAvailable: false,
		MaxBlocks:       1_000_000,
	})
	if err == nil {
		t.Errorf("expected error when fetch hint emitted; rep=%+v", rep)
	}
	if rep.Strategy != StrategyFetch {
		t.Errorf("Strategy=%v want StrategyFetch", rep.Strategy)
	}
}

// TestPreStartSync_AutoSmallGapUsesDelta_E2E: gap inside window,
// delta source set → strategy=delta + actual delta apply works.
func TestPreStartSync_AutoSmallGapUsesDelta_E2E(t *testing.T) {
	srcA := makeArchive(t, 1000)
	srcB := makeArchiveWithBump(t, 2000, "v2-bump")
	mirror := t.TempDir()
	publishToMirror(t, srcA, mirror, "minimal", 1000)
	publishToMirror(t, srcB, mirror, "minimal", 2000)
	mB, _ := snapshot.ManifestFor(srcB, "minimal")
	publishDelta(t, srcA, srcB, "minimal", 1000, 2000, mB.ManifestID, mirror)

	client := t.TempDir()
	_, _ = snapshot.Fetch("file://"+filepath.Join(mirror, "simnet", "1000", "minimal"),
		client, "minimal", false, false, 2)

	rep, err := PreStartSync(context.Background(), Config{
		Datadir:     client,
		Source:      "file://" + filepath.Join(mirror, "simnet"),
		Mode:        "minimal",
		ModeRequest: "auto",
		DeltaWindow: 10_000,
		MaxIter:     5,
	})
	if err != nil {
		t.Fatalf("PreStartSync: %v", err)
	}
	if rep.Strategy != StrategyDelta {
		t.Errorf("Strategy=%v want StrategyDelta", rep.Strategy)
	}
	if rep.DeltasApplied != 1 {
		t.Errorf("DeltasApplied=%d want 1", rep.DeltasApplied)
	}
	if rep.FinalHeight != 2000 {
		t.Errorf("FinalHeight=%d want 2000", rep.FinalHeight)
	}
}

// TestPreStartSync_ExplicitLibp2pHonoured: even when delta path
// is available + gap is small, ModeRequest=libp2p defers.
func TestPreStartSync_ExplicitLibp2pHonoured(t *testing.T) {
	srcA := makeArchive(t, 1000)
	srcB := makeArchiveWithBump(t, 2000, "v2-bump")
	mirror := t.TempDir()
	publishToMirror(t, srcA, mirror, "minimal", 1000)
	publishToMirror(t, srcB, mirror, "minimal", 2000)
	mB, _ := snapshot.ManifestFor(srcB, "minimal")
	publishDelta(t, srcA, srcB, "minimal", 1000, 2000, mB.ManifestID, mirror)

	client := t.TempDir()
	_, _ = snapshot.Fetch("file://"+filepath.Join(mirror, "simnet", "1000", "minimal"),
		client, "minimal", false, false, 2)

	rep, err := PreStartSync(context.Background(), Config{
		Datadir:         client,
		Source:          "file://" + filepath.Join(mirror, "simnet"),
		Mode:            "minimal",
		ModeRequest:     "libp2p",
		Libp2pAvailable: true,
	})
	if err != nil {
		t.Fatalf("PreStartSync: %v", err)
	}
	if rep.Strategy != StrategyLibp2p {
		t.Errorf("Strategy=%v want StrategyLibp2p", rep.Strategy)
	}
	if rep.DeltasApplied != 0 {
		t.Errorf("DeltasApplied=%d want 0 (libp2p deferred)", rep.DeltasApplied)
	}
}

// helper: ensure tests don't rely on the testTempDir being
// pre-existing across runs.
func TestMain(m *testing.M) {
	os.Exit(m.Run())
}
