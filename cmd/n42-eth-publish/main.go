// n42-eth-publish manages a publication root for n42-eth-snapshot
// releases. Three subcommands:
//
//	release   publish a full archive at <published>/<network>/<height>/<mode>/
//	delta     publish a delta tree    at <published>/<network>/deltas/<from>-<to>/<mode>/
//	prune     enforce retention: keep last N releases + K deltas, GC the rest
//
// Maintains a top-level <published>/<network>/releases.json index
// so clients can list available heights / deltas without crawling.
//
// Spec: docs/ethel/n42-eth-delta-updates.md
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/n42blockchain/N42/cmd/n42-eth-snapshot/snapshot"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "release":
		runRelease(os.Args[2:])
	case "delta":
		runPublishDelta(os.Args[2:])
	case "prune":
		runPrune(os.Args[2:])
	case "list":
		runList(os.Args[2:])
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `n42-eth-publish — publication tooling for the n42-eth snapshot tree

USAGE
    n42-eth-publish <subcommand> [flags]

SUBCOMMANDS
    release  copy a full archive into the published tree
    delta    copy a delta tree into the published tree
    prune    enforce retention: keep last N releases + K deltas
    list     show what's in the publication root

PUBLICATION LAYOUT
    <root>/<network>/
      <height>/<mode>/...           # full release
      deltas/<from>-<to>/<mode>/... # incremental delta
      releases.json                 # index of releases + deltas`)
}

// PublishedIndex is the schema for <root>/<network>/releases.json.
type PublishedIndex struct {
	Network  string                  `json:"network"`
	Updated  string                  `json:"updated_at"`
	Latest   map[string]*ReleaseRef  `json:"latest"`   // mode → latest release ptr
	Releases []*ReleaseRef           `json:"releases"`
	Deltas   []*DeltaRef             `json:"deltas"`
}

type ReleaseRef struct {
	Height     uint64            `json:"height"`
	CreatedAt  string            `json:"created_at"`
	Manifests  map[string]string `json:"manifests"` // mode → manifest_id
}

type DeltaRef struct {
	FromHeight uint64 `json:"from_height"`
	ToHeight   uint64 `json:"to_height"`
	Mode       string `json:"mode"`
	ManifestID string `json:"manifest_id"`
	CreatedAt  string `json:"created_at"`
}

// ----- subcommands -----

func runRelease(args []string) {
	fs := flag.NewFlagSet("release", flag.ExitOnError)
	src := fs.String("src", "", "source archive datadir (contains manifest-<mode>.json)")
	root := fs.String("root", "", "publication root")
	network := fs.String("network", "mainnet", "network name")
	mode := fs.String("mode", "archive", "mode (minimal|full|archive)")
	hardlink := fs.Bool("hardlink", false, "hardlink instead of copy")
	_ = fs.Parse(args)
	if *src == "" || *root == "" {
		die("--src and --root are required")
	}
	man, err := snapshot.ManifestFor(*src, *mode)
	if err != nil {
		die("read source manifest: %v", err)
	}
	if man.Height == 0 {
		die("source manifest has no height; cannot publish without one")
	}

	dstDir := filepath.Join(*root, *network, fmt.Sprintf("%d", man.Height), *mode)
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		die("mkdir %s: %v", dstDir, err)
	}

	// Copy each file in the manifest plus the manifest itself.
	t0 := time.Now()
	var bytes int64
	for _, f := range man.Files {
		srcFull := filepath.Join(*src, f.Path)
		dstFull := filepath.Join(dstDir, f.Path)
		if err := transferFile(srcFull, dstFull, *hardlink); err != nil {
			die("transfer %s: %v", f.Path, err)
		}
		bytes += f.Size
	}
	manRel := fmt.Sprintf("manifest-%s.json", *mode)
	if err := transferFile(filepath.Join(*src, manRel), filepath.Join(dstDir, manRel), false); err != nil {
		die("publish manifest: %v", err)
	}
	updateIndex(*root, *network, func(idx *PublishedIndex) {
		setRelease(idx, *mode, man.Height, man.ManifestID, man.CreatedAt)
	})
	fmt.Printf("published release %s height=%d mode=%s files=%d bytes=%.2f GB elapsed=%s\n",
		*network, man.Height, *mode, len(man.Files),
		float64(bytes)/1024/1024/1024, time.Since(t0).Truncate(time.Millisecond))
}

