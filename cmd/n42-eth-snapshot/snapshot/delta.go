package snapshot

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/n42blockchain/N42/cmd/n42-eth-manifest/manifest"
)

// DeltaManifest is the schema for delta-manifest-<mode>.json
// produced by cmd/n42-eth-delta-build. The fields mirror the
// publisher's encoder exactly (Phase H.1).
type DeltaManifest struct {
	Network            string                `json:"network"`
	FromHeight         uint64                `json:"from_height"`
	ToHeight           uint64                `json:"to_height"`
	Mode               string                `json:"mode"`
	BasedOnManifestID  string                `json:"based_on_manifest_id"`
	CreatedAt          string                `json:"created_at"`
	ManifestID         string                `json:"manifest_id"`
	Files              []*manifest.FileEntry `json:"files"`
}

// DeltaPlan describes what a `delta apply` would do without
// actually doing it.
type DeltaPlan struct {
	Source             string
	Datadir            string
	Mode               string
	LocalManifestID    string
	BaselineManifestID string // delta.BasedOnManifestID
	FromHeight         uint64
	ToHeight           uint64
	FilesToFetch       int
	BytesToFetch       int64
	Applicable         bool   // local matches the delta's baseline
	Reason             string // why not applicable, if !Applicable
}

// PlanDelta fetches the delta-manifest for the target mode from
// `source` and reports whether it can be applied to the local
// datadir, plus the size and file count of the work.
func PlanDelta(source, datadir, mode string) (*DeltaPlan, *DeltaManifest, error) {
	src, err := OpenSource(source)
	if err != nil {
		return nil, nil, err
	}

	deltaRel := fmt.Sprintf("delta-manifest-%s.json", mode)
	rc, _, err := src.Open(deltaRel)
	if err != nil {
		return nil, nil, fmt.Errorf("fetch %s: %w", deltaRel, err)
	}
	defer rc.Close()
	var dm DeltaManifest
	if err := json.NewDecoder(rc).Decode(&dm); err != nil {
		return nil, nil, fmt.Errorf("decode %s: %w", deltaRel, err)
	}
	if dm.Mode != mode {
		return nil, nil, fmt.Errorf("delta manifest mode=%s, expected %s", dm.Mode, mode)
	}

	plan := &DeltaPlan{
		Source:             source,
		Datadir:            datadir,
		Mode:               mode,
		BaselineManifestID: dm.BasedOnManifestID,
		FromHeight:         dm.FromHeight,
		ToHeight:           dm.ToHeight,
	}

	localMan, err := ManifestFor(datadir, mode)
	if err != nil {
		plan.Reason = fmt.Sprintf("no local manifest-%s.json: %v", mode, err)
		return plan, &dm, nil
	}
	plan.LocalManifestID = localMan.ManifestID

	if localMan.ManifestID != dm.BasedOnManifestID {
		plan.Reason = fmt.Sprintf("local manifest_id %s ≠ delta baseline %s",
			localMan.ManifestID, dm.BasedOnManifestID)
		return plan, &dm, nil
	}

	plan.Applicable = true
	// Count files to fetch: skip any whose local copy already has
	// the right size + hash (rare for deltas, but possible if a
	// previous apply was interrupted after writing some files).
	for _, f := range dm.Files {
		full := filepath.Join(datadir, f.Path)
		st, serr := os.Stat(full)
		if serr == nil && st.Size() == f.Size {
			if got, herr := HashFile(full); herr == nil && got == f.Blake2b256 {
				continue
			}
		}
		plan.FilesToFetch++
		plan.BytesToFetch += f.Size
	}
	return plan, &dm, nil
}

