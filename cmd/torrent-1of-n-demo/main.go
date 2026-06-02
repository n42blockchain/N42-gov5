// torrent-1of-n-demo: end-to-end proof that a cold segment seeded by one node
// can be pulled by another over BitTorrent (the 1-of-N availability the
// EIP-4444 cold-read path relies on). Starts TWO local torrent clients:
//
//	seeder  — SeedContent(blob) -> infohash + metainfo
//	fetcher — AddTorrent(metainfo) + direct-peer the seeder -> Download -> read
//
// then verifies the pulled bytes equal the seeded bytes. No DHT/tracker: the
// fetcher is pointed at the seeder directly (localhost), isolating the
// swarm-transfer mechanism from peer discovery.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"net"
	"os"
	"time"

	atorrent "github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"

	"github.com/n42blockchain/N42/conf"
	storagetorrent "github.com/n42blockchain/N42/internal/distributed/storage/torrent"
)

func mkClient(dir, listen string) (*storagetorrent.Client, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	cfg := conf.DefaultTorrentDistCfg()
	cfg.Enabled = true
	cfg.DataDir = dir
	cfg.ListenAddr = listen
	cfg.EnableDHT = false // direct-peer; isolate transfer from discovery
	cfg.EnablePEX = false
	c, err := storagetorrent.NewClient(&cfg)
	if err != nil {
		return nil, err
	}
	c.Start()
	return c, nil
}

func main() {
	base, err := os.MkdirTemp("", "torrent-1ofn-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer os.RemoveAll(base)

	const seederAddr = "127.0.0.1:42120"
	const fetcherAddr = "127.0.0.1:42121"

	seeder, err := mkClient(base+"/seeder", seederAddr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "seeder:", err)
		os.Exit(1)
	}
	defer seeder.Stop()
	fetcher, err := mkClient(base+"/fetcher", fetcherAddr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fetcher:", err)
		os.Exit(1)
	}
	defer fetcher.Stop()

	// Simulated cold segment (a few MB so it spans multiple pieces).
	blob := make([]byte, 6*1024*1024)
	for i := range blob {
		blob[i] = byte(i*2654435761 + i>>3)
	}
	want := sha256.Sum256(blob)

	sb := storagetorrent.NewBridge(seeder)
	var ch [32]byte
	copy(ch[:], want[:])
	mapping, err := sb.SeedContent(context.Background(), ch, "bodyc.0097.cdat", blob, 256*1024)
	if err != nil {
		fmt.Fprintln(os.Stderr, "seed:", err)
		os.Exit(1)
	}
	fmt.Printf("seeder: seeding %d B as infohash %s (listen %s)\n", len(blob), mapping.InfoHash.HexString(), seederAddr)

	// Wait for the seeder to finish verifying its on-disk pieces (VerifyData is
	// async) so it actually has data to serve before the fetcher connects.
	if st, ok := seeder.GetTorrent(mapping.InfoHash); ok {
		<-st.GotInfo()
		for i := 0; i < 100 && st.BytesCompleted() < int64(len(blob)); i++ {
			time.Sleep(100 * time.Millisecond)
		}
		fmt.Printf("seeder: verified %d/%d B complete\n", st.BytesCompleted(), len(blob))
	}

	// Fetcher: load the metainfo, add it, point it directly at the seeder, download.
	mi, err := metainfo.Load(bytes.NewReader(mapping.MetaInfo))
	if err != nil {
		fmt.Fprintln(os.Stderr, "load metainfo:", err)
		os.Exit(1)
	}
	t, err := fetcher.AddTorrent(mi)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fetcher add:", err)
		os.Exit(1)
	}
	t.AddPeers([]atorrent.PeerInfo{{Addr: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 42120}}})
	fmt.Printf("fetcher: added torrent, direct-peered seeder %s, downloading...\n", seederAddr)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	t0 := time.Now()
	if err := fetcher.Download(ctx, t); err != nil {
		fmt.Fprintln(os.Stderr, "download:", err)
		os.Exit(1)
	}
	got, err := fetcher.ReadTorrentData(t)
	if err != nil {
		fmt.Fprintln(os.Stderr, "read:", err)
		os.Exit(1)
	}
	gotSum := sha256.Sum256(got)
	ok := bytes.Equal(gotSum[:], want[:]) && len(got) == len(blob)
	fmt.Printf("fetcher: pulled %d B in %s; sha256 match=%v\n", len(got), time.Since(t0).Truncate(time.Millisecond), ok)
	if !ok {
		fmt.Println("1-of-N FAIL")
		os.Exit(1)
	}
	fmt.Println("1-of-N PASS (segment seeded by node A, pulled by node B over BitTorrent)")
}
