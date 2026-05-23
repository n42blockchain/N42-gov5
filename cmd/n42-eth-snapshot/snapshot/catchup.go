package snapshot

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
)

// CatchUpReport summarises a CatchUp run.
type CatchUpReport struct {
	Network         string
	Mode            string
	StartHeight     uint64
	FinalHeight     uint64
	RemoteHeight    uint64
	Iterations      int
	UpToDate        bool
	TotalBytesXfer  int64
	Errors          []string
}

// CatchUp brings a client datadir from its current height to the
// publisher's latest by walking the chain of available deltas.
// maxIterations bounds the number of delta-apply cycles (0 = unlimited);
// stops early if no further delta exists from the current height.
//
// Algorithm:
//   1. Read local manifest_id from datadir
//   2. Fetch publisher releases.json index
//   3. Find a delta whose based_on points at our local manifest_id
//      (i.e. from_height == our local height with matching mid)
//   4. Apply that delta
//   5. Repeat until at remote latest, or maxIterations hit, or no
//      applicable delta found
//
// Returns a report; only errors on hard failures (source unreachable,
// invalid delta, blake2b mismatch). Reaching maxIterations without
// converging is NOT an error — UpToDate=false.
func CatchUp(datadir, source, mode string, maxIterations int) (*CatchUpReport, error) {
	rep := &CatchUpReport{Mode: mode}

	st, err := Status(datadir, source, mode)
	if err != nil {
		return rep, fmt.Errorf("status: %w", err)
	}
	rep.Network = st.Network
	rep.StartHeight = st.LocalHeight
	rep.FinalHeight = st.LocalHeight
	rep.RemoteHeight = st.RemoteHeight
	if st.UpToDate {
		rep.UpToDate = true
		return rep, nil
	}

	// Fetch publisher's full release index once; iterate from
	// there. The publisher won't add deltas mid-run for our
	// purposes — a follower (G5) would re-fetch periodically.
	src, err := OpenSource(source)
	if err != nil {
		return rep, fmt.Errorf("open source: %w", err)
	}
	rc, _, err := src.Open("releases.json")
	if err != nil {
		return rep, fmt.Errorf("fetch releases.json: %w", err)
	}
	defer rc.Close()
	var idx struct {
		Network  string                       `json:"network"`
		Releases []*remoteReleaseRef          `json:"releases"`
		Deltas   []*remoteDeltaRef            `json:"deltas"`
		Latest   map[string]*remoteReleaseRef `json:"latest"`
	}
	if err := json.NewDecoder(rc).Decode(&idx); err != nil {
		return rep, fmt.Errorf("decode releases.json: %w", err)
	}

	for {
		localMan, err := ManifestFor(datadir, mode)
		if err != nil {
			return rep, fmt.Errorf("read local manifest: %w", err)
		}
		if localMan.Height >= rep.RemoteHeight {
			rep.UpToDate = true
			rep.FinalHeight = localMan.Height
			break
		}
		if maxIterations > 0 && rep.Iterations >= maxIterations {
			break
		}
		next := findNextDelta(idx.Deltas, mode, localMan.ManifestID, localMan.Height)
		if next == nil {
			rep.Errors = append(rep.Errors,
				fmt.Sprintf("no delta available from height=%d manifest_id=%s",
					localMan.Height, localMan.ManifestID))
			break
		}
		deltaSource := joinPath(source, "deltas", formatHeightRange(next.FromHeight, next.ToHeight), mode)
		ar, aerr := ApplyDelta(deltaSource, datadir, mode, 4)
		rep.Iterations++
		if ar != nil {
			rep.TotalBytesXfer += ar.BytesXfer
		}
		if aerr != nil {
			rep.Errors = append(rep.Errors,
				fmt.Sprintf("delta %d→%d: %v", next.FromHeight, next.ToHeight, aerr))
			return rep, aerr
		}
		rep.FinalHeight = next.ToHeight
	}
	return rep, nil
}

// remoteDeltaRef matches the DeltaRef shape published in releases.json.
type remoteDeltaRef struct {
	FromHeight uint64 `json:"from_height"`
	ToHeight   uint64 `json:"to_height"`
	Mode       string `json:"mode"`
	ManifestID string `json:"manifest_id"`
	CreatedAt  string `json:"created_at"`
}

// findNextDelta picks the best delta to apply from our current
// state. Prefers the one with FromHeight == localHeight; among
// matches, picks the highest ToHeight (longest leap forward).
func findNextDelta(deltas []*remoteDeltaRef, mode, _ string, localHeight uint64) *remoteDeltaRef {
	var candidates []*remoteDeltaRef
	for _, d := range deltas {
		if d.Mode != mode {
			continue
		}
		if d.FromHeight != localHeight {
			continue
		}
		candidates = append(candidates, d)
	}
	if len(candidates) == 0 {
		return nil
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].ToHeight > candidates[j].ToHeight
	})
	return candidates[0]
}

func formatHeightRange(from, to uint64) string {
	return formatUintBase10(from) + "-" + formatUintBase10(to)
}

func formatUintBase10(n uint64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// joinPath: minimal URL-aware join. For file:// and http(s):// we
// just concat with "/" separators (the Source layer already
// normalises trailing slashes).
func joinPath(parts ...string) string {
	if len(parts) == 0 {
		return ""
	}
	out := parts[0]
	for _, p := range parts[1:] {
		if p == "" {
			continue
		}
		if out[len(out)-1] == '/' {
			out += p
		} else {
			out += "/" + p
		}
	}
	return out
}

// Print writes a human-readable report.
func (r *CatchUpReport) Print(w io.Writer) {
	fmt.Fprintf(w, "catch-up: mode=%s network=%s\n", r.Mode, r.Network)
	fmt.Fprintf(w, "  start height  : %d\n", r.StartHeight)
	fmt.Fprintf(w, "  final height  : %d\n", r.FinalHeight)
	fmt.Fprintf(w, "  remote height : %d\n", r.RemoteHeight)
	fmt.Fprintf(w, "  iterations    : %d\n", r.Iterations)
	fmt.Fprintf(w, "  bytes xferred : %.2f MB\n", float64(r.TotalBytesXfer)/1024/1024)
	fmt.Fprintf(w, "  up to date    : %v\n", r.UpToDate)
	if len(r.Errors) > 0 {
		fmt.Fprintf(w, "  errors (%d):\n", len(r.Errors))
		for _, e := range r.Errors {
			fmt.Fprintf(w, "    %s\n", e)
		}
	}
}
