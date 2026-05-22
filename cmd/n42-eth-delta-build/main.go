// n42-eth-delta-build produces an incremental snapshot delta
// between two release manifests. Given a baseline (from) and a
// target (to), it copies (or hardlinks) only the files whose
// blake2b changed (or are net-new) into a destination tree along
// with a delta-manifest pointing at the baseline by manifest_id.
//
// Spec: docs/ethel/n42-eth-delta-updates.md
//
// Usage:
//
//	n42-eth-delta-build \
//	  --from-archive /publish/25100000 \
//	  --to-archive   /publish/25200000 \
//	  --mode archive \
//	  --out /publish/delta-25100000-25200000 \
//	  --hardlink           # default: copy. Use hardlink on same FS for speed
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
	"sort"
	"time"

	"github.com/n42blockchain/N42/cmd/n42-eth-manifest/manifest"
	"github.com/n42blockchain/N42/cmd/n42-eth-snapshot/snapshot"
)

func main() {
	fromArchive := flag.String("from-archive", "", "baseline archive datadir containing manifest-<mode>.json")
	toArchive := flag.String("to-archive", "", "target archive datadir containing manifest-<mode>.json")
	mode := flag.String("mode", "archive", "mode (minimal|full|archive)")
	out := flag.String("out", "", "output delta tree dir")
	hardlink := flag.Bool("hardlink", false, "hardlink instead of copying (same-FS only)")
	dryRun := flag.Bool("dry-run", false, "list the delta files without writing")
	flag.Parse()

	if *fromArchive == "" || *toArchive == "" || *out == "" {
		die("--from-archive, --to-archive, and --out are required")
	}

	fromMan, err := snapshot.ManifestFor(*fromArchive, *mode)
	if err != nil {
		die("read from manifest: %v", err)
	}
	toMan, err := snapshot.ManifestFor(*toArchive, *mode)
	if err != nil {
		die("read to manifest: %v", err)
	}
	if fromMan.Mode != toMan.Mode {
		die("mode mismatch: from=%s to=%s", fromMan.Mode, toMan.Mode)
	}

	fromIndex := make(map[string]string, len(fromMan.Files))
	for _, f := range fromMan.Files {
		fromIndex[f.Path] = f.Blake2b256
	}

	var deltaFiles []*manifest.FileEntry
	var totalBytes int64
	for _, f := range toMan.Files {
		prevHash, existed := fromIndex[f.Path]
		if existed && prevHash == f.Blake2b256 {
			continue // unchanged
		}
		// Either new (!existed) or mutated (different hash).
		deltaFiles = append(deltaFiles, f)
		totalBytes += f.Size
	}

	delta := &deltaManifest{
		Network:            toMan.Network,
		FromHeight:         fromMan.Height,
		ToHeight:           toMan.Height,
		Mode:               toMan.Mode,
		BasedOnManifestID:  fromMan.ManifestID,
		CreatedAt:          time.Now().UTC().Format(time.RFC3339),
		Files:              deltaFiles,
	}
	// Compute delta manifest_id deterministically.
	sort.Slice(delta.Files, func(i, j int) bool { return delta.Files[i].Path < delta.Files[j].Path })
	h := sha256.New()
	for _, f := range delta.Files {
		fmt.Fprintf(h, "%s\t%d\t%s\n", f.Path, f.Size, f.Blake2b256)
	}
	delta.ManifestID = hex.EncodeToString(h.Sum(nil))

	fmt.Printf("delta %s → %s (mode=%s)\n", deriveHeightLabel(fromMan), deriveHeightLabel(toMan), *mode)
	fmt.Printf("  baseline manifest_id : %s\n", fromMan.ManifestID)
	fmt.Printf("  changed/new files    : %d / %d\n", len(deltaFiles), len(toMan.Files))
	fmt.Printf("  bytes in delta       : %.2f GB\n", float64(totalBytes)/1024/1024/1024)
	fmt.Printf("  baseline total       : %.2f GB\n", float64(sumSizes(fromMan.Files))/1024/1024/1024)
	fmt.Printf("  delta as %% of full   : %5.1f %%\n",
		100*float64(totalBytes)/float64(sumSizes(toMan.Files)))

	if *dryRun {
		fmt.Println("\n(dry-run; nothing written)")
		return
	}

	if err := os.MkdirAll(*out, 0o755); err != nil {
		die("mkdir out: %v", err)
	}
	t0 := time.Now()
	for _, f := range deltaFiles {
		src := filepath.Join(*toArchive, filepath.FromSlash(f.Path))
		dst := filepath.Join(*out, filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			die("mkdir %s: %v", filepath.Dir(dst), err)
		}
		if *hardlink {
			if err := os.Link(src, dst); err != nil {
				// Fall back to copy if hardlink fails (cross-FS).
				if err := copyFile(src, dst); err != nil {
					die("copy %s: %v", f.Path, err)
				}
			}
		} else {
			if err := copyFile(src, dst); err != nil {
				die("copy %s: %v", f.Path, err)
			}
		}
	}
	// Write delta manifest at the root.
	deltaPath := filepath.Join(*out, fmt.Sprintf("delta-manifest-%s.json", *mode))
	if err := writeJSON(deltaPath, delta); err != nil {
		die("write delta manifest: %v", err)
	}
	fmt.Printf("\nwrote %d files + %s in %s\n", len(deltaFiles), deltaPath,
		time.Since(t0).Truncate(time.Millisecond))
}

// deltaManifest extends the standard Manifest with the baseline
// pointer fields. Encoded as a sibling type so we don't pollute
// the shared Manifest struct.
type deltaManifest struct {
	Network           string                 `json:"network"`
	FromHeight        uint64                 `json:"from_height"`
	ToHeight          uint64                 `json:"to_height"`
	Mode              string                 `json:"mode"`
	BasedOnManifestID string                 `json:"based_on_manifest_id"`
	CreatedAt         string                 `json:"created_at"`
	ManifestID        string                 `json:"manifest_id"`
	Files             []*manifest.FileEntry  `json:"files"`
}

func sumSizes(files []*manifest.FileEntry) int64 {
	var s int64
	for _, f := range files {
		s += f.Size
	}
	return s
}

func deriveHeightLabel(m *manifest.Manifest) string {
	if m.Height > 0 {
		return fmt.Sprintf("%d", m.Height)
	}
	return "?"
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

func writeJSON(path string, v any) error {
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

// Suppress unused import lint if errors is removed later.
var _ = errors.New
