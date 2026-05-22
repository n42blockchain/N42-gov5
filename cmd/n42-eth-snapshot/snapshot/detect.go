package snapshot

import (
	"github.com/n42blockchain/N42/cmd/n42-eth-manifest/manifest"
)

// DetectResult describes what mode a datadir currently contains.
type DetectResult struct {
	Mode            string   // "minimal" | "full" | "archive" | ""
	Height          uint64   // 0 if no manifest available
	Intact          bool     // true if all sections of the detected mode are present
	MissingSections []string // sections missing from the next-tier-up
}

// DetectMode classifies the datadir as the highest mode whose
// sections are all populated. If a manifest-<mode>.json is present
// the height is read from it; otherwise height is 0.
func DetectMode(datadir string) (*DetectResult, error) {
	tiers := []string{"archive", "full", "minimal"}
	for _, mode := range tiers {
		sel, _ := manifest.SelectorFor(mode)
		missing, err := manifest.MissingSections(datadir, sel)
		if err != nil {
			return nil, err
		}
		if len(missing) == 0 {
			res := &DetectResult{Mode: mode, Intact: true}
			if m, err := ManifestFor(datadir, mode); err == nil {
				res.Height = m.Height
			}
			// Report what's needed to climb to the next tier.
			if next := nextTier(mode); next != "" {
				nextSel, _ := manifest.SelectorFor(next)
				nm, _ := manifest.MissingSections(datadir, nextSel)
				res.MissingSections = nm
			}
			return res, nil
		}
	}
	// Not even minimal is complete; report partial state.
	sel, _ := manifest.SelectorFor("minimal")
	missing, _ := manifest.MissingSections(datadir, sel)
	return &DetectResult{Mode: "", Intact: false, MissingSections: missing}, nil
}

func nextTier(mode string) string {
	switch mode {
	case "minimal":
		return "full"
	case "full":
		return "archive"
	}
	return ""
}
