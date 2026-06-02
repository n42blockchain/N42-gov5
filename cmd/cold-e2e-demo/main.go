// cold-e2e-demo: end-to-end proof of the ARCHIVE full-history download path.
// One node seeds several cold segments (via the real Bridge.SeedContent) and
// publishes a torrentsync manifest; a second node pulls EVERY segment via the
// real coldresolve.DownloadAll, SHA256-verifying each against the manifest.
//
// Peer discovery is short-circuited to localhost direct-peering (no DHT) so the
// test exercises the multi-file download orchestration, not the swarm finder.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"time"

	atorrent "github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"

	"github.com/n42blockchain/N42/conf"
	storagetorrent "github.com/n42blockchain/N42/internal/distributed/storage/torrent"
	"github.com/n42blockchain/N42/internal/ethel/coldresolve"
	"github.com/n42blockchain/N42/internal/sync/torrentsync"
)

func mkClient(dir, listen string) *storagetorrent.Client {
	_ = os.MkdirAll(dir, 0755)
	cfg := conf.DefaultTorrentDistCfg()
	cfg.Enabled, cfg.DataDir, cfg.ListenAddr, cfg.EnableDHT, cfg.EnablePEX = true, dir, listen, false, false
	c, err := storagetorrent.NewClient(&cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	c.Start()
	return c
}

// directFetcher implements coldresolve.Fetcher by pulling each segment from a
// known seeder over a direct localhost peer (stands in for swarm discovery).
type directFetcher struct {
	fc       *storagetorrent.Client
	metas    map[string][]byte // infohash hex -> metainfo bytes
	seedPort int
	cacheDir string
}

func (f *directFetcher) Fetch(seg torrentsync.SegmentInfo) (string, error) {
	dst := f.cacheDir + "/" + seg.FileName
	if _, err := os.Stat(dst); err == nil {
		return dst, nil
	}
	mb, ok := f.metas[seg.InfoHash]
	if !ok {
		return "", fmt.Errorf("no metainfo for %s", seg.FileName)
	}
	mi, err := metainfo.Load(bytes.NewReader(mb))
	if err != nil {
		return "", err
	}
	t, err := f.fc.AddTorrent(mi)
	if err != nil {
		return "", err
	}
	t.AddPeers([]atorrent.PeerInfo{{Addr: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: f.seedPort}}})
	data, err := f.fc.ReadTorrentData(t) // blocks until complete via DownloadAll inside
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(dst, data, 0644); err != nil {
		return "", err
	}
	return dst, nil
}

func main() {
	base, _ := os.MkdirTemp("", "cold-e2e-*")
	defer os.RemoveAll(base)
	const seedPort = 42130

	seeder := mkClient(base+"/seeder", fmt.Sprintf("127.0.0.1:%d", seedPort))
	defer seeder.Stop()
	fetcher := mkClient(base+"/fetcher", "127.0.0.1:42131")
	defer fetcher.Stop()
	sbridge := storagetorrent.NewBridge(seeder)

	// Seed 3 cold "segments" + build the manifest (as n42-cold-seed would).
	files := []string{"bodyc.0001.cdat", "bodyc.0002.cdat", "receipts.0001.cdat"}
	var segs []torrentsync.SegmentInfo
	metas := map[string][]byte{}
	for i, name := range files {
		blob := make([]byte, (i+2)*2*1024*1024) // 4,6,8 MB
		for j := range blob {
			blob[j] = byte(j*2654435761 + i)
		}
		sum := sha256.Sum256(blob)
		var ch [32]byte
		copy(ch[:], sum[:])
		m, err := sbridge.SeedContent(nil, ch, name, blob, 256*1024)
		if err != nil {
			fmt.Fprintln(os.Stderr, "seed:", err)
			os.Exit(1)
		}
		// Wait for the seeder to verify its pieces.
		if st, ok := seeder.GetTorrent(m.InfoHash); ok {
			<-st.GotInfo()
			for k := 0; k < 100 && st.BytesCompleted() < int64(len(blob)); k++ {
				time.Sleep(50 * time.Millisecond)
			}
		}
		segs = append(segs, torrentsync.SegmentInfo{
			FileName: name, Size: int64(len(blob)),
			SHA256: hex.EncodeToString(sum[:]), InfoHash: m.InfoHash.HexString(),
		})
		metas[m.InfoHash.HexString()] = m.MetaInfo
		fmt.Printf("seeder: %s (%d MB) infohash %s…\n", name, len(blob)>>20, m.InfoHash.HexString()[:12])
	}
	manifest := &torrentsync.Manifest{ChainID: 1, Segments: segs, UpdatedAt: time.Unix(0, 0)}

	// Archive node: download EVERY segment via the real DownloadAll path.
	cache := base + "/store"
	_ = os.MkdirAll(cache, 0755)
	df := &directFetcher{fc: fetcher, metas: metas, seedPort: seedPort, cacheDir: cache}
	t0 := time.Now()
	fetched, _, failed := coldresolve.DownloadAll(manifest, df, true)
	fmt.Printf("archive DownloadAll: fetched=%d failed=%d in %s\n", fetched, failed, time.Since(t0).Truncate(time.Millisecond))

	// Verify every file landed + matches.
	allOK := fetched == len(files) && failed == 0
	for _, seg := range segs {
		got, err := os.ReadFile(cache + "/" + seg.FileName)
		if err != nil {
			allOK = false
			continue
		}
		sum := sha256.Sum256(got)
		if hex.EncodeToString(sum[:]) != seg.SHA256 {
			allOK = false
		}
	}
	if allOK {
		fmt.Println("ARCHIVE E2E PASS (full history seeded by A, downloaded + verified by B over BitTorrent)")
	} else {
		fmt.Println("ARCHIVE E2E FAIL")
		os.Exit(1)
	}
}
