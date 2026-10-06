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
	// Optional marks files that belong to the eventual tier shape but are not
	// required to identify a usable tier. Caplin seeds are optional because the
	// HotStuff chain never produces them and eth-el can start before they exist.
	Optional bool
	// WindowSegments > 0 publishes only the newest N data (.cdat) segments of
	// this section, keeping every index file. This is what makes `full` a
	// history-windowed product (EIP-4444 shape) rather than a full archive:
	// the bodies section ships roughly the last year. It is enforced here, in
	// the selector, precisely because an assembly-time-only rule gets missed —
	// a run that published full-history bodies as "full" is what prompted it.
	WindowSegments int
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
	// Torrent is filled by n42-eth-torrent once a .torrent is built over this
	// tier's files, so the client can fetch over BitTorrent (magnet) as well as
	// HTTP. Omitted until then.
	Torrent *TorrentInfo `json:"torrent,omitempty"`
}

// TorrentInfo records the per-tier BitTorrent metadata. InfoHash is reproducible
// across producers (depends only on name + piece length + ordered files + piece
// hashes); Magnet/trackers/webseeds are convenience fields.
type TorrentInfo struct {
	Name        string   `json:"name"`
	InfoHash    string   `json:"infohash"`     // hex SHA-1 of the bencoded info dict
	Magnet      string   `json:"magnet"`       // magnet: URI
	PieceLength int64    `json:"piece_length"` // bytes
	Pieces      int      `json:"pieces"`       // number of pieces
	TotalBytes  int64    `json:"total_bytes"`
	TorrentFile string   `json:"torrent_file"` // relative path of the .torrent (if written under datadir)
	WebSeeds    []string `json:"webseeds,omitempty"`
	Trackers    []string `json:"trackers,omitempty"`
}

