package coldresolve

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/n42blockchain/N42/internal/sync/torrentsync"
)

// fakeDLFetcher serves files from a map keyed by FileName; it can also be
// told to fail a given name.
type fakeDLFetcher struct {
	dir     string
	failOn  map[string]bool
	content map[string][]byte
}

func (f *fakeDLFetcher) Fetch(seg torrentsync.SegmentInfo) (string, error) {
	if f.failOn[seg.FileName] {
		return "", fmt.Errorf("fake fetch error for %s", seg.FileName)
	}
	p := filepath.Join(f.dir, seg.FileName)
	data := f.content[seg.FileName]
	if data == nil {
		data = []byte("payload-" + seg.FileName)
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		return "", err
	}
	return p, nil
}

func TestDownloadAll(t *testing.T) {
	dir := t.TempDir()
	good := []byte("good-data")
	sum := sha256.Sum256(good)

	m := &torrentsync.Manifest{Segments: []torrentsync.SegmentInfo{
		{FileName: "seg-ok.era"},
		{FileName: "seg-fail.era"},
		{FileName: "seg-badsum.era", SHA256: hex.EncodeToString(sum[:])},
		{FileName: "seg-goodsum.era", SHA256: hex.EncodeToString(sum[:])},
	}}
	f := &fakeDLFetcher{
		dir:    dir,
		failOn: map[string]bool{"seg-fail.era": true},
		content: map[string][]byte{
			"seg-badsum.era":  []byte("wrong-data"),
			"seg-goodsum.era": good,
		},
	}

	fetched, _, failed := DownloadAll(m, f, true)
	// seg-ok (no verify requested since SHA256==""): fetched.
	// seg-fail: fetch error -> failed.
	// seg-badsum: sha256 mismatch -> failed.
	// seg-goodsum: sha256 match -> fetched.
	if fetched != 2 {
		t.Errorf("fetched = %d, want 2", fetched)
	}
	if failed != 2 {
		t.Errorf("failed = %d, want 2", failed)
	}
}

func TestDownloadAll_NoVerify(t *testing.T) {
	dir := t.TempDir()
	m := &torrentsync.Manifest{Segments: []torrentsync.SegmentInfo{
		{FileName: "a.era", SHA256: "deadbeef"},
	}}
	f := &fakeDLFetcher{dir: dir}
	fetched, _, failed := DownloadAll(m, f, false)
	if fetched != 1 || failed != 0 {
		t.Errorf("fetched=%d failed=%d, want 1,0", fetched, failed)
	}
}

func TestDownloadService(t *testing.T) {
	dir := t.TempDir()
	m := &torrentsync.Manifest{Segments: []torrentsync.SegmentInfo{{FileName: "x.era"}}}
	f := &fakeDLFetcher{dir: dir}
	svc := NewDownloadService(m, f, false)

	if got := svc.Name(); got != "history-download" {
		t.Errorf("Name() = %q", got)
	}
	if err := svc.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Let the background goroutine run to completion.
	deadline := time.After(2 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(dir, "x.era")); err == nil {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for background download")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err := svc.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	// Stop is idempotent / safe to call again.
	if err := svc.Stop(); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
}

func TestDownloadService_StopBeforeStart(t *testing.T) {
	svc := NewDownloadService(&torrentsync.Manifest{}, &fakeDLFetcher{}, false)
	if err := svc.Stop(); err != nil {
		t.Fatalf("Stop before Start: %v", err)
	}
}
