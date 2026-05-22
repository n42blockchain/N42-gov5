// Package snapshot is the client-side library powering
// cmd/n42-eth-snapshot. Each function is a self-contained subcommand
// implementation so the CLI surface stays a thin dispatcher.
package snapshot

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"golang.org/x/crypto/blake2b"

	"github.com/n42blockchain/N42/cmd/n42-eth-manifest/manifest"
)

// ManifestFor reads the JSON manifest for a given mode under a datadir.
func ManifestFor(datadir, mode string) (*manifest.Manifest, error) {
	path := filepath.Join(datadir, fmt.Sprintf("manifest-%s.json", mode))
	return ReadManifest(path)
}

// ReadManifest parses any manifest JSON file from disk.
func ReadManifest(path string) (*manifest.Manifest, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var m manifest.Manifest
	if err := json.NewDecoder(f).Decode(&m); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	return &m, nil
}

// HashFile returns the blake2b-256 of a file at the given path.
// Mirrors the writer side in cmd/n42-eth-manifest/main.go.
func HashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h, err := blake2b.New256(nil)
	if err != nil {
		return "", err
	}
	buf := make([]byte, 4<<20)
	for {
		n, rerr := f.Read(buf)
		if n > 0 {
			h.Write(buf[:n])
		}
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				break
			}
			return "", rerr
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// hashAll hashes every entry in `files` in parallel, mutating
// each FileEntry.Blake2b256 in place. `root` is prepended to
// each entry.Path.
func hashAll(root string, files []*manifest.FileEntry, workers int) []error {
	if workers <= 0 {
		workers = runtime.NumCPU() / 2
		if workers < 1 {
			workers = 1
		}
	}
	type result struct {
		idx int
		err error
	}
	jobs := make(chan int, workers*4)
	results := make(chan result, workers*4)

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				h, err := HashFile(filepath.Join(root, files[idx].Path))
				if err == nil {
					files[idx].Blake2b256 = h
				}
				results <- result{idx, err}
			}
		}()
	}
	go func() {
		for i := range files {
			jobs <- i
		}
		close(jobs)
	}()

	errs := make([]error, len(files))
	go func() {
		wg.Wait()
		close(results)
	}()
	for r := range results {
		errs[r.idx] = r.err
	}
	return errs
}
