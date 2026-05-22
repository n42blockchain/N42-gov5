package snapshot

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"github.com/n42blockchain/N42/cmd/n42-eth-manifest/manifest"
)

// FetchReport summarises what fetch/upgrade did.
type FetchReport struct {
	Mode          string
	TotalFiles    int
	AlreadyOK     int
	Downloaded    int
	Failed        int
	BytesXfer     int64
	BytesSkipped  int64
	Errors        []string
	DryRun        bool
	OK            bool
}

// Fetch copies every file in the target manifest from `source` into
// `datadir`. A file that already exists with the correct size and
// blake2b is skipped. Each downloaded file is hash-verified before
// being moved into place (tmp + rename atomicity).
func Fetch(source, datadir, mode string, includeSenders, dryRun bool, parallel int) (*FetchReport, error) {
	src, err := OpenSource(source)
	if err != nil {
		return nil, err
	}
	manRel := fmt.Sprintf("manifest-%s.json", mode)

	if err := os.MkdirAll(datadir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir datadir: %w", err)
	}
	// Always (re-)fetch the manifest itself first.
	if err := fetchOneFile(src, manRel, filepath.Join(datadir, manRel)); err != nil {
		return nil, fmt.Errorf("fetch manifest %s: %w", manRel, err)
	}

	m, err := ManifestFor(datadir, mode)
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}

	want := m.Files
	if includeSenders {
		// Pull the optional senders manifest entries from a sibling
		// manifest-senders.json if the publisher provides one; this
		// is a future-friendly stub — for now we just look for
		// senders files matching the selector pattern in the same
		// manifest.
		want = append(want, sendersFiles(m)...)
	}

	report := &FetchReport{
		Mode:       mode,
		TotalFiles: len(want),
		DryRun:     dryRun,
		OK:         true,
	}

	// Plan: split into skip vs fetch.
	type plan struct {
		entry  *manifest.FileEntry
		action string // "skip" | "fetch"
	}
	plans := make([]plan, 0, len(want))
	for _, f := range want {
		full := filepath.Join(datadir, f.Path)
		st, serr := os.Stat(full)
		if serr == nil && st.Size() == f.Size {
			got, herr := HashFile(full)
			if herr == nil && got == f.Blake2b256 {
				plans = append(plans, plan{f, "skip"})
				report.AlreadyOK++
				report.BytesSkipped += f.Size
				continue
			}
		}
		plans = append(plans, plan{f, "fetch"})
	}

	if dryRun {
		return report, nil
	}

	if parallel <= 0 {
		parallel = 4
	}
	type job struct{ p plan }
	jobs := make(chan job, parallel*2)
	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		bytes  int64
		errs   []string
		failed int
		done   int
	)
	for w := 0; w < parallel; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				if j.p.action != "fetch" {
					continue
				}
				dst := filepath.Join(datadir, j.p.entry.Path)
				size, err := fetchAndVerify(src, j.p.entry.Path, dst, j.p.entry.Blake2b256, j.p.entry.Size)
				mu.Lock()
				if err != nil {
					failed++
					errs = append(errs, fmt.Sprintf("%s: %v", j.p.entry.Path, err))
				} else {
					done++
					bytes += size
				}
				mu.Unlock()
			}
		}()
	}
	for _, p := range plans {
		jobs <- job{p}
	}
	close(jobs)
	wg.Wait()

	report.Downloaded = done
	report.Failed = failed
	report.BytesXfer = bytes
	report.Errors = errs
	if failed > 0 {
		report.OK = false
	}
	return report, nil
}

// sendersFiles returns any entries in m whose section starts with
// "senders". Publisher implementations either bundle these in the
// main manifest or ship a separate manifest-senders.json — we
// support the bundled case for now.
func sendersFiles(m *manifest.Manifest) []*manifest.FileEntry {
	var out []*manifest.FileEntry
	for _, f := range m.Files {
		if f.Section == "senders" {
			out = append(out, f)
		}
	}
	return out
}

// fetchOneFile copies source→dst without any verification. Used
// only to bootstrap the manifest itself.
func fetchOneFile(src Source, relPath, dst string) error {
	rc, _, err := src.Open(relPath)
	if err != nil {
		return err
	}
	defer rc.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp := dst + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, rc); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

