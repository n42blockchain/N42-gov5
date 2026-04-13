// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.

// standardize-headers replaces the N42 copyright header in .go files with
// a normalized 2-line short form: "Copyright 2021-2026 The N42 Authors" +
// "This file is part of the N42 library.". Files whose first line is not
// an N42 copyright comment are skipped (third-party headers preserved).
package main

import (
	"bytes"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
)

var (
	standardHeader = []byte("// Copyright 2021-2026 The N42 Authors\n// This file is part of the N42 library.\n")
	copyPrefix     = []byte("// Copyright")
	// Author tags that are NEVER replaced. Go stdlib code stays as-is.
	preserveTags = [][]byte{
		[]byte("The Go Authors"),
	}
	// Markers identifying generated files. Generated headers stay as-is.
	generatedMarker = []byte("DO NOT EDIT")
)

func main() {
	root := flag.String("root", ".", "directory to scan")
	dryRun := flag.Bool("dry-run", false, "report what would change without modifying")
	workers := flag.Int("workers", 16, "concurrent workers")
	flag.Parse()

	// Walk and collect candidate files.
	var files []string
	err := filepath.WalkDir(*root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		// Skip noise dirs.
		if d.IsDir() {
			name := d.Name()
			if name == ".git" || name == "vendor" || name == ".claude" || name == "node_modules" || name == "build" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "walk:", err)
		os.Exit(1)
	}
	fmt.Printf("Scanning %d .go files...\n", len(files))

	var (
		processed atomic.Int64
		modified  atomic.Int64
		skipped   atomic.Int64
		errs      atomic.Int64
	)

	jobs := make(chan string, *workers*2)
	var wg sync.WaitGroup
	for i := 0; i < *workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for path := range jobs {
				processed.Add(1)
				ok, err := processFile(path, *dryRun)
				if err != nil {
					errs.Add(1)
					fmt.Fprintln(os.Stderr, "err:", path, err)
					continue
				}
				if ok {
					modified.Add(1)
				} else {
					skipped.Add(1)
				}
			}
		}()
	}
	for _, f := range files {
		jobs <- f
	}
	close(jobs)
	wg.Wait()

	fmt.Printf("\nDone. processed=%d modified=%d skipped=%d errors=%d\n",
		processed.Load(), modified.Load(), skipped.Load(), errs.Load())
}

// processFile rewrites the leading N42 copyright header into the standard
// short form. Returns (true, nil) if the file was changed (or would change
// in dry-run), (false, nil) if no change is needed, or an error.
func processFile(path string, dryRun bool) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	if len(data) == 0 {
		return false, nil
	}

	// Skip protobuf-generated files (.pb.go).
	if strings.HasSuffix(path, ".pb.go") {
		return false, nil
	}

	// File must start with "// Copyright".
	firstLineEnd := bytes.IndexByte(data, '\n')
	if firstLineEnd < 0 {
		return false, nil
	}
	firstLine := data[:firstLineEnd]
	if !bytes.HasPrefix(firstLine, copyPrefix) {
		return false, nil
	}
	// Skip preserved authors (Go stdlib).
	for _, tag := range preserveTags {
		if bytes.Contains(firstLine, tag) {
			return false, nil
		}
	}
	// Skip generated files. Look for "DO NOT EDIT" anywhere in the first ~3KB.
	scanLen := len(data)
	if scanLen > 3072 {
		scanLen = 3072
	}
	if bytes.Contains(data[:scanLen], generatedMarker) {
		return false, nil
	}

	// Find the end of the leading comment block. The header is the run of
	// lines starting with "//" from line 1, optionally followed by exactly
	// one blank line. Anything past that is body content (untouched).
	pos := 0
	for pos < len(data) {
		lineEnd := bytes.IndexByte(data[pos:], '\n')
		if lineEnd < 0 {
			// EOF mid-line.
			lineEnd = len(data) - pos
		}
		line := data[pos : pos+lineEnd]
		// Strip trailing CR (Windows line endings).
		stripped := line
		if len(stripped) > 0 && stripped[len(stripped)-1] == '\r' {
			stripped = stripped[:len(stripped)-1]
		}
		if !bytes.HasPrefix(stripped, []byte("//")) {
			// Hit a non-comment line — header ended on previous line.
			break
		}
		pos += lineEnd + 1 // include newline
	}
	// pos now points to the first non-comment byte (could be a blank line).
	// Skip exactly one trailing blank line if present (separator before package).
	if pos < len(data) {
		lineEnd := bytes.IndexByte(data[pos:], '\n')
		if lineEnd >= 0 {
			line := data[pos : pos+lineEnd]
			if len(line) > 0 && line[len(line)-1] == '\r' {
				line = line[:len(line)-1]
			}
			if len(line) == 0 {
				pos += lineEnd + 1
			}
		}
	}

	// Compose new file: standard header + blank line + rest.
	rest := data[pos:]
	var out bytes.Buffer
	out.Grow(len(standardHeader) + 1 + len(rest))
	out.Write(standardHeader)
	out.WriteByte('\n')
	out.Write(rest)

	if bytes.Equal(out.Bytes(), data) {
		return false, nil
	}
	if dryRun {
		return true, nil
	}
	return true, os.WriteFile(path, out.Bytes(), 0644)
}
