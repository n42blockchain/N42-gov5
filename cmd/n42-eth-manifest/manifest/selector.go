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
	case "mobile":
		// mobile ships NO file bundle: it's the app binary + a checkpoint config
		// {block, hash, IDC URL}. Headers/bodies/witness/code are streamed from the
		// IDC per block (rolling ~900-block in-memory window), the trust anchor is
		// fetched at runtime, and state is served via verified EIP-1186 proofs.
		// There is nothing to download/seed, so there is no manifest.
		return nil, fmt.Errorf("mobile has no file bundle (app + checkpoint config, streams from IDC)")
	case "minimal":
		return minimalSelector(), nil
	case "full":
		return fullSelector(), nil
	case "archive":
		return archiveSelector(), nil
	}
	return nil, fmt.Errorf("unknown mode %q (want minimal|full|archive; mobile is app+config)", mode)
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

var snapshotSections = []Section{
	{Name: "state-accounts", Patterns: []string{
		"snapshot/accounts.*.idx", "snapshot/accounts.*.ef",
		"snapshot/accounts.*.val.zst", "snapshot/accounts.*.codedict",
	}},
	{Name: "state-storage", Patterns: []string{
		"snapshot/storage.*.idx", "snapshot/storage.*.ef", "snapshot/storage.*.val.zst",
	}},
}

// minimal: compact state snapshot ONLY (~25.7 GB, weekly). Headers/bodies/codes
// for the snapshot→tip catch-up are fetched live (IDC/peers), not bundled; older
// data missing locally is requested from peers on demand.
func minimalSelector() *Selector {
	return &Selector{Mode: "minimal", Sections: append([]Section{}, snapshotSections...)}
}

// full: snapshot + headers + codes + 1-yr hot bodies + txindex (~130 GB). Receipts
// and history (accthist/storhist) are NOT shipped — receipts/logs serve the latest
// window; bodies older than ~1 yr stay on cold seeders (1-of-N). The 1-yr hot
// bodies subset is produced by assembling the datadir with only the hot
// bodyc.NNNN.cdat (EIP-4444 boundary) — the glob then matches just those.
func fullSelector() *Selector {
	s := &Selector{Mode: "full", Sections: []Section{
		{Name: "headers", Patterns: []string{"chain/freezer/headerc.cidx", "chain/freezer/headerc.*.cdat"}},
		{Name: "code", Patterns: []string{"chain/freezer/codes.cidx", "chain/freezer/codes.*.cdat"}},
	}}
	s.Sections = append(s.Sections, snapshotSections...)
	s.Sections = append(s.Sections,
		Section{Name: "bodies", Patterns: []string{"chain/freezer/bodyc.cidx", "chain/freezer/bodyc.*.cdat"}},
		Section{Name: "tx-index", Patterns: []string{"chain/freezer/txindex.cidx", "chain/freezer/txindex.*.cdat"}},
	)
	return s
}

// archive: raw materials ONLY — headers + bodies + codes + witness + txindex +
// anchors (~829 GB). State, receipts, changesets, and history are NOT shipped;
// they are regenerated locally by witness-replay from genesis (saves ~640 GB
// download vs shipping the derived data). No snapshot — archive rebuilds state.
func archiveSelector() *Selector {
	return &Selector{Mode: "archive", Sections: []Section{
		{Name: "headers", Patterns: []string{"chain/freezer/headerc.cidx", "chain/freezer/headerc.*.cdat"}},
		{Name: "bodies", Patterns: []string{"chain/freezer/bodyc.cidx", "chain/freezer/bodyc.*.cdat"}},
		{Name: "code", Patterns: []string{"chain/freezer/codes.cidx", "chain/freezer/codes.*.cdat"}},
		{Name: "witness", Patterns: []string{"chain/freezer/witness.cidx", "chain/freezer/witness.*.cdat"}},
		{Name: "tx-index", Patterns: []string{"chain/freezer/txindex.cidx", "chain/freezer/txindex.*.cdat"}},
		{Name: "anchors", Patterns: []string{"chain/freezer/anchorc.cidx", "chain/freezer/anchorc.*.cdat", "chain/freezer/anchorc.blocks"}},
	}}
}

