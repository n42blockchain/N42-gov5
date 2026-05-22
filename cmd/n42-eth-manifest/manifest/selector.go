// Package manifest defines the file-selector contract that maps
// each distribution mode to the set of paths it ships. The CLI in
// cmd/n42-eth-manifest is a thin wrapper around this package; the
// snapshot fetch/verify client will consume the same selector.
package manifest

import "fmt"

// Selector lists glob patterns (relative to datadir) that belong
// to a particular distribution mode. Each pattern uses Go's
// filepath.Match syntax; helpers that walk the datadir join the
// patterns with the root via filepath.Glob.
//
// Patterns are organised by the logical section they belong to so
// the manifest writer can preserve grouping in output.
type Selector struct {
	Mode     string    // "minimal" | "full" | "archive"
	Sections []Section // ordered for stable manifest output
}

// Section is a named group of file patterns (e.g. "headers",
// "state-accounts", "witness").
type Section struct {
	Name     string
	Patterns []string
}

// Manifest is the on-disk JSON shape. Mirrors the spec in
// docs/ethel/n42-eth-client-distribution.md.
type Manifest struct {
	Network    string       `json:"network"`
	Height     uint64       `json:"height"`
	Mode       string       `json:"mode"`
	CreatedAt  string       `json:"created_at"`
	ManifestID string       `json:"manifest_id"`
	Files      []*FileEntry `json:"files"`
}

// FileEntry is one row in Manifest.Files.
type FileEntry struct {
	Path       string `json:"path"`        // relative to datadir
	Section    string `json:"section"`     // logical grouping
	Size       int64  `json:"size"`        // bytes
	Blake2b256 string `json:"blake2b256"`  // hex-encoded
}

// SelectorFor returns the Selector for a named mode. Unknown
// modes error rather than silently fall back.
func SelectorFor(mode string) (*Selector, error) {
	switch mode {
	case "minimal":
		return minimalSelector(), nil
	case "full":
		return fullSelector(), nil
	case "archive":
		return archiveSelector(), nil
	}
	return nil, fmt.Errorf("unknown mode %q (want minimal|full|archive)", mode)
}

// WithSenders returns a Selector that additionally includes the
// opt-in senders pack. Idempotent — calling twice doesn't
// duplicate the section.
func WithSenders(s *Selector) *Selector {
	for _, sec := range s.Sections {
		if sec.Name == "senders" {
			return s
		}
	}
	out := &Selector{Mode: s.Mode + "+senders", Sections: make([]Section, len(s.Sections)+1)}
	copy(out.Sections, s.Sections)
	out.Sections[len(s.Sections)] = Section{
		Name:     "senders",
		Patterns: []string{"chain/freezer/senders.cidx", "chain/freezer/senders.*.cdat"},
	}
	return out
}

// minimal: headers + state snapshot + code.
func minimalSelector() *Selector {
	return &Selector{
		Mode: "minimal",
		Sections: []Section{
			{
				Name:     "headers",
				Patterns: []string{"chain/freezer/headerc.cidx", "chain/freezer/headerc.*.cdat"},
			},
			{
				Name:     "code",
				Patterns: []string{"chain/freezer/codes.cidx", "chain/freezer/codes.*.cdat"},
			},
			{
				Name: "state-accounts",
				Patterns: []string{
					"snapshot/accounts.*.idx",
					"snapshot/accounts.*.ef",
					"snapshot/accounts.*.val.zst",
					"snapshot/accounts.*.codedict",
				},
			},
			{
				Name: "state-storage",
				Patterns: []string{
					"snapshot/storage.*.idx",
					"snapshot/storage.*.ef",
					"snapshot/storage.*.val.zst",
				},
			},
		},
	}
}

// full: minimal + bodies + receipts + history.
// Senders is intentionally NOT included (opt-in via WithSenders).
func fullSelector() *Selector {
	s := minimalSelector()
	s.Mode = "full"
	s.Sections = append(s.Sections,
		Section{Name: "bodies", Patterns: []string{
			"chain/freezer/bodyc.cidx", "chain/freezer/bodyc.*.cdat",
		}},
		Section{Name: "receipts", Patterns: []string{
			"chain/freezer/receipts.cidx", "chain/freezer/receipts.*.cdat",
		}},
		Section{Name: "history-accounts", Patterns: []string{
			"chain/freezer/accthist.cidx", "chain/freezer/accthist.*.cdat",
		}},
		Section{Name: "history-storage", Patterns: []string{
			"chain/freezer/storhist.cidx", "chain/freezer/storhist.*.cdat",
		}},
		Section{Name: "tx-index", Patterns: []string{
			"chain/freezer/txindex.cidx", "chain/freezer/txindex.*.cdat",
		}},
	)
	return s
}

// archive: full + witness.
func archiveSelector() *Selector {
	s := fullSelector()
	s.Mode = "archive"
	s.Sections = append(s.Sections,
		Section{Name: "witness", Patterns: []string{
			"chain/freezer/witness.cidx", "chain/freezer/witness.*.cdat",
		}},
	)
	return s
}