// FileEntry is one row in Manifest.Files.
type FileEntry struct {
	Path       string `json:"path"`       // relative to datadir
	Section    string `json:"section"`    // logical grouping
	Size       int64  `json:"size"`       // bytes
	Blake2b256 string `json:"blake2b256"` // hex-encoded
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

// beaconCheckpointSection is the caplin checkpoint-sync seed: a single finalized
// BeaconState (weak-subjectivity anchor, ~150 MB zstd) so minimal/full can start
// embedded consensus validation without an external checkpoint URL. Produced by
// the caplin work (#31); absent until then, so it shows as a known gap. Provisional
// path under caplin/ (matches erigon's caplin datadir convention).
var beaconCheckpointSection = Section{
	Name:     "beacon-checkpoint",
	Patterns: []string{"caplin/checkpoint/state.*.ssz.zst"},
	Optional: true,
}

// beaconArchiveSection is the full-history beacon-block archive, extreme-compressed
// (the ~20 GB erigon caplin archive re-compressed). Shipped by archive so it can
// serve consensus history. Produced by #31; absent until then. Provisional glob.
var beaconArchiveSection = Section{
	Name:     "beacon-archive",
	Patterns: []string{"caplin/beacon-archive.*.zst", "caplin/beacon-archive.*.idx"},
	Optional: true,
}

// headersSection and codeSection are shared by every tier that ships them.
// They were duplicated per tier until minimal started shipping them too, at
// which point three copies of the same two globs was one copy too many.
var headersSection = Section{
	Name:     "headers",
	Patterns: []string{"chain/freezer/headerc.cidx", "chain/freezer/headerc.*.cdat"},
}

var codeSection = Section{
	Name:     "code",
	Patterns: []string{"chain/freezer/codes.cidx", "chain/freezer/codes.*.cdat"},
}

// minimal: compact state snapshot + headers + codes + caplin checkpoint-sync
// seed (~47 GB weekly). Bodies and receipts are NOT bundled — the snapshot→tip
// catch-up fetches those live (IDC/peers).
//
// headerc and codes ARE bundled (operator decision, 2026-08-30, restoring the
// §6b spec): a snapshot-direct node needs the header chain to validate what it
// catches up on and the content-addressed code freezer to execute it, and
// pulling either at start-up is exactly what a seed exists to avoid. The
// earlier snapshot-only build shipped 24.6 GB against a 47 GB spec, and the
// three-mode tests only passed because the test dirs were assembled by hand
// with headerc + codes added — a node installed from the manifest had neither.
func minimalSelector() *Selector {
	s := &Selector{Mode: "minimal", Sections: []Section{headersSection, codeSection}}
	s.Sections = append(s.Sections, snapshotSections...)
	s.Sections = append(s.Sections, beaconCheckpointSection)
	return s
}

// DefaultFullBodiesWindow is how many bodyc data segments the `full` product
// ships: roughly one year of bodies (EIP-4444 shape). Measured at the 2026-08
// tip, where a year spans the last 56 two-GB segments (~118 GB). Segment sizes
// are fixed but blocks grow, so re-measure when the year boundary moves and
// pass --bodies-window rather than editing this.
const DefaultFullBodiesWindow = 56

// full: snapshot + headers + codes + 1-yr hot bodies + txindex (~160 GB). Receipts
// and history (accthist/storhist) are NOT shipped — a full node's receipts are the
// ones it produces itself while catching up and following the tip (kept, not
// pruned); bodies older than ~1 yr stay on cold seeders (1-of-N).
//
// The one-year bodies window is enforced HERE rather than left to whoever
// assembles the datadir: freezer segment numbering is monotonic in block
// height, so the selector keeps the newest N .cdat plus the whole cidx. A run
// that published full-history bodies as "full" (672 GB instead of 160 GB) is
// what moved this out of the assembly instructions and into the code.
func fullSelector() *Selector { return fullSelectorWindowed(DefaultFullBodiesWindow) }

func fullSelectorWindowed(window int) *Selector {
	s := &Selector{Mode: "full", Sections: []Section{headersSection, codeSection}}
	s.Sections = append(s.Sections, snapshotSections...)
	s.Sections = append(s.Sections,
		Section{
			Name:           "bodies",
			Patterns:       []string{"chain/freezer/bodyc.cidx", "chain/freezer/bodyc.*.cdat"},
			WindowSegments: window,
		},
		Section{Name: "tx-index", Patterns: []string{"chain/freezer/txindex.cidx", "chain/freezer/txindex.*.cdat"}},
		beaconCheckpointSection, // caplin checkpoint-sync seed for embedded consensus
	)
	return s
}

// SelectorForWithWindow is SelectorFor with an explicit bodies window for the
// `full` mode (0 = the default). Other modes ignore it: archive ships every
// body by definition, minimal ships none.
func SelectorForWithWindow(mode string, window int) (*Selector, error) {
	if mode == "full" && window > 0 {
		return fullSelectorWindowed(window), nil
	}
	return SelectorFor(mode)
}

// archive: raw materials ONLY — headers + bodies + codes + witness + txindex +
// anchors (~829 GB). State, receipts, changesets, and history are NOT shipped;
// they are regenerated locally by witness-replay from genesis (saves ~640 GB
// download vs shipping the derived data). No snapshot — archive rebuilds state.
func archiveSelector() *Selector {
	return &Selector{Mode: "archive", Sections: []Section{
		headersSection,
		{Name: "bodies", Patterns: []string{"chain/freezer/bodyc.cidx", "chain/freezer/bodyc.*.cdat"}},
		codeSection,
		{Name: "witness", Patterns: []string{"chain/freezer/witness.cidx", "chain/freezer/witness.*.cdat"}},
		{Name: "tx-index", Patterns: []string{"chain/freezer/txindex.cidx", "chain/freezer/txindex.*.cdat"}},
		{Name: "anchors", Patterns: []string{"chain/freezer/anchorc.cidx", "chain/freezer/anchorc.*.cdat", "chain/freezer/anchorc.blocks"}},
		beaconArchiveSection, // full-history beacon archive, extreme-compressed
	}}
}