func runPublishDelta(args []string) {
	fs := flag.NewFlagSet("delta", flag.ExitOnError)
	src := fs.String("src", "", "source delta tree (contains delta-manifest-<mode>.json)")
	root := fs.String("root", "", "publication root")
	network := fs.String("network", "mainnet", "network name")
	mode := fs.String("mode", "archive", "mode (minimal|full|archive)")
	hardlink := fs.Bool("hardlink", false, "hardlink instead of copy")
	_ = fs.Parse(args)
	if *src == "" || *root == "" {
		die("--src and --root are required")
	}
	deltaRel := fmt.Sprintf("delta-manifest-%s.json", *mode)
	f, err := os.Open(filepath.Join(*src, deltaRel))
	if err != nil {
		die("read %s: %v", deltaRel, err)
	}
	defer f.Close()
	var dm snapshot.DeltaManifest
	if err := json.NewDecoder(f).Decode(&dm); err != nil {
		die("decode %s: %v", deltaRel, err)
	}

	dstDir := filepath.Join(*root, *network, "deltas",
		fmt.Sprintf("%d-%d", dm.FromHeight, dm.ToHeight), *mode)
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		die("mkdir %s: %v", dstDir, err)
	}

	t0 := time.Now()
	var bytes int64
	for _, f := range dm.Files {
		srcFull := filepath.Join(*src, f.Path)
		dstFull := filepath.Join(dstDir, f.Path)
		if err := transferFile(srcFull, dstFull, *hardlink); err != nil {
			die("transfer %s: %v", f.Path, err)
		}
		bytes += f.Size
	}
	if err := transferFile(filepath.Join(*src, deltaRel), filepath.Join(dstDir, deltaRel), false); err != nil {
		die("publish delta-manifest: %v", err)
	}
	updateIndex(*root, *network, func(idx *PublishedIndex) {
		appendDelta(idx, *mode, dm.FromHeight, dm.ToHeight, dm.ManifestID, dm.CreatedAt)
	})
	fmt.Printf("published delta %d→%d mode=%s files=%d bytes=%.2f GB elapsed=%s\n",
		dm.FromHeight, dm.ToHeight, *mode, len(dm.Files),
		float64(bytes)/1024/1024/1024, time.Since(t0).Truncate(time.Millisecond))
}

