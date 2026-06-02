package coldseed

import (
	"testing"

	"github.com/n42blockchain/N42/internal/sync/torrentsync"
)

func names(fs []FileState) []string {
	out := make([]string, len(fs))
	for i, f := range fs {
		out[i] = f.FileName
	}
	return out
}
func keepNames(ss []torrentsync.SegmentInfo) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = s.FileName
	}
	return out
}
func has(ss []string, x string) bool {
	for _, s := range ss {
		if s == x {
			return true
		}
	}
	return false
}

func TestPlanSeedSealedIdempotentActiveAlways(t *testing.T) {
	existing := &torrentsync.Manifest{Segments: []torrentsync.SegmentInfo{
		{FileName: "bodyc.0097.cdat", Size: 100, InfoHash: "aa"},
		{FileName: "bodyc.0098.cdat", Size: 200, InfoHash: "bb"},
		{FileName: "bodyc.0099.cdat", Size: 50, InfoHash: "cc"}, // was active last run
	}}
	current := []FileState{
		{FileName: "bodyc.0097.cdat", Size: 100},              // sealed, unchanged → keep
		{FileName: "bodyc.0098.cdat", Size: 200},              // sealed, unchanged → keep
		{FileName: "bodyc.0099.cdat", Size: 80},               // now sealed but GREW since last run → reseed
		{FileName: "bodyc.0100.cdat", Size: 30, Active: true}, // new active → reseed
	}
	p := PlanSeed(current, existing)

	if !has(keepNames(p.Keep), "bodyc.0097.cdat") || !has(keepNames(p.Keep), "bodyc.0098.cdat") {
		t.Errorf("unchanged sealed should be kept, got keep=%v", keepNames(p.Keep))
	}
	if len(p.Keep) != 2 {
		t.Errorf("want 2 kept, got %v", keepNames(p.Keep))
	}
	if !has(names(p.Reseed), "bodyc.0099.cdat") {
		t.Error("grown (now-sealed) file must be reseeded")
	}
	if !has(names(p.Reseed), "bodyc.0100.cdat") {
		t.Error("active file must always be reseeded")
	}
	if len(p.Reseed) != 2 {
		t.Errorf("want 2 reseed, got %v", names(p.Reseed))
	}
}

func TestPlanSeedFirstRunSeedsAll(t *testing.T) {
	current := []FileState{
		{FileName: "headerc.0000.cdat", Size: 10},
		{FileName: "headerc.0001.cdat", Size: 10, Active: true},
	}
	p := PlanSeed(current, nil) // no prior manifest
	if len(p.Keep) != 0 || len(p.Reseed) != 2 {
		t.Errorf("first run: want all reseeded, got keep=%d reseed=%d", len(p.Keep), len(p.Reseed))
	}
}

func TestPlanSeedActiveReseededEvenIfInManifest(t *testing.T) {
	// Active file present in manifest with same size must STILL be reseeded
	// (its infohash is treated as stale because the tail keeps growing).
	existing := &torrentsync.Manifest{Segments: []torrentsync.SegmentInfo{
		{FileName: "witness.0042.cdat", Size: 999, InfoHash: "zz"},
	}}
	current := []FileState{{FileName: "witness.0042.cdat", Size: 999, Active: true}}
	p := PlanSeed(current, existing)
	if len(p.Reseed) != 1 || len(p.Keep) != 0 {
		t.Errorf("active must reseed regardless of manifest match, got keep=%d reseed=%d", len(p.Keep), len(p.Reseed))
	}
}
