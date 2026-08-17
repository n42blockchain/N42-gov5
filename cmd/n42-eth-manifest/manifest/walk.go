package manifest

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// WalkFiles resolves every Selector section's glob patterns against
// the datadir and returns the deduplicated, ordered file list.
// Returns an entry per matched file with Path (relative to root),
// Section (the logical group), and Size (from stat). Blake2b256
// is left empty for the caller to fill.
//
// Files are returned in deterministic order: section order from
// the selector, then alphabetical within each section. Same file
// matched by multiple sections is recorded once (first section
// wins).
func WalkFiles(root string, sel *Selector) ([]*FileEntry, error) {
	var (
		out  []*FileEntry
		seen = make(map[string]bool)
	)
	for _, sec := range sel.Sections {
		var matched []string
		for _, pat := range sec.Patterns {
			full := filepath.Join(root, filepath.FromSlash(pat))
			ms, err := filepath.Glob(full)
			if err != nil {
				return nil, fmt.Errorf("glob %q: %w", pat, err)
			}
			matched = append(matched, ms...)
		}
		sort.Strings(matched)
		// A windowed section publishes only the newest N data segments.
		// Freezer segment numbering is monotonic in block height, so the
		// window is simply the tail of the sorted list; index files
		// (everything that is not a .cdat) are always kept because the
		// reader needs the whole index and tolerates the resulting gap
		// (ErrBodyTrimmed + ColdResolver).
		if sec.WindowSegments > 0 {
			var idx, dat []string
			for _, m := range matched {
				if strings.HasSuffix(m, ".cdat") {
					dat = append(dat, m)
				} else {
					idx = append(idx, m)
				}
			}
			if len(dat) > sec.WindowSegments {
				dat = dat[len(dat)-sec.WindowSegments:]
			}
			matched = append(idx, dat...)
			sort.Strings(matched)
		}
		for _, m := range matched {
			rel, err := filepath.Rel(root, m)
			if err != nil {
				return nil, fmt.Errorf("rel %s: %w", m, err)
			}
			rel = filepath.ToSlash(rel)
			if seen[rel] {
				continue
			}
			fi, err := os.Stat(m)
			if err != nil {
				return nil, fmt.Errorf("stat %s: %w", m, err)
			}
			if fi.IsDir() {
				continue
			}
			seen[rel] = true
			out = append(out, &FileEntry{
				Path:    rel,
				Section: sec.Name,
				Size:    fi.Size(),
			})
		}
	}
	return out, nil
}

// SectionsPresent returns the names of selector sections that
// have at least one matched file under root. Useful for the CLI
// mode-detection ("we have headers + state but no bodies → minimal").
func SectionsPresent(root string, sel *Selector) ([]string, error) {
	files, err := WalkFiles(root, sel)
	if err != nil {
		return nil, err
	}
	present := make(map[string]struct{}, 8)
	for _, f := range files {
		present[f.Section] = struct{}{}
	}
	var out []string
	for _, sec := range sel.Sections {
		if _, ok := present[sec.Name]; ok {
			out = append(out, sec.Name)
		}
	}
	return out, nil
}

// MissingSections is the inverse: sections in the selector that have ZERO
// matched files. Optional sections are included for informational reporting.
func MissingSections(root string, sel *Selector) ([]string, error) {
	files, err := WalkFiles(root, sel)
	if err != nil {
		return nil, err
	}
	present := make(map[string]struct{}, 8)
	for _, f := range files {
		present[f.Section] = struct{}{}
	}
	var out []string
	for _, sec := range sel.Sections {
		if _, ok := present[sec.Name]; !ok {
			out = append(out, sec.Name)
		}
	}
	return out, nil
}

// RequiredMissingSections reports required sections with no matching files.
// Optional sections remain part of manifests and MissingSections reports, but
// their absence must not make a usable distribution tier undetectable.
func RequiredMissingSections(root string, sel *Selector) ([]string, error) {
	files, err := WalkFiles(root, sel)
	if err != nil {
		return nil, err
	}
	present := make(map[string]struct{}, 8)
	for _, f := range files {
		present[f.Section] = struct{}{}
	}
	var out []string
	for _, sec := range sel.Sections {
		if sec.Optional {
			continue
		}
		if _, ok := present[sec.Name]; !ok {
			out = append(out, sec.Name)
		}
	}
	return out, nil
}

// FormatSections is a tiny helper for the CLI to print "headers,
// code, state-accounts, state-storage".
func FormatSections(s []string) string { return strings.Join(s, ", ") }
