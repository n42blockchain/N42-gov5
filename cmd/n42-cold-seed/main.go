// n42-cold-seed: archive/seeder node tool. Seeds ALL columnar freezer files
// (headers + bodies + witness) over BitTorrent so other nodes can pull full
// history under a 1-of-N assumption, and publishes a torrentsync manifest with
// per-file infohash + SHA256 + block range.
//
// Sealed files (every file but the highest-numbered of each prefix) are
// immutable → seeded once; later runs skip them (size unchanged). The ACTIVE
// file (highest NNNN) is still being appended to, so it is re-seeded every run
// — run weekly (--interval 168h) so its manifest entry tracks the growing tail.
//
// Usage:
//
//	n42-cold-seed --dir <freezer> --prefixes bodyc,headerc,witness \
//	   --manifest seed-manifest.json --seeddata <torrent-data-dir> [--interval 168h] [--dryrun]
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/internal/distributed/storage/torrent"
	"github.com/n42blockchain/N42/internal/ethel/coldseed"
	"github.com/n42blockchain/N42/internal/ethel/historyexpiry"
	"github.com/n42blockchain/N42/internal/sync/torrentsync"
)

const segSize = historyexpiry.SegSize

// fileNumRe parses "<prefix>.NNNN.cdat" → NNNN.
func parseFileNum(name, prefix string) (int, bool) {
	if !strings.HasPrefix(name, prefix+".") || !strings.HasSuffix(name, ".cdat") {
		return 0, false
	}
	mid := strings.TrimSuffix(strings.TrimPrefix(name, prefix+"."), ".cdat")
	n, err := strconv.Atoi(mid)
	if err != nil {
		return 0, false
	}
	return n, true
}

// cidxGroups maps fileNum → [minSeg,maxSeg] by reading <prefix>.cidx (8B/seg,
// fileNum = LE u16[0:2]). Returns nil if the cidx is absent (block ranges then 0).
func cidxGroups(dir, prefix string) map[int][2]uint64 {
	data, err := os.ReadFile(filepath.Join(dir, prefix+".cidx"))
	if err != nil {
		return nil
	}
	groups := map[int][2]uint64{}
	for s := 0; (s+1)*8 <= len(data); s++ {
		fn := int(uint16(data[s*8]) | uint16(data[s*8+1])<<8)
		g, ok := groups[fn]
		if !ok {
			groups[fn] = [2]uint64{uint64(s), uint64(s)}
		} else {
			if uint64(s) < g[0] {
				g[0] = uint64(s)
			}
			if uint64(s) > g[1] {
				g[1] = uint64(s)
			}
			groups[fn] = g
		}
	}
	return groups
}

// scanPrefix returns the current FileState list for a prefix (active = max NNNN).
func scanPrefix(dir, prefix string) ([]coldseed.FileState, error) {
	matches, err := filepath.Glob(filepath.Join(dir, prefix+".*.cdat"))
	if err != nil {
		return nil, err
	}
	type fe struct {
		num  int
		name string
		size int64
	}
	var fes []fe
	for _, m := range matches {
		name := filepath.Base(m)
		num, ok := parseFileNum(name, prefix)
		if !ok {
			continue
		}
		fi, err := os.Stat(m)
		if err != nil {
			continue
		}
		fes = append(fes, fe{num, name, fi.Size()})
	}
	if len(fes) == 0 {
		return nil, nil
	}
	sort.Slice(fes, func(i, j int) bool { return fes[i].num < fes[j].num })
	maxNum := fes[len(fes)-1].num
	groups := cidxGroups(dir, prefix)
	out := make([]coldseed.FileState, 0, len(fes))
	for _, f := range fes {
		fs := coldseed.FileState{FileName: f.name, Size: f.size, Active: f.num == maxNum}
		if g, ok := groups[f.num]; ok {
			fs.FromBlock = g[0] * segSize
			fs.ToBlock = g[1]*segSize + segSize - 1
		}
		out = append(out, fs)
	}
	return out, nil
}

