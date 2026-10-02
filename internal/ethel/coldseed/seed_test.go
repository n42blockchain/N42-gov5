package coldseed

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/internal/sync/torrentsync"
)

func TestParseFileNum(t *testing.T) {
	n, ok := parseFileNum("bodyc.0042.cdat", "bodyc")
	require.True(t, ok)
	require.Equal(t, 42, n)

	_, ok = parseFileNum("bodyc.0042.ridx", "bodyc")
	require.False(t, ok, "wrong suffix")

	_, ok = parseFileNum("headerc.0042.cdat", "bodyc")
	require.False(t, ok, "wrong prefix")

	_, ok = parseFileNum("bodyc.notanum.cdat", "bodyc")
	require.False(t, ok, "non-numeric")
}

func TestCidxGroups(t *testing.T) {
	dir := t.TempDir()

	// Two segments (seg 0, seg 1) both belonging to fileNum 3.
	buf := make([]byte, 16)
	buf[0] = 3 // seg 0 -> fileNum 3 (LE u16)
	buf[8] = 3 // seg 1 -> fileNum 3
	require.NoError(t, os.WriteFile(filepath.Join(dir, "bodyc.cidx"), buf, 0o644))

	groups := cidxGroups(dir, "bodyc")
	require.NotNil(t, groups)
	g, ok := groups[3]
	require.True(t, ok)
	require.Equal(t, [2]uint64{0, 1}, g)
}

func TestCidxGroups_AbsentOrOversized(t *testing.T) {
	dir := t.TempDir()
	require.Nil(t, cidxGroups(dir, "missing"))

	// Not a multiple of 8 bytes -> treated as a per-block index, skipped.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "headerc.cidx"), []byte{1, 2, 3}, 0o644))
	require.Nil(t, cidxGroups(dir, "headerc"))
}

func TestScanPrefix_ActiveIsHighestNum(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "bodyc.0000.cdat"), []byte("aaaa"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "bodyc.0001.cdat"), []byte("bb"), 0o644))

	states, err := ScanPrefix(dir, "bodyc")
	require.NoError(t, err)
	require.Len(t, states, 2)
	require.Equal(t, "bodyc.0000.cdat", states[0].FileName)
	require.False(t, states[0].Active)
	require.Equal(t, "bodyc.0001.cdat", states[1].FileName)
	require.True(t, states[1].Active)
}

func TestScanPrefix_NoMatchesReturnsNil(t *testing.T) {
	dir := t.TempDir()
	states, err := ScanPrefix(dir, "bodyc")
	require.NoError(t, err)
	require.Nil(t, states)
}

// fakeSink is an in-memory SeedSink that records every call and returns a
// deterministic infohash derived from the name, so assertions can check
// exactly which files were (re)seeded.
type fakeSink struct {
	calls []string
}

func (f *fakeSink) Seed(_ context.Context, name string, _ []byte, _ int) (string, error) {
	f.calls = append(f.calls, name)
	return "ih-" + name, nil
}

func TestRunOnce_SeedsAllOnFirstRunAndSkipsUnchangedSealed(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "bodyc.0000.cdat"), []byte("sealed-data"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "bodyc.0001.cdat"), []byte("active-data"), 0o644))

	manifestPath := filepath.Join(dir, "manifest.json")
	sink := &fakeSink{}
	opts := Options{
		Dir:          dir,
		Prefixes:     []string{"bodyc", ""}, // empty prefix must be skipped
		ManifestPath: manifestPath,
		ChainID:      1,
		PieceSize:    1 << 18,
	}

	require.NoError(t, RunOnce(context.Background(), opts, sink))
	require.ElementsMatch(t, []string{"bodyc.0000.cdat", "bodyc.0001.cdat"}, sink.calls)

	m, err := torrentsync.LoadManifest(manifestPath)
	require.NoError(t, err)
	require.Len(t, m.Segments, 2)

	// Second run: sealed file unchanged -> only the active file is reseeded.
	sink2 := &fakeSink{}
	require.NoError(t, RunOnce(context.Background(), opts, sink2))
	require.Equal(t, []string{"bodyc.0001.cdat"}, sink2.calls)
}

func TestRunOnce_EmptyDirIsNoop(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "manifest.json")
	sink := &fakeSink{}
	opts := Options{Dir: dir, Prefixes: []string{"bodyc"}, ManifestPath: manifestPath}
	require.NoError(t, RunOnce(context.Background(), opts, sink))
	require.Empty(t, sink.calls)
}

func TestService_StartStopSinglePass(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "bodyc.0000.cdat"), []byte("x"), 0o644))
	manifestPath := filepath.Join(dir, "manifest.json")
	sink := &fakeSink{}
	opts := Options{Dir: dir, Prefixes: []string{"bodyc"}, ManifestPath: manifestPath} // Interval=0 -> single pass

	svc := NewService(opts, sink)
	require.Equal(t, "history-seeder", svc.Name())
	require.NoError(t, svc.Start(context.Background()))

	require.Eventually(t, func() bool {
		return len(sink.calls) > 0
	}, time.Second, 10*time.Millisecond)

	require.NoError(t, svc.Stop())
}

func TestService_StopBeforeIntervalElapses(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "bodyc.0000.cdat"), []byte("x"), 0o644))
	manifestPath := filepath.Join(dir, "manifest.json")
	sink := &fakeSink{}
	opts := Options{Dir: dir, Prefixes: []string{"bodyc"}, ManifestPath: manifestPath, Interval: time.Hour}

	svc := NewService(opts, sink)
	require.NoError(t, svc.Start(context.Background()))

	require.Eventually(t, func() bool {
		return len(sink.calls) > 0
	}, time.Second, 10*time.Millisecond)

	// Stop should cancel the ctx and the loop should exit promptly even
	// though Interval is long.
	require.NoError(t, svc.Stop())
}