func runPrune(args []string) {
	fs := flag.NewFlagSet("prune", flag.ExitOnError)
	root := fs.String("root", "", "publication root")
	network := fs.String("network", "mainnet", "network name")
	keepReleases := fs.Int("keep-releases", 4, "keep last N releases per mode")
	keepDeltas := fs.Int("keep-deltas", 8, "keep last K deltas per mode")
	dryRun := fs.Bool("dry-run", true, "default: report what would be removed without deleting")
	_ = fs.Parse(args)
	if *root == "" {
		die("--root is required")
	}
	idx, err := readIndex(*root, *network)
	if err != nil {
		die("read index: %v", err)
	}

	// Sort each per-mode release list by height desc; keep top N.
	keepRelease := make(map[string]map[uint64]struct{}, 3)
	for _, mode := range []string{"minimal", "full", "archive"} {
		var heights []uint64
		for _, r := range idx.Releases {
			if _, ok := r.Manifests[mode]; ok {
				heights = append(heights, r.Height)
			}
		}
		sort.Slice(heights, func(i, j int) bool { return heights[i] > heights[j] })
		set := make(map[uint64]struct{})
		for i := 0; i < *keepReleases && i < len(heights); i++ {
			set[heights[i]] = struct{}{}
		}
		keepRelease[mode] = set
	}

	keepDelta := make(map[string]map[string]struct{}, 3)
	for _, mode := range []string{"minimal", "full", "archive"} {
		var deltas []*DeltaRef
		for _, d := range idx.Deltas {
			if d.Mode == mode {
				deltas = append(deltas, d)
			}
		}
		sort.Slice(deltas, func(i, j int) bool { return deltas[i].ToHeight > deltas[j].ToHeight })
		set := make(map[string]struct{})
		for i := 0; i < *keepDeltas && i < len(deltas); i++ {
			set[fmt.Sprintf("%d-%d", deltas[i].FromHeight, deltas[i].ToHeight)] = struct{}{}
		}
		keepDelta[mode] = set
	}

	var (
		removedReleases []string
		removedDeltas   []string
		bytesFreed      int64
	)
	// Find dirs on disk to remove.
	netRoot := filepath.Join(*root, *network)
	entries, _ := os.ReadDir(netRoot)
	for _, e := range entries {
		if !e.IsDir() || e.Name() == "deltas" {
			continue
		}
		h := parseUint(e.Name())
		if h == 0 {
			continue
		}
		// Per-mode subdir.
		modes, _ := os.ReadDir(filepath.Join(netRoot, e.Name()))
		for _, md := range modes {
			if !md.IsDir() {
				continue
			}
			mode := md.Name()
			keep := keepRelease[mode]
			if keep == nil {
				continue
			}
			if _, ok := keep[h]; ok {
				continue
			}
			p := filepath.Join(netRoot, e.Name(), mode)
			sz := dirSize(p)
			removedReleases = append(removedReleases, p)
			bytesFreed += sz
			if !*dryRun {
				_ = os.RemoveAll(p)
			}
		}
	}
	deltaRoot := filepath.Join(netRoot, "deltas")
	deltaEntries, _ := os.ReadDir(deltaRoot)
	for _, d := range deltaEntries {
		if !d.IsDir() {
			continue
		}
		modes, _ := os.ReadDir(filepath.Join(deltaRoot, d.Name()))
		for _, md := range modes {
			if !md.IsDir() {
				continue
			}
			mode := md.Name()
			keep := keepDelta[mode]
			if keep == nil {
				continue
			}
			if _, ok := keep[d.Name()]; ok {
				continue
			}
			p := filepath.Join(deltaRoot, d.Name(), mode)
			sz := dirSize(p)
			removedDeltas = append(removedDeltas, p)
			bytesFreed += sz
			if !*dryRun {
				_ = os.RemoveAll(p)
			}
		}
	}

	if !*dryRun {
		// Update index to remove pruned entries.
		updateIndex(*root, *network, func(idx *PublishedIndex) {
			var keptR []*ReleaseRef
			for _, r := range idx.Releases {
				anyKept := false
				for mode := range r.Manifests {
					if _, ok := keepRelease[mode][r.Height]; ok {
						anyKept = true
						break
					}
				}
				if anyKept {
					keptR = append(keptR, r)
				}
			}
			idx.Releases = keptR
			var keptD []*DeltaRef
			for _, d := range idx.Deltas {
				key := fmt.Sprintf("%d-%d", d.FromHeight, d.ToHeight)
				if _, ok := keepDelta[d.Mode][key]; ok {
					keptD = append(keptD, d)
				}
			}
			idx.Deltas = keptD
		})
	}

	prefix := ""
	if *dryRun {
		prefix = "(dry-run) "
	}
	fmt.Printf("%sprune: keep-releases=%d keep-deltas=%d per mode\n",
		prefix, *keepReleases, *keepDeltas)
	fmt.Printf("  removed releases : %d\n", len(removedReleases))
	for _, p := range removedReleases {
		fmt.Printf("    %s\n", p)
	}
	fmt.Printf("  removed deltas   : %d\n", len(removedDeltas))
	for _, p := range removedDeltas {
		fmt.Printf("    %s\n", p)
	}
	fmt.Printf("  bytes freed      : %.2f GB\n", float64(bytesFreed)/1024/1024/1024)
}