// fetchAndVerify downloads relPath from src to dst, hashes the
// downloaded file, and only moves it into place if the hash
// matches wantHash. Returns bytes transferred.
func fetchAndVerify(src Source, relPath, dst, wantHash string, wantSize int64) (int64, error) {
	rc, _, err := src.Open(relPath)
	if err != nil {
		return 0, err
	}
	defer rc.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return 0, err
	}
	tmp := dst + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(f, rc)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return 0, err
	}
	if wantSize > 0 && n != wantSize {
		os.Remove(tmp)
		return 0, fmt.Errorf("size mismatch: got %d, want %d", n, wantSize)
	}
	got, err := HashFile(tmp)
	if err != nil {
		os.Remove(tmp)
		return 0, err
	}
	if got != wantHash {
		os.Remove(tmp)
		return 0, errors.New("blake2b mismatch after download")
	}
	if err := os.Rename(tmp, dst); err != nil {
		return 0, err
	}
	return n, nil
}

// Upgrade is a thin wrapper around Fetch with the same semantics:
// fetches whatever is missing for the target mode, leaves existing
// (higher-tier) files in place.
func Upgrade(source, datadir, to string, includeSenders bool, parallel int) (*FetchReport, error) {
	return Fetch(source, datadir, to, includeSenders, false, parallel)
}

// DowngradeReport — files removed (or to-be-removed in dry-run mode).
type DowngradeReport struct {
	Mode        string
	Removed     []string
	BytesFreed  int64
	DryRun      bool
}

// Downgrade walks the datadir for files that the target mode's
// manifest does NOT reference and removes them (if !dry-run).
// Only files under chain/freezer/ and snapshot/ are considered;
// arbitrary user files are never touched.
func Downgrade(datadir, to string, doDelete bool) (*DowngradeReport, error) {
	want, err := ManifestFor(datadir, to)
	if err != nil {
		return nil, fmt.Errorf("read target manifest: %w", err)
	}
	keep := make(map[string]struct{}, len(want.Files))
	for _, f := range want.Files {
		keep[f.Path] = struct{}{}
	}

	rep := &DowngradeReport{Mode: to, DryRun: !doDelete}
	walkRoots := []string{"chain/freezer", "snapshot"}
	for _, sub := range walkRoots {
		root := filepath.Join(datadir, filepath.FromSlash(sub))
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, werr error) error {
			if werr != nil {
				return nil
			}
			if d.IsDir() {
				return nil
			}
			rel, _ := filepath.Rel(datadir, path)
			rel = filepath.ToSlash(rel)
			if _, ok := keep[rel]; ok {
				return nil
			}
			st, _ := d.Info()
			if st != nil {
				rep.BytesFreed += st.Size()
			}
			rep.Removed = append(rep.Removed, rel)
			if doDelete {
				_ = os.Remove(path)
			}
			return nil
		})
	}
	return rep, nil
}

// Print writes a human-readable report.
func (r *FetchReport) Print(w io.Writer) {
	prefix := ""
	if r.DryRun {
		prefix = "(dry-run) "
	}
	fmt.Fprintf(w, "%smode=%s  files=%d  already-ok=%d  downloaded=%d  failed=%d\n",
		prefix, r.Mode, r.TotalFiles, r.AlreadyOK, r.Downloaded, r.Failed)
	fmt.Fprintf(w, "  bytes xferred : %.2f GB\n", float64(r.BytesXfer)/1024/1024/1024)
	fmt.Fprintf(w, "  bytes skipped : %.2f GB\n", float64(r.BytesSkipped)/1024/1024/1024)
	if len(r.Errors) > 0 {
		fmt.Fprintf(w, "  errors (%d):\n", len(r.Errors))
		for _, e := range r.Errors {
			fmt.Fprintf(w, "    %s\n", e)
		}
	}
	if r.OK {
		fmt.Fprintln(w, "  result        : OK")
	} else {
		fmt.Fprintln(w, "  result        : FAIL")
	}
}

func (r *DowngradeReport) Print(w io.Writer) {
	prefix := ""
	if r.DryRun {
		prefix = "(dry-run) "
	}
	fmt.Fprintf(w, "%sdowngrade target=%s — %d files to remove, %.2f GB to free\n",
		prefix, r.Mode, len(r.Removed), float64(r.BytesFreed)/1024/1024/1024)
	for _, p := range r.Removed {
		fmt.Fprintf(w, "  %s\n", p)
	}
}
