package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Resolve existing ancestors too: an output directory may not exist yet,
// while one of its parents may be a symlink into the input database.
func canonicalExportPath(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("export path is empty")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	probe := absolute
	var suffix []string
	for {
		resolved, err := filepath.EvalSymlinks(probe)
		if err == nil {
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return resolved, nil
		}
		if !os.IsNotExist(err) || filepath.Dir(probe) == probe {
			return "", err
		}
		suffix = append(suffix, filepath.Base(probe))
		probe = filepath.Dir(probe)
	}
}

func validateExportPaths(dbPath string, outputs ...string) error {
	database, err := canonicalExportPath(dbPath)
	if err != nil {
		return err
	}
	seen := make(map[string]bool)
	for _, output := range outputs {
		path, err := canonicalExportPath(output)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(database, path)
		if err != nil {
			return err
		}
		if relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return fmt.Errorf("export output %q is inside the source database", output)
		}
		if seen[path] {
			return fmt.Errorf("export outputs alias %q", output)
		}
		seen[path] = true
	}
	return nil
}
