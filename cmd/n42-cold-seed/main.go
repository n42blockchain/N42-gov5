// n42-cold-seed: archive/seeder node tool. Seeds ALL columnar freezer files
// (headers + bodies + witness) over BitTorrent so other nodes can pull full
// history under a 1-of-N assumption, and publishes a torrentsync manifest with
// per-file infohash + SHA256 + block range.
//
// Sealed files (every file but the highest-numbered of each prefix) are
// immutable → seeded once; later runs skip them on unchanged size. The ACTIVE
// file (highest NNNN) is still being appended to → re-seeded every run. Run
// weekly (--interval 168h) so its manifest entry tracks the growing tail.
//
// The seeding logic lives in internal/ethel/coldseed (shared with the eth-el
// node's embedded archive seeder); this is the standalone CLI wrapper.
//
// Usage:
//
//	n42-cold-seed --dir <freezer> --prefixes bodyc,headerc,witness \
//	   --manifest seed-manifest.json --seeddata <torrent-data-dir> [--interval 168h] [--dryrun]
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/n42blockchain/N42/conf"
	storagetorrent "github.com/n42blockchain/N42/internal/distributed/storage/torrent"
	"github.com/n42blockchain/N42/internal/ethel/coldseed"
	"github.com/n42blockchain/N42/internal/sync/torrentsync"
)

func main() {
	dir := flag.String("dir", "", "freezer dir with <prefix>.NNNN.cdat files")
	prefixesS := flag.String("prefixes", "bodyc,headerc", "comma list of file prefixes to seed (bodyc,headerc,witness)")
	manifestPath := flag.String("manifest", "seed-manifest.json", "manifest path (read existing + write updated)")
	seedData := flag.String("seeddata", "", "torrent client data dir (required unless --dryrun)")
	listen := flag.String("listen", ":0", "torrent listen addr")
	chainID := flag.Uint64("chainid", 1, "chain id")
	pieceSize := flag.Int("piece", 4*1024*1024, "torrent piece size bytes")
	interval := flag.Duration("interval", 0, "if >0, loop forever re-seeding every interval (e.g. 168h weekly)")
	dryrun := flag.Bool("dryrun", false, "classify + list what would be seeded; no torrent client, no file reads")
	flag.Parse()
	if *dir == "" {
		fmt.Fprintln(os.Stderr, "usage: n42-cold-seed --dir <freezer> [--prefixes ..] [--seeddata D] [--interval 168h] [--dryrun]")
		os.Exit(1)
	}
	prefixes := strings.Split(*prefixesS, ",")
	opts := coldseed.Options{
		Dir: *dir, Prefixes: prefixes, ManifestPath: *manifestPath,
		ChainID: *chainID, PieceSize: *pieceSize, Interval: *interval,
	}

	if *dryrun {
		existing, _ := torrentsync.LoadManifest(*manifestPath)
		for _, p := range prefixes {
			p = strings.TrimSpace(p)
			cur, err := coldseed.ScanPrefix(*dir, p)
			if err != nil || len(cur) == 0 {
				fmt.Printf("  [%s] no files\n", p)
				continue
			}
			plan := coldseed.PlanSeed(cur, existing)
			fmt.Printf("  [%s] %d files: %d sealed-kept, %d to (re)seed (active=%s)\n",
				p, len(cur), len(plan.Keep), len(plan.Reseed), cur[len(cur)-1].FileName)
			for _, f := range plan.Reseed {
				tag := "sealed-new/changed"
				if f.Active {
					tag = "ACTIVE (weekly reseed)"
				}
				fmt.Printf("    would seed %s (%.2f GB, blocks %d..%d) [%s]\n",
					f.FileName, float64(f.Size)/(1<<30), f.FromBlock, f.ToBlock, tag)
			}
		}
		return
	}

	if *seedData == "" {
		fmt.Fprintln(os.Stderr, "--seeddata required unless --dryrun")
		os.Exit(1)
	}
	cfg := conf.DefaultTorrentDistCfg()
	cfg.Enabled = true
	cfg.DataDir = *seedData
	cfg.ListenAddr = *listen
	cfg.EnableDHT = true
	client, err := storagetorrent.NewClient(&cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "torrent client:", err)
		os.Exit(1)
	}
	client.Start()
	defer client.Stop()
	sink := coldseed.BridgeSink{Bridge: storagetorrent.NewBridge(client)}

	for {
		fmt.Printf("=== n42-cold-seed pass (dir=%s prefixes=%v) ===\n", *dir, prefixes)
		if err := coldseed.RunOnce(context.Background(), opts, sink); err != nil {
			fmt.Fprintln(os.Stderr, "pass error:", err)
			if *interval == 0 {
				os.Exit(1)
			}
		}
		if *interval == 0 {
			return
		}
		fmt.Printf("sleeping %s until next reseed (active file tracks the growing tail)...\n", *interval)
		time.Sleep(*interval)
	}
}