func sha256File(path string) (string, []byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", nil, err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), data, nil
}

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

	var bridge *torrent.Bridge
	if !*dryrun {
		if *seedData == "" {
			fmt.Fprintln(os.Stderr, "--seeddata required unless --dryrun")
			os.Exit(1)
		}
		cfg := conf.DefaultTorrentDistCfg()
		cfg.Enabled = true
		cfg.DataDir = *seedData
		cfg.ListenAddr = *listen
		cfg.EnableDHT = true
		client, err := torrent.NewClient(&cfg)
		if err != nil {
			fmt.Fprintln(os.Stderr, "torrent client:", err)
			os.Exit(1)
		}
		client.Start()
		defer client.Stop()
		bridge = torrent.NewBridge(client)
	}

	runOnce := func() error {
		existing, _ := torrentsync.LoadManifest(*manifestPath) // nil on first run
		var allKeep, allSeeded []torrentsync.SegmentInfo
		var nReseed, nKeep int
		for _, prefix := range prefixes {
			prefix = strings.TrimSpace(prefix)
			if prefix == "" {
				continue
			}
			current, err := scanPrefix(*dir, prefix)
			if err != nil {
				return fmt.Errorf("scan %s: %w", prefix, err)
			}
			if len(current) == 0 {
				fmt.Printf("  [%s] no files\n", prefix)
				continue
			}
			plan := coldseed.PlanSeed(current, existing)
			allKeep = append(allKeep, plan.Keep...)
			nKeep += len(plan.Keep)
			activeName := current[len(current)-1].FileName
			fmt.Printf("  [%s] %d files: %d sealed-kept, %d to (re)seed (active=%s)\n",
				prefix, len(current), len(plan.Keep), len(plan.Reseed), activeName)

			for _, f := range plan.Reseed {
				if *dryrun {
					tag := "sealed-new/changed"
					if f.Active {
						tag = "ACTIVE (weekly reseed)"
					}
					fmt.Printf("    would seed %s (%.2f GB, blocks %d..%d) [%s]\n",
						f.FileName, float64(f.Size)/(1<<30), f.FromBlock, f.ToBlock, tag)
					continue
				}
				sum, data, err := sha256File(filepath.Join(*dir, f.FileName))
				if err != nil {
					return fmt.Errorf("read %s: %w", f.FileName, err)
				}
				var ch [32]byte
				copy(ch[:], mustHex(sum))
				mapping, err := bridge.SeedContent(context.Background(), ch, f.FileName, data, *pieceSize)
				if err != nil {
					return fmt.Errorf("seed %s: %w", f.FileName, err)
				}
				allSeeded = append(allSeeded, torrentsync.SegmentInfo{
					FromBlock: f.FromBlock, ToBlock: f.ToBlock, FileName: f.FileName,
					Size: f.Size, BlockCount: f.ToBlock - f.FromBlock + 1,
					SHA256: sum, InfoHash: mapping.InfoHash.HexString(),
				})
				fmt.Printf("    seeded %s: infohash %s sha256 %s…\n",
					f.FileName, mapping.InfoHash.HexString(), sum[:12])
			}
			nReseed += len(plan.Reseed)
		}

		if *dryrun {
			fmt.Printf("dryrun: %d prefixes, %d kept, would (re)seed %d\n", len(prefixes), nKeep, nReseed)
			return nil
		}
		segs := append(allKeep, allSeeded...)
		sort.Slice(segs, func(i, j int) bool {
			if segs[i].FileName != segs[j].FileName {
				return segs[i].FileName < segs[j].FileName
			}
			return segs[i].FromBlock < segs[j].FromBlock
		})
		m := &torrentsync.Manifest{ChainID: *chainID, Segments: segs, UpdatedAt: time.Unix(0, 0)}
		if err := torrentsync.SaveManifest(m, *manifestPath); err != nil {
			return fmt.Errorf("save manifest: %w", err)
		}
		fmt.Printf("manifest %s: %d segments (%d kept + %d seeded)\n", *manifestPath, len(segs), nKeep, len(allSeeded))
		return nil
	}

	for {
		fmt.Printf("=== n42-cold-seed pass (dir=%s prefixes=%v) ===\n", *dir, prefixes)
		if err := runOnce(); err != nil {
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

func mustHex(s string) []byte {
	b, _ := hex.DecodeString(s)
	return b
}
