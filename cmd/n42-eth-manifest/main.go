// n42-eth-manifest walks a built n42-eth archive and emits a
// content-addressed manifest JSON for one of the three distribution
// modes (minimal / full / archive).
//
// The manifest is the contract between the publisher pipeline and
// the client snapshot CLI: each file's blake2b-256 lets the client
// fetch idempotently and verify atomically. See
// docs/ethel/n42-eth-client-distribution.md for the spec.
//
// Usage:
//
//	n42-eth-manifest --datadir /var/lib/n42 --mode archive
//	  → writes /var/lib/n42/manifest-archive.json
//
//	n42-eth-manifest --datadir /var/lib/n42 --mode full \
//	    --include-senders --network mainnet --height 25100000 \
//	    --out /tmp/manifest-full.json
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/blake2b"

	"github.com/n42blockchain/N42/cmd/n42-eth-manifest/manifest"
)

func main() {
	datadir := flag.String("datadir", ".", "archive root containing chain/ and snapshot/")
	mode := flag.String("mode", "archive", "one of: minimal, full, archive")
	out := flag.String("out", "", "output JSON path (default: <datadir>/manifest-<mode>.json)")
	network := flag.String("network", "mainnet", "network name (mainnet, sepolia, holesky, ...)")
	height := flag.Uint64("height", 0, "chain height; 0 = derive from headerc segments (TODO)")
	includeSenders := flag.Bool("include-senders", false, "include the optional senders pack (full + archive only)")
	parallel := flag.Int("parallel", 0, "concurrent hashers (0 = NumCPU/2)")
	dryRun := flag.Bool("dry-run", false, "list the files that would be hashed without computing hashes")
	bodiesWindow := flag.Int("bodies-window", 0, "full mode: how many newest bodyc segments to publish (0 = the built-in one-year default)")
	flag.Parse()

	if _, err := os.Stat(*datadir); err != nil {
		die("datadir %s not present: %v", *datadir, err)
	}

	cfg, err := manifest.SelectorForWithWindow(*mode, *bodiesWindow)
	if err != nil {
		die("%v", err)
	}
	if *includeSenders {
		cfg = manifest.WithSenders(cfg)
	}

	files, err := manifest.WalkFiles(*datadir, cfg)
	if err != nil {
		die("walk files: %v", err)
	}
	if len(files) == 0 {
		die("no files matched for mode=%s under %s — is this a built archive?", *mode, *datadir)
	}

	if *dryRun {
		fmt.Printf("dry-run: %d files matched for mode=%s\n", len(files), *mode)
		var total uint64
		for _, f := range files {
			fmt.Printf("  %-12d %s\n", f.Size, f.Path)
			total += uint64(f.Size)
		}
		fmt.Printf("\nTotal: %.2f GB\n", float64(total)/1024/1024/1024)
		return
	}

	workers := *parallel
	if workers <= 0 {
		workers = runtime.NumCPU() / 2
		if workers < 1 {
			workers = 1
		}
	}

	t0 := time.Now()
	if err := hashAll(*datadir, files, workers); err != nil {
		die("hash: %v", err)
	}

	man := &manifest.Manifest{
		Network:   *network,
		Height:    *height,
		Mode:      *mode,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		Files:     files,
	}
	// Top-level hash of the file list, so clients can detect tampering
	// of the manifest itself when distributed with a known anchor.
	man.ManifestID = computeManifestID(man)

	outPath := *out
	if outPath == "" {
		outPath = filepath.Join(*datadir, fmt.Sprintf("manifest-%s.json", *mode))
	}
	if err := writeJSON(outPath, man); err != nil {
		die("write %s: %v", outPath, err)
	}

	var totalBytes uint64
	for _, f := range files {
		totalBytes += uint64(f.Size)
	}
	fmt.Printf("wrote %s\n", outPath)
	fmt.Printf("  mode      : %s\n", *mode)
	fmt.Printf("  network   : %s\n", *network)
	fmt.Printf("  height    : %d\n", *height)
	fmt.Printf("  files     : %d\n", len(files))
	fmt.Printf("  total     : %.2f GB\n", float64(totalBytes)/1024/1024/1024)
	fmt.Printf("  elapsed   : %s\n", time.Since(t0).Truncate(time.Millisecond))
	fmt.Printf("  manifestID: %s\n", man.ManifestID)
}

// hashAll computes blake2b-256 for each file using a worker pool.
// File entries are mutated in place (Blake2b256 field).
func hashAll(root string, files []*manifest.FileEntry, workers int) error {
	type task struct{ idx int }
	jobs := make(chan task, workers*4)

	var (
		wg       sync.WaitGroup
		errOnce  sync.Once
		firstErr error
	)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for t := range jobs {
				h, herr := hashFile(filepath.Join(root, files[t.idx].Path))
				if herr != nil {
					errOnce.Do(func() { firstErr = herr })
					return
				}
				files[t.idx].Blake2b256 = h
			}
		}()
	}
	for i := range files {
		jobs <- task{idx: i}
	}
	close(jobs)
	wg.Wait()
	return firstErr
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h, err := blake2b.New256(nil)
	if err != nil {
		return "", err
	}
	buf := make([]byte, 4<<20) // 4 MiB
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

// computeManifestID returns the sha256 of the canonical sorted
// file-list representation. Cheap top-level fingerprint.
func computeManifestID(m *manifest.Manifest) string {
	sort.Slice(m.Files, func(i, j int) bool {
		return m.Files[i].Path < m.Files[j].Path
	})
	h := sha256.New()
	for _, f := range m.Files {
		fmt.Fprintf(h, "%s\t%d\t%s\n", f.Path, f.Size, f.Blake2b256)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func writeJSON(path string, v any) error {
	if !strings.HasSuffix(path, ".json") {
		return fmt.Errorf("output path must end in .json (got %s)", path)
	}
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