// ApplyDelta downloads each delta file, verifies its blake2b, and
// atomically replaces the local manifest with the new full
// manifest for the target height. Fails loud if the local doesn't
// match the delta baseline.
//
// IMPORTANT: ApplyDelta REQUIRES the publisher to also publish the
// full target manifest at manifest-<mode>.json next to the delta —
// it's what we install locally at the end. (The delta itself is
// only a subset of files plus baseline pointer.)
func ApplyDelta(source, datadir, mode string, parallel int) (*DeltaApplyReport, error) {
	plan, dm, err := PlanDelta(source, datadir, mode)
	if err != nil {
		return nil, err
	}
	rep := &DeltaApplyReport{
		Plan:       plan,
		FromHeight: dm.FromHeight,
		ToHeight:   dm.ToHeight,
	}
	if !plan.Applicable {
		return rep, fmt.Errorf("delta not applicable: %s", plan.Reason)
	}

	src, err := OpenSource(source)
	if err != nil {
		return rep, err
	}

	// Fetch + verify each file (skipping already-OK).
	if parallel <= 0 {
		parallel = 4
	}
	type job struct{ entry *manifest.FileEntry }
	jobs := make(chan job, parallel*2)
	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		bytes  int64
		errs   []string
		failed int
		done   int
		skipped int
	)
	for w := 0; w < parallel; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				dst := filepath.Join(datadir, j.entry.Path)
				if st, serr := os.Stat(dst); serr == nil && st.Size() == j.entry.Size {
					if got, herr := HashFile(dst); herr == nil && got == j.entry.Blake2b256 {
						mu.Lock()
						skipped++
						mu.Unlock()
						continue
					}
				}
				n, err := fetchAndVerify(src, j.entry.Path, dst,
					j.entry.Blake2b256, j.entry.Size)
				mu.Lock()
				if err != nil {
					failed++
					errs = append(errs, fmt.Sprintf("%s: %v", j.entry.Path, err))
				} else {
					done++
					bytes += n
				}
				mu.Unlock()
			}
		}()
	}
	for _, f := range dm.Files {
		jobs <- job{f}
	}
	close(jobs)
	wg.Wait()

	rep.Skipped = skipped
	rep.Downloaded = done
	rep.Failed = failed
	rep.BytesXfer = bytes
	rep.Errors = errs

	if failed > 0 {
		rep.OK = false
		return rep, fmt.Errorf("%d files failed", failed)
	}

	// Install the new full manifest atomically. The publisher must
	// have the target-height full manifest available at the same
	// source under manifest-<mode>.json.
	rc, _, err := src.Open(fmt.Sprintf("manifest-%s.json", mode))
	if err != nil {
		return rep, fmt.Errorf("fetch new manifest: %w", err)
	}
	defer rc.Close()
	newManPath := filepath.Join(datadir, fmt.Sprintf("manifest-%s.json", mode))
	tmp := newManPath + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return rep, err
	}
	if _, err := io.Copy(f, rc); err != nil {
		f.Close()
		os.Remove(tmp)
		return rep, err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return rep, err
	}
	if err := os.Rename(tmp, newManPath); err != nil {
		return rep, err
	}

	rep.OK = true
	return rep, nil
}

// DeltaApplyReport summarises a delta apply run.
type DeltaApplyReport struct {
	Plan       *DeltaPlan
	FromHeight uint64
	ToHeight   uint64
	Skipped    int
	Downloaded int
	Failed     int
	BytesXfer  int64
	Errors     []string
	OK         bool
}

// Print writes a human-readable report.
func (r *DeltaApplyReport) Print(w io.Writer) {
	fmt.Fprintf(w, "delta apply: mode=%s from=%d → to=%d\n",
		r.Plan.Mode, r.FromHeight, r.ToHeight)
	fmt.Fprintf(w, "  local baseline ok : %v\n", r.Plan.Applicable)
	fmt.Fprintf(w, "  files planned     : %d\n", r.Plan.FilesToFetch)
	fmt.Fprintf(w, "  files skipped     : %d\n", r.Skipped)
	fmt.Fprintf(w, "  files downloaded  : %d\n", r.Downloaded)
	fmt.Fprintf(w, "  files failed      : %d\n", r.Failed)
	fmt.Fprintf(w, "  bytes xferred     : %.2f GB\n", float64(r.BytesXfer)/1024/1024/1024)
	if len(r.Errors) > 0 {
		fmt.Fprintf(w, "  errors (%d):\n", len(r.Errors))
		for _, e := range r.Errors {
			fmt.Fprintf(w, "    %s\n", e)
		}
	}
	if r.OK {
		fmt.Fprintln(w, "  result            : OK (manifest installed)")
	} else {
		fmt.Fprintln(w, "  result            : FAIL")
	}
}

// Suppress unused import lint if we drop the errors helpers later.
var _ = errors.New
