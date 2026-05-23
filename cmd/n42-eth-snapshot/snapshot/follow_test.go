package snapshot

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// TestFollow_StaysCurrent: with no new releases, follower sleeps
// quietly and reports a single "no work" cycle.
func TestFollow_StaysCurrent(t *testing.T) {
	src := touchFakeArchive(t)
	if err := writeFakeManifestWithHeight(t, src, "minimal", 25000); err != nil {
		t.Fatalf("manifest: %v", err)
	}
	m, _ := ManifestFor(src, "minimal")
	mirror := t.TempDir()
	publishFakeMirror(t, src, mirror, "simnet", "minimal", m)
	client := t.TempDir()
	_, _ = Fetch("file://"+filepath.Join(mirror, "simnet", "25000", "minimal"),
		client, "minimal", false, false, 2)

	cfg := FollowConfig{
		Datadir:      client,
		Source:       "file://" + filepath.Join(mirror, "simnet"),
		Mode:         "minimal",
		PollInterval: 50 * time.Millisecond,
		MaxCycles:    3,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	rep, err := Follow(ctx, cfg)
	if err != nil {
		t.Fatalf("Follow: %v", err)
	}
	if rep.Cycles != 3 {
		t.Errorf("Cycles=%d want 3", rep.Cycles)
	}
	if rep.AppliedDeltas != 0 {
		t.Errorf("AppliedDeltas=%d want 0", rep.AppliedDeltas)
	}
	if rep.FinalHeight != 25000 {
		t.Errorf("FinalHeight=%d want 25000", rep.FinalHeight)
	}
}

// TestFollow_AppliesNewRelease: publisher emits a new release
// mid-loop, follower picks it up on next poll.
func TestFollow_AppliesNewRelease(t *testing.T) {
	srcA := touchFakeArchive(t)
	if err := writeFakeManifestWithHeight(t, srcA, "minimal", 1000); err != nil {
		t.Fatalf("manifest A: %v", err)
	}
	mA, _ := ManifestFor(srcA, "minimal")
	mirror := t.TempDir()
	publishFakeMirror(t, srcA, mirror, "simnet", "minimal", mA)

	client := t.TempDir()
	_, _ = Fetch("file://"+filepath.Join(mirror, "simnet", "1000", "minimal"),
		client, "minimal", false, false, 2)

	// Background publisher: after a short delay, publish a new
	// release + delta from 1000 → 2000.
	publishDone := make(chan struct{})
	go func() {
		defer close(publishDone)
		time.Sleep(80 * time.Millisecond)
		srcB := touchFakeArchive(t)
		_ = os.WriteFile(filepath.Join(srcB, "chain/freezer/headerc.cidx"),
			[]byte("v-2000"), 0o644)
		if err := writeFakeManifestWithHeight(t, srcB, "minimal", 2000); err != nil {
			t.Errorf("manifest B: %v", err)
			return
		}
		mB, _ := ManifestFor(srcB, "minimal")
		publishFakeMirror(t, srcB, mirror, "simnet", "minimal", mB)

		d := t.TempDir()
		if err := buildDeltaTree(t, srcA, srcB, "minimal", d); err != nil {
			t.Errorf("buildDeltaTree: %v", err)
			return
		}
		dst := filepath.Join(mirror, "simnet", "deltas", "1000-2000", "minimal")
		_ = os.MkdirAll(dst, 0o755)
		_ = copyTree(d, dst)
		_ = copyTestFile(filepath.Join(srcB, "manifest-minimal.json"),
			filepath.Join(dst, "manifest-minimal.json"))
		appendDeltaToMirror(t, mirror, "simnet", "minimal", 1000, 2000, mB.ManifestID)
	}()

	cfg := FollowConfig{
		Datadir:      client,
		Source:       "file://" + filepath.Join(mirror, "simnet"),
		Mode:         "minimal",
		PollInterval: 50 * time.Millisecond,
		MaxCycles:    10,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	rep, err := Follow(ctx, cfg)
	if err != nil {
		t.Fatalf("Follow: %v", err)
	}
	<-publishDone

	if rep.AppliedDeltas < 1 {
		t.Errorf("AppliedDeltas=%d want ≥1", rep.AppliedDeltas)
	}
	if rep.FinalHeight != 2000 {
		t.Errorf("FinalHeight=%d want 2000", rep.FinalHeight)
	}
}

// TestFollow_CancelStopsCleanly: ctx cancel mid-loop returns without
// error and reports CancelledClean=true.
func TestFollow_CancelStopsCleanly(t *testing.T) {
	src := touchFakeArchive(t)
	if err := writeFakeManifestWithHeight(t, src, "minimal", 100); err != nil {
		t.Fatalf("manifest: %v", err)
	}
	m, _ := ManifestFor(src, "minimal")
	mirror := t.TempDir()
	publishFakeMirror(t, src, mirror, "simnet", "minimal", m)
	client := t.TempDir()
	_, _ = Fetch("file://"+filepath.Join(mirror, "simnet", "100", "minimal"),
		client, "minimal", false, false, 2)

	cfg := FollowConfig{
		Datadir:      client,
		Source:       "file://" + filepath.Join(mirror, "simnet"),
		Mode:         "minimal",
		PollInterval: 200 * time.Millisecond,
		MaxCycles:    0, // unlimited — cancel is what stops us
	}
	ctx, cancel := context.WithCancel(context.Background())

	var done atomic.Bool
	var rep *FollowReport
	var err error
	go func() {
		rep, err = Follow(ctx, cfg)
		done.Store(true)
	}()
	time.Sleep(150 * time.Millisecond)
	cancel()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !done.Load() {
		time.Sleep(10 * time.Millisecond)
	}
	if !done.Load() {
		t.Fatalf("Follow did not exit within 2s of cancel")
	}
	if err != nil {
		t.Errorf("Follow returned err on cancel: %v", err)
	}
	if !rep.CancelledClean {
		t.Errorf("CancelledClean=false want true")
	}
}
