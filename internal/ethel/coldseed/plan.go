// Package coldseed plans which columnar freezer files an archive/seeder node
// should (re)seed over BitTorrent so other nodes can pull full history
// (headers + bodies + witness) under a 1-of-N assumption.
//
// All files are seeded, not just cold ones. The crucial distinction:
//   - SEALED files (every file except the highest-numbered) are immutable once
//     a higher file exists → seed once; subsequent runs skip them (size matches
//     the manifest) so their infohash is stable.
//   - The ACTIVE file (highest NNNN) is still being appended to, so its content
//     (and therefore its infohash) changes over time → it is re-seeded on every
//     run. Run the seeder periodically (e.g. weekly) so the active file's
//     manifest entry tracks the growing tail.
package coldseed

import "github.com/n42blockchain/N42/internal/sync/torrentsync"

// FileState is a freezer data file as seen on disk this run.
type FileState struct {
	FileName           string // e.g. "bodyc.0335.cdat"
	Size               int64
	FromBlock, ToBlock uint64
	Active             bool // highest-numbered file of its kind (still being written)
}

// SeedPlan splits the current files into those carried over unchanged and those
// that must be (re)seeded this run.
type SeedPlan struct {
	Keep   []torrentsync.SegmentInfo // unchanged sealed files already seeded — reuse infohash
	Reseed []FileState               // new/changed sealed + every active file
}

// PlanSeed decides, given the files present this run and the previous manifest,
// what to (re)seed. Sealed files unchanged in size since the manifest are kept
// (idempotent — fetched/seeded at most once); new or size-changed sealed files
// and ALL active files are reseeded.
func PlanSeed(current []FileState, existing *torrentsync.Manifest) SeedPlan {
	prev := map[string]torrentsync.SegmentInfo{}
	if existing != nil {
		for _, s := range existing.Segments {
			prev[s.FileName] = s
		}
	}
	var plan SeedPlan
	for _, f := range current {
		if f.Active {
			plan.Reseed = append(plan.Reseed, f) // tail still growing → always reseed
			continue
		}
		if p, ok := prev[f.FileName]; ok && p.Size == f.Size && p.InfoHash != "" {
			plan.Keep = append(plan.Keep, p) // sealed + unchanged + already has infohash
			continue
		}
		plan.Reseed = append(plan.Reseed, f) // new or changed sealed file
	}
	return plan
}