func runList(args []string) {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	root := fs.String("root", "", "publication root")
	network := fs.String("network", "mainnet", "network name")
	_ = fs.Parse(args)
	if *root == "" {
		die("--root is required")
	}
	idx, err := readIndex(*root, *network)
	if err != nil {
		die("read index: %v", err)
	}
	fmt.Printf("network: %s   updated: %s\n", idx.Network, idx.Updated)
	fmt.Println("\nlatest:")
	for mode, r := range idx.Latest {
		fmt.Printf("  %-10s height=%d manifest_id=%s\n", mode, r.Height, abbrev(r.Manifests[mode]))
	}
	fmt.Printf("\nreleases (%d):\n", len(idx.Releases))
	sort.Slice(idx.Releases, func(i, j int) bool { return idx.Releases[i].Height > idx.Releases[j].Height })
	for _, r := range idx.Releases {
		modes := make([]string, 0, len(r.Manifests))
		for m := range r.Manifests {
			modes = append(modes, m)
		}
		sort.Strings(modes)
		fmt.Printf("  height=%d  modes=[%s]\n", r.Height, strings.Join(modes, ","))
	}
	fmt.Printf("\ndeltas (%d):\n", len(idx.Deltas))
	sort.Slice(idx.Deltas, func(i, j int) bool { return idx.Deltas[i].ToHeight > idx.Deltas[j].ToHeight })
	for _, d := range idx.Deltas {
		fmt.Printf("  mode=%-8s %d → %d  (id=%s)\n",
			d.Mode, d.FromHeight, d.ToHeight, abbrev(d.ManifestID))
	}
}

// ----- helpers -----

func setRelease(idx *PublishedIndex, mode string, height uint64, manID, createdAt string) {
	// Latest update.
	if idx.Latest == nil {
		idx.Latest = make(map[string]*ReleaseRef)
	}
	if cur, ok := idx.Latest[mode]; !ok || cur.Height < height {
		idx.Latest[mode] = &ReleaseRef{Height: height, CreatedAt: createdAt,
			Manifests: map[string]string{mode: manID}}
	}
	// Releases list — merge or append.
	for _, r := range idx.Releases {
		if r.Height == height {
			r.Manifests[mode] = manID
			return
		}
	}
	idx.Releases = append(idx.Releases, &ReleaseRef{
		Height:    height,
		CreatedAt: createdAt,
		Manifests: map[string]string{mode: manID},
	})
}

func appendDelta(idx *PublishedIndex, mode string, from, to uint64, manID, createdAt string) {
	// Replace any existing delta with the same (mode, from, to).
	for i, d := range idx.Deltas {
		if d.Mode == mode && d.FromHeight == from && d.ToHeight == to {
			idx.Deltas[i] = &DeltaRef{
				FromHeight: from, ToHeight: to, Mode: mode,
				ManifestID: manID, CreatedAt: createdAt,
			}
			return
		}
	}
	idx.Deltas = append(idx.Deltas, &DeltaRef{
		FromHeight: from, ToHeight: to, Mode: mode,
		ManifestID: manID, CreatedAt: createdAt,
	})
}

func readIndex(root, network string) (*PublishedIndex, error) {
	path := filepath.Join(root, network, "releases.json")
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &PublishedIndex{Network: network, Latest: map[string]*ReleaseRef{}}, nil
		}
		return nil, err
	}
	defer f.Close()
	var idx PublishedIndex
	if err := json.NewDecoder(f).Decode(&idx); err != nil {
		return nil, err
	}
	if idx.Network == "" {
		idx.Network = network
	}
	if idx.Latest == nil {
		idx.Latest = map[string]*ReleaseRef{}
	}
	return &idx, nil
}

func writeIndex(root, network string, idx *PublishedIndex) error {
	dir := filepath.Join(root, network)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	idx.Updated = time.Now().UTC().Format(time.RFC3339)
	tmp := filepath.Join(dir, "releases.json.tmp")
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(idx); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, "releases.json"))
}

func updateIndex(root, network string, mutate func(*PublishedIndex)) {
	idx, err := readIndex(root, network)
	if err != nil {
		die("read index: %v", err)
	}
	mutate(idx)
	if err := writeIndex(root, network, idx); err != nil {
		die("write index: %v", err)
	}
}

func transferFile(src, dst string, hardlink bool) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if hardlink {
		if err := os.Link(src, dst); err == nil {
			return nil
		}
		// Fall through to copy on cross-FS failure.
	}
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

func dirSize(p string) int64 {
	var total int64
	_ = filepath.Walk(p, func(_ string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	return total
}

func parseUint(s string) uint64 {
	var n uint64
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + uint64(c-'0')
	}
	return n
}

func abbrev(h string) string {
	if len(h) <= 16 {
		return h
	}
	return h[:8] + "..." + h[len(h)-8:]
}

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
