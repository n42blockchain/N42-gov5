package snapshot

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/n42blockchain/N42/cmd/n42-eth-manifest/manifest"
)

// writeFakeManifest invokes the same file selection + hashing logic
// as cmd/n42-eth-manifest, but inline so tests don't shell out.
// Writes manifest-<mode>.json into dir.
func writeFakeManifest(t *testing.T, dir, mode string) error {
	t.Helper()
	sel, err := manifest.SelectorFor(mode)
	if err != nil {
		return err
	}
	files, err := manifest.WalkFiles(dir, sel)
	if err != nil {
		return err
	}
	for _, f := range files {
		h, err := HashFile(filepath.Join(dir, f.Path))
		if err != nil {
			return err
		}
		f.Blake2b256 = h
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })

	h := sha256.New()
	for _, f := range files {
		fmt.Fprintf(h, "%s\t%d\t%s\n", f.Path, f.Size, f.Blake2b256)
	}
	man := manifest.Manifest{
		Network:    "test",
		Height:     1,
		Mode:       mode,
		ManifestID: hex.EncodeToString(h.Sum(nil)),
		Files:      files,
	}
	out := filepath.Join(dir, fmt.Sprintf("manifest-%s.json", mode))
	f, err := os.Create(out)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(&man)
}
