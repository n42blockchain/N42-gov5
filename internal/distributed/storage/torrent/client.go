// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.

// Package torrent provides a BitTorrent bridge for N42's content-addressed storage.
//
// It wraps the anacrolix/torrent library to enable:
//   - Seeding on-chain CAS content as torrents
//   - Downloading content by infohash or magnet link
//   - Mapping keccak256 content hashes to torrent infohashes
package torrent

import (
	"context"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
	"golang.org/x/time/rate"

	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/log"
)

// Client wraps an anacrolix/torrent client for CAS content distribution.
type Client struct {
	cfg    *conf.TorrentDistCfg
	client *torrent.Client

	mu       sync.RWMutex
	torrents map[metainfo.Hash]*torrent.Torrent // infohash -> torrent

	ctx    context.Context
	cancel context.CancelFunc
}

// NewClient creates a new BitTorrent client for content distribution.
func NewClient(cfg *conf.TorrentDistCfg) (*Client, error) {
	ctx, cancel := context.WithCancel(context.Background())

	tcfg := torrent.NewDefaultClientConfig()
	tcfg.ListenPort = 0 // default: OS-assigned ephemeral port
	// Honor a configured listen address (host:port). Previously cfg.ListenAddr
	// was ignored, so the client always bound a random port — direct-peer
	// dialing (and any fixed-port firewall rule) could not work.
	if cfg.ListenAddr != "" {
		if _, portStr, err := net.SplitHostPort(cfg.ListenAddr); err == nil {
			if p, err := strconv.Atoi(portStr); err == nil {
				tcfg.ListenPort = p
			}
		}
	}
	tcfg.DataDir = cfg.DataDir
	tcfg.NoDHT = !cfg.EnableDHT
	tcfg.NoDefaultPortForwarding = true
	tcfg.Seed = true

	// Set rate limits if configured
	if cfg.UploadRateKB > 0 {
		tcfg.UploadRateLimiter = newRateLimiter(cfg.UploadRateKB * 1024)
	}
	if cfg.DownloadRateKB > 0 {
		tcfg.DownloadRateLimiter = newRateLimiter(cfg.DownloadRateKB * 1024)
	}

	// Use file-based storage
	dataDir := filepath.Clean(cfg.DataDir)
	tcfg.DefaultStorage = storage.NewFileByInfoHash(dataDir)

	tc, err := torrent.NewClient(tcfg)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("torrent client: %w", err)
	}

	return &Client{
		cfg:      cfg,
		client:   tc,
		torrents: make(map[metainfo.Hash]*torrent.Torrent),
		ctx:      ctx,
		cancel:   cancel,
	}, nil
}

// Start begins the torrent client.
func (c *Client) Start() {
	log.Info("BitTorrent client started", "dht", c.cfg.EnableDHT, "dataDir", c.cfg.DataDir)
}

// Stop shuts down the client and all active torrents.
func (c *Client) Stop() {
	c.cancel()
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, t := range c.torrents {
		t.Drop()
	}
	c.torrents = make(map[metainfo.Hash]*torrent.Torrent)
	c.client.Close()
	log.Info("BitTorrent client stopped")
}

// AddTorrent adds a torrent from metainfo and starts it.
func (c *Client) AddTorrent(mi *metainfo.MetaInfo) (*torrent.Torrent, error) {
	t, err := c.client.AddTorrent(mi)
	if err != nil {
		return nil, fmt.Errorf("add torrent: %w", err)
	}

	c.mu.Lock()
	c.torrents[t.InfoHash()] = t
	c.mu.Unlock()

	return t, nil
}

// AddMagnet adds a torrent from a magnet URI.
func (c *Client) AddMagnet(magnetURI string) (*torrent.Torrent, error) {
	t, err := c.client.AddMagnet(magnetURI)
	if err != nil {
		return nil, fmt.Errorf("add magnet: %w", err)
	}

	c.mu.Lock()
	c.torrents[t.InfoHash()] = t
	c.mu.Unlock()

	return t, nil
}

// GetTorrent looks up a torrent by infohash.
func (c *Client) GetTorrent(ih metainfo.Hash) (*torrent.Torrent, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	t, ok := c.torrents[ih]
	return t, ok
}

// Download downloads a torrent to completion with a timeout.
func (c *Client) Download(ctx context.Context, t *torrent.Torrent) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.GotInfo():
	}

	t.DownloadAll()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.Complete().On():
		return nil
	}
}

// ReadTorrentData reads the complete data of a downloaded torrent.
func (c *Client) ReadTorrentData(t *torrent.Torrent) ([]byte, error) {
	select {
	case <-t.GotInfo():
	case <-time.After(30 * time.Second):
		return nil, fmt.Errorf("timeout waiting for torrent info")
	}

	length := t.Info().TotalLength()
	if length <= 0 {
		return nil, fmt.Errorf("torrent has no data")
	}

	r := t.NewReader()
	defer r.Close()

	data := make([]byte, length)
	if _, err := io.ReadFull(r, data); err != nil {
		return nil, fmt.Errorf("read torrent: %w", err)
	}
	return data, nil
}

// RemoveTorrent drops a torrent from the client.
func (c *Client) RemoveTorrent(ih metainfo.Hash) {
	c.mu.Lock()
	if t, ok := c.torrents[ih]; ok {
		t.Drop()
		delete(c.torrents, ih)
	}
	c.mu.Unlock()
}

// Stats returns aggregate client statistics.
func (c *Client) Stats() torrent.ConnStats {
	return c.client.ConnStats()
}

// ActiveTorrents returns the number of active torrents.
func (c *Client) ActiveTorrents() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.torrents)
}

// newRateLimiter creates a token-bucket rate limiter for the given bytes/sec.
// Burst is capped at 256KB to prevent large initial bursts.
func newRateLimiter(bytesPerSec int) *rate.Limiter {
	burst := bytesPerSec
	const maxBurst = 256 * 1024 // 256KB
	if burst > maxBurst {
		burst = maxBurst
	}
	return rate.NewLimiter(rate.Limit(bytesPerSec), burst)
}
