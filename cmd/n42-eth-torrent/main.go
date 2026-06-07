// n42-eth-torrent builds ONE multi-file BitTorrent metainfo (.torrent) over the
// files named by a tier manifest (from n42-eth-manifest), streaming the piece
// hashes from disk so an 800 GB archive never has to fit in RAM. It prints the
// magnet URI and, with --update-manifest, records the infohash/magnet back into
// the manifest JSON so the snapshot client can fetch over BitTorrent.
//
// The infohash depends only on the info dict (name + piece length + ordered
// files + piece hashes), so any producer building from the same datadir gets the
// same infohash regardless of which trackers/webseeds they attach.
//
// Usage:
//
//	n42-eth-torrent --datadir D:/n42-release --manifest D:/n42-release/manifest-minimal.json \
//	    --tracker udp://tracker.n42:6969 --webseed https://snapshots.n42/mainnet/ \
//	    --update-manifest
//	  → writes D:/n42-release/minimal.torrent + fills manifest.torrent
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/anacrolix/torrent/metainfo"

	"github.com/n42blockchain/N42/cmd/n42-eth-manifest/manifest"
	"github.com/n42blockchain/N42/cmd/n42-eth-torrent/torrentbuild"
)

func main() {
	datadir := flag.String("datadir", ".", "root the manifest paths are relative to")
	manPath := flag.String("manifest", "", "tier manifest JSON (from n42-eth-manifest) [required]")
	out := flag.String("out", "", "output .torrent path (default: <datadir>/<mode>.torrent)")
	piece := flag.Int64("piece", 0, "piece length in bytes (0 = auto-size by total)")
	trackerCSV := flag.String("tracker", "", "comma-separated announce URLs (BEP12 announce-list)")
	webseedCSV := flag.String("webseed", "", "comma-separated HTTP url-list bases (BEP19 webseeds)")
	name := flag.String("name", "", "torrent name (default: n42-<network>-<mode>-h<height>)")
	updateManifest := flag.Bool("update-manifest", false, "write infohash + magnet back into the manifest JSON")
	dryRun := flag.Bool("dry-run", false, "show file set + piece plan without hashing")
	flag.Parse()

	if *manPath == "" {
		die("--manifest is required")
	}
	man, err := loadManifest(*manPath)
	if err != nil {
		die("load manifest: %v", err)
	}
	if len(man.Files) == 0 {
		die("manifest %s has no files", *manPath)
	}

	files := torrentbuild.FilesFromManifest(man)
	var total int64
	for _, f := range files {
		total += f.Length
	}
	torrentName := *name
	if torrentName == "" {
		torrentName = fmt.Sprintf("n42-%s-%s-h%d", man.Network, man.Mode, man.Height)
	}
	pieceLen := *piece
	if pieceLen <= 0 {
		pieceLen = torrentbuild.AutoPieceLength(total)
	}

	if *dryRun {
		fmt.Printf("dry-run: %s\n", torrentName)
		fmt.Printf("  files       : %d\n", len(files))
		fmt.Printf("  total       : %.2f GB\n", gb(total))
		fmt.Printf("  piece length: %s\n", humanBytes(pieceLen))
		fmt.Printf("  pieces      : ~%d\n", (total+pieceLen-1)/pieceLen)
		return
	}

	t0 := time.Now()
	info, err := torrentbuild.BuildInfo(*datadir, torrentName, files, pieceLen)
	if err != nil {
		die("build info: %v", err)
	}

	trackers := splitCSV(*trackerCSV)
	webseeds := splitCSV(*webseedCSV)
	mi, ih, err := torrentbuild.BuildMetaInfo(info, torrentbuild.MetaOpts{
		Trackers:     trackers,
		WebSeeds:     webseeds,
		Comment:      fmt.Sprintf("N42 eth-el %s tier @ height %d", man.Mode, man.Height),
		CreatedBy:    "n42-eth-torrent",
		CreationDate: 0, // fixed → reproducible across producers
	})
	if err != nil {
		die("build metainfo: %v", err)
	}

	outPath := *out
	if outPath == "" {
		outPath = filepath.Join(*datadir, man.Mode+".torrent")
	}
	if err := writeTorrent(outPath, mi); err != nil {
		die("write torrent: %v", err)
	}
	magnet := torrentbuild.Magnet(mi, info)

	fmt.Printf("wrote %s\n", outPath)
	fmt.Printf("  name        : %s\n", torrentName)
	fmt.Printf("  infohash    : %s\n", ih.HexString())
	fmt.Printf("  files       : %d\n", len(files))
	fmt.Printf("  total       : %.2f GB\n", gb(total))
	fmt.Printf("  piece length: %s\n", humanBytes(pieceLen))
	fmt.Printf("  pieces      : %d\n", info.NumPieces())
	fmt.Printf("  elapsed     : %s\n", time.Since(t0).Truncate(time.Millisecond))
	fmt.Printf("  magnet      : %s\n", magnet)

	if *updateManifest {
		torRel := outPath
		if r, rerr := filepath.Rel(*datadir, outPath); rerr == nil && !strings.HasPrefix(r, "..") {
			torRel = filepath.ToSlash(r)
		}
		man.Torrent = &manifest.TorrentInfo{
			Name:        torrentName,
			InfoHash:    ih.HexString(),
			Magnet:      magnet,
			PieceLength: pieceLen,
			Pieces:      info.NumPieces(),
			TotalBytes:  total,
			TorrentFile: torRel,
			WebSeeds:    webseeds,
			Trackers:    trackers,
		}
		if err := writeJSON(*manPath, man); err != nil {
			die("update manifest: %v", err)
		}
		fmt.Printf("  manifest    : updated %s (torrent block)\n", *manPath)
	}
}

func loadManifest(path string) (*manifest.Manifest, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m manifest.Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

func writeTorrent(path string, mi *metainfo.MetaInfo) error {
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if err := mi.Write(f); err != nil {
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

func splitCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
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

func gb(b int64) float64 { return float64(b) / 1024 / 1024 / 1024 }
func humanBytes(b int64) string {
	switch {
	case b >= 1<<20:
		return fmt.Sprintf("%d MiB", b>>20)
	case b >= 1<<10:
		return fmt.Sprintf("%d KiB", b>>10)
	default:
		return fmt.Sprintf("%d B", b)
	}
}

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
