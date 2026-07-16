// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// P2 swarm distribution scale demo (project_distributed_compute_storage_
// wiring_plan §27): one seeder + N in-process leechers pull the SAME blob —
// the exact shape of IDC-node → phone witness/packet distribution. It asserts
// every leecher reconstructs the blob correctly, and reports the seeder's
// upload bytes to show egress does NOT grow linearly with leecher count
// (pieces are shared peer-to-peer), which is the whole reason the mobile layer
// reuses the torrent bridge instead of a direct-serve origin.
//
// Env-gated (real client sockets, seconds of wall time): set MV_SWARM_DEMO=1.

package torrent

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/anacrolix/torrent"

	"github.com/n42blockchain/N42/conf"
)

// demoClient returns a client plus a stop-and-clean func. It deliberately does
// NOT use t.TempDir: on Windows the torrent client's .torrent.db lingers a
// moment after Close, racing t.TempDir's RemoveAll. We stop the client, let
// the handle settle, then best-effort remove — a cleanup lock is not a test
// failure.
func demoClient(t *testing.T) (*Client, func()) {
	t.Helper()
	dir, err := os.MkdirTemp("", "mvswarm-*")
	if err != nil {
		t.Fatal(err)
	}
	cfg := &conf.TorrentDistCfg{
		DataDir:    dir,
		ListenAddr: "127.0.0.1:0", // ephemeral, localhost only
		EnableDHT:  false,         // no DHT: peers wired explicitly below
	}
	c, err := NewClient(cfg)
	if err != nil {
		os.RemoveAll(dir)
		t.Fatalf("client: %v", err)
	}
	return c, func() {
		c.Stop()
		for i := 0; i < 20; i++ {
			if os.RemoveAll(dir) == nil {
				if _, statErr := os.Stat(filepath.Join(dir)); os.IsNotExist(statErr) {
					return
				}
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
}

func TestSwarmDistributionScaleDemo(t *testing.T) {
	if os.Getenv("MV_SWARM_DEMO") != "1" {
		t.Skip("set MV_SWARM_DEMO=1 to run the swarm distribution demo")
	}

	const (
		leechers  = 12
		blobSize  = 2 << 20 // 2 MiB — a plausible witness/packet size
		pieceSize = 64 << 10 // 32 pieces: room for peer-to-peer sharing
	)

	// A deterministic, incompressible-ish blob standing in for a block packet.
	blob := make([]byte, blobSize)
	for i := range blob {
		blob[i] = byte(i*2654435761 + 1)
	}

	// Seeder holds the only original copy.
	seeder, stopSeeder := demoClient(t)
	defer stopSeeder()
	sb := NewBridge(seeder)
	var contentHash [32]byte
	copy(contentHash[:], blob[:32])
	mapping, err := sb.SeedContent(context.Background(), contentHash, "packet.bin", blob, pieceSize)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, ok := seeder.GetTorrent(mapping.InfoHash); !ok {
		t.Fatal("seeder torrent missing after seed")
	}
	magnet, err := sb.FormatMagnet(contentHash)
	if err != nil {
		t.Fatalf("magnet: %v", err)
	}

	// Bring up the leechers via the magnet (metadata is exchanged over the
	// swarm, like a real phone joining) and wire a full peer mesh (seeder +
	// every other leecher), so a piece one leecher has can flow to another
	// without the seeder serving it again.
	lc := make([]*Client, leechers)
	lt := make([]*torrent.Torrent, leechers)
	for i := 0; i < leechers; i++ {
		c, stop := demoClient(t)
		defer stop()
		lc[i] = c
		tor, err := c.AddMagnet(magnet)
		if err != nil {
			t.Fatalf("leecher %d add: %v", i, err)
		}
		lt[i] = tor
	}
	for i := 0; i < leechers; i++ {
		lt[i].AddClientPeer(seeder.client)
		for j := 0; j < leechers; j++ {
			if j != i {
				lt[i].AddClientPeer(lc[j].client)
			}
		}
	}

	// Download all concurrently, bounded.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	done := make(chan error, leechers)
	for i := 0; i < leechers; i++ {
		i := i
		go func() { done <- lc[i].Download(ctx, lt[i]) }()
	}
	for i := 0; i < leechers; i++ {
		if err := <-done; err != nil {
			t.Fatalf("a leecher download failed: %v", err)
		}
	}

	// Every leecher must reconstruct the blob byte-for-byte.
	for i := 0; i < leechers; i++ {
		got, err := lc[i].ReadTorrentData(lt[i])
		if err != nil {
			t.Fatalf("leecher %d read: %v", i, err)
		}
		if !bytes.Equal(got, blob) {
			t.Fatalf("leecher %d reconstructed a different blob (%d bytes)", i, len(got))
		}
	}

	// Egress: had every leecher pulled the whole blob from the origin, the
	// seeder would have uploaded leechers*blobSize. Peer sharing makes it less.
	sstats := seeder.Stats()
	up := sstats.BytesWrittenData.Int64()
	naive := int64(leechers) * int64(blobSize)
	ratio := float64(up) / float64(blobSize)
	t.Logf("swarm demo: %d leechers × %d B; seeder uploaded %d B = %.1f× blob (naive origin-serve = %d B = %d×)",
		leechers, blobSize, up, ratio, naive, leechers)
	if up >= naive {
		t.Fatalf("no peer sharing: seeder egress %d >= naive %d", up, naive)
	}
}
