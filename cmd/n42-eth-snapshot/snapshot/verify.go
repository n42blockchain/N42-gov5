package snapshot

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// VerifyReport is the outcome of a Verify call.
type VerifyReport struct {
	ManifestPath  string
	Mode          string
	Height        uint64
	FilesChecked  int
	Mismatches    []string // path + reason
	MissingFiles  []string // path
	WrongSize     []string // path + sizes
	OK            bool
}

// Verify walks the manifest's files, re-hashes each, and reports
// any mismatch / missing file / size discrepancy. If manifestPath
// is empty, the maximal-mode manifest under datadir is auto-detected.
func Verify(datadir, manifestPath string, workers int) (*VerifyReport, error) {
	if manifestPath == "" {
		det, err := DetectMode(datadir)
		if err != nil {
			return nil, err
		}
		if det.Mode == "" {
			return nil, errors.New("verify: no manifest found and no mode detected in datadir")
		}
		manifestPath = filepath.Join(datadir, fmt.Sprintf("manifest-%s.json", det.Mode))
	}
	m, err := ReadManifest(manifestPath)
	if err != nil {
		return nil, err
	}
	rep := &VerifyReport{
		ManifestPath: manifestPath,
		Mode:         m.Mode,
		Height:       m.Height,
		FilesChecked: len(m.Files),
		OK:           true,
	}

	// Stat pass: catch missing files and wrong sizes before hashing.
	toHash := make([]*manifestFileExpect, 0, len(m.Files))
	for _, f := range m.Files {
		full := filepath.Join(datadir, f.Path)
		st, err := os.Stat(full)
		if err != nil {
			rep.MissingFiles = append(rep.MissingFiles, f.Path)
			rep.OK = false
			continue
		}
		if st.Size() != f.Size {
			rep.WrongSize = append(rep.WrongSize,
				fmt.Sprintf("%s (got %d, want %d)", f.Path, st.Size(), f.Size))
			rep.OK = false
			continue
		}
		toHash = append(toHash, &manifestFileExpect{path: f.Path, want: f.Blake2b256})
	}

	// Hash pass: compute blake2b for each and compare.
	hashFiles := make([]string, len(toHash))
	wantHashes := make([]string, len(toHash))
	for i, e := range toHash {
		hashFiles[i] = e.path
		wantHashes[i] = e.want
	}
	gotHashes, errs := hashListParallel(datadir, hashFiles, workers)
	for i, got := range gotHashes {
		if errs[i] != nil {
			rep.Mismatches = append(rep.Mismatches,
				fmt.Sprintf("%s — hash error: %v", hashFiles[i], errs[i]))
			rep.OK = false
			continue
		}
		if got != wantHashes[i] {
			rep.Mismatches = append(rep.Mismatches,
				fmt.Sprintf("%s — blake2b mismatch", hashFiles[i]))
			rep.OK = false
		}
	}
	return rep, nil
}

type manifestFileExpect struct {
	path string
	want string
}

// hashListParallel: identical to hashAll but returns hashes separately
// without mutating any manifest struct.
func hashListParallel(root string, paths []string, workers int) ([]string, []error) {
	out := make([]string, len(paths))
	errs := make([]error, len(paths))
	if workers <= 0 {
		workers = 4
	}
	type job struct{ idx int }
	jobs := make(chan job, workers*4)
	done := make(chan int, workers*4)
	for w := 0; w < workers; w++ {
		go func() {
			for j := range jobs {
				h, err := HashFile(filepath.Join(root, paths[j.idx]))
				out[j.idx] = h
				errs[j.idx] = err
				done <- 1
			}
		}()
	}
	go func() {
		for i := range paths {
			jobs <- job{i}
		}
		close(jobs)
	}()
	for i := 0; i < len(paths); i++ {
		<-done
	}
	return out, errs
}

// Print writes a human-readable report.
func (r *VerifyReport) Print(w io.Writer) {
	fmt.Fprintf(w, "manifest : %s\n", r.ManifestPath)
	fmt.Fprintf(w, "mode     : %s\n", r.Mode)
	fmt.Fprintf(w, "height   : %d\n", r.Height)
	fmt.Fprintf(w, "files    : %d\n", r.FilesChecked)
	if len(r.MissingFiles) > 0 {
		fmt.Fprintf(w, "\nMISSING (%d):\n", len(r.MissingFiles))
		for _, p := range r.MissingFiles {
			fmt.Fprintf(w, "  %s\n", p)
		}
	}
	if len(r.WrongSize) > 0 {
		fmt.Fprintf(w, "\nWRONG SIZE (%d):\n", len(r.WrongSize))
		for _, p := range r.WrongSize {
			fmt.Fprintf(w, "  %s\n", p)
		}
	}
	if len(r.Mismatches) > 0 {
		fmt.Fprintf(w, "\nHASH MISMATCH (%d):\n", len(r.Mismatches))
		for _, p := range r.Mismatches {
			fmt.Fprintf(w, "  %s\n", p)
		}
	}
	if r.OK {
		fmt.Fprintln(w, "\nresult   : OK")
	} else {
		fmt.Fprintln(w, "\nresult   : FAIL")
	}
}
