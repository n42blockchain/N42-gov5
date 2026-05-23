package snapshot

import (
	"encoding/json"
	"fmt"
	"io"
)

// StatusReport answers "am I behind, and by how much?". Used by
// the `n42-eth-snapshot status` subcommand to tell the operator
// whether catch-up work is needed.
type StatusReport struct {
	Network       string
	Mode          string
	LocalHeight   uint64
	RemoteHeight  uint64
	BehindBlocks  uint64
	UpToDate      bool

	// LocalManifestID and RemoteManifestID help diagnose mismatches.
	// A non-empty LocalManifestID that doesn't match the publisher's
	// latest means a delta apply is needed.
	LocalManifestID  string
	RemoteManifestID string

	// Note is a one-line operator hint covering edge cases:
	// no local manifest, mismatched mode, etc.
	Note string
}

// remoteIndex is the schema for <mirror>/<network>/releases.json
// — the same shape n42-eth-publish writes (PublishedIndex in
// cmd/n42-eth-publish). We re-declare here as a local struct to
// avoid an import cycle (snapshot is the lower-level package).
type remoteIndex struct {
	Network  string                            `json:"network"`
	Updated  string                            `json:"updated_at"`
	Latest   map[string]*remoteReleaseRef      `json:"latest"`
	Releases []*remoteReleaseRef               `json:"releases"`
}

type remoteReleaseRef struct {
	Height    uint64            `json:"height"`
	CreatedAt string            `json:"created_at"`
	Manifests map[string]string `json:"manifests"`
}

// Status fetches the publisher's releases.json index from
// `source` (file:// or http(s)://), compares the latest published
// height for `mode` against the local datadir's manifest, and
// returns a structured report.
//
// `source` should point at the per-network root (e.g.
// file:///mnt/mirror/mainnet/), where releases.json lives.
//
// Errors only on hard failures: source unreachable, releases.json
// malformed, or the latest publisher release doesn't list the
// requested mode. Missing local manifest is a SOFT condition —
// LocalHeight=0 and Note populated.
func Status(datadir, source, mode string) (*StatusReport, error) {
	src, err := OpenSource(source)
	if err != nil {
		return nil, fmt.Errorf("open source: %w", err)
	}
	rc, _, err := src.Open("releases.json")
	if err != nil {
		return nil, fmt.Errorf("fetch releases.json: %w", err)
	}
	defer rc.Close()
	var idx remoteIndex
	if err := json.NewDecoder(rc).Decode(&idx); err != nil {
		return nil, fmt.Errorf("decode releases.json: %w", err)
	}
	latest, ok := idx.Latest[mode]
	if !ok || latest == nil {
		return nil, fmt.Errorf("publisher has no latest entry for mode %q", mode)
	}
	rep := &StatusReport{
		Network:          idx.Network,
		Mode:             mode,
		RemoteHeight:     latest.Height,
		RemoteManifestID: latest.Manifests[mode],
	}

	localMan, err := ManifestFor(datadir, mode)
	if err != nil {
		rep.Note = fmt.Sprintf("no local manifest-%s.json in %s — bootstrap needed", mode, datadir)
		rep.BehindBlocks = rep.RemoteHeight
		return rep, nil
	}
	rep.LocalHeight = localMan.Height
	rep.LocalManifestID = localMan.ManifestID
	if rep.LocalHeight > rep.RemoteHeight {
		rep.Note = fmt.Sprintf("local is AHEAD of publisher by %d blocks (publisher may be behind)",
			rep.LocalHeight-rep.RemoteHeight)
	} else if rep.LocalHeight == rep.RemoteHeight {
		if rep.LocalManifestID != rep.RemoteManifestID {
			rep.Note = "heights match but manifest_ids differ — file-level corruption suspected; run verify"
		} else {
			rep.UpToDate = true
		}
	} else {
		rep.BehindBlocks = rep.RemoteHeight - rep.LocalHeight
	}
	return rep, nil
}

// Print writes the StatusReport in a friendly format.
func (r *StatusReport) Print(w io.Writer) {
	fmt.Fprintf(w, "network        : %s\n", r.Network)
	fmt.Fprintf(w, "mode           : %s\n", r.Mode)
	fmt.Fprintf(w, "local height   : %d\n", r.LocalHeight)
	fmt.Fprintf(w, "remote height  : %d\n", r.RemoteHeight)
	fmt.Fprintf(w, "behind blocks  : %d\n", r.BehindBlocks)
	fmt.Fprintf(w, "up to date     : %v\n", r.UpToDate)
	if r.LocalManifestID != "" || r.RemoteManifestID != "" {
		fmt.Fprintf(w, "local mid      : %s\n", r.LocalManifestID)
		fmt.Fprintf(w, "remote mid     : %s\n", r.RemoteManifestID)
	}
	if r.Note != "" {
		fmt.Fprintf(w, "note           : %s\n", r.Note)
	}
}
