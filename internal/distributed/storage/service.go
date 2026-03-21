// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

// Package storage implements a decentralized storage bridge that connects
// N42 smart contracts to IPFS/Filecoin storage networks via HTTP API.
package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/log"
)

// cidPattern validates CID format (CIDv0: Qm..., CIDv1: b..., bafy...)
var cidPattern = regexp.MustCompile(`^(Qm[1-9A-HJ-NP-Za-km-z]{44,}|b[a-z2-7]{58,})$`)

// validateCID checks that a CID string is well-formed and safe.
func validateCID(cid string) error {
	if cid == "" {
		return fmt.Errorf("empty CID")
	}
	if !cidPattern.MatchString(cid) {
		return fmt.Errorf("invalid CID format: %s", cid)
	}
	return nil
}

// ObjectStat holds metadata about a stored object.
type ObjectStat struct {
	Hash           string `json:"Hash"`
	NumLinks       int    `json:"NumLinks"`
	BlockSize      int64  `json:"BlockSize"`
	LinksSize      int64  `json:"LinksSize"`
	DataSize       int64  `json:"DataSize"`
	CumulativeSize int64  `json:"CumulativeSize"`
}

// IPFSClient communicates with a local IPFS node via HTTP API.
type IPFSClient struct {
	apiBase    string
	httpClient *http.Client
	maxGetSize int64
}

// NewIPFSClient creates an IPFS HTTP API client.
func NewIPFSClient(apiAddr string, pinTimeout, getTimeout time.Duration, maxGetSize int64) *IPFSClient {
	if !strings.HasPrefix(apiAddr, "http") {
		apiAddr = "http://" + apiAddr
	}
	return &IPFSClient{
		apiBase: strings.TrimRight(apiAddr, "/"),
		httpClient: &http.Client{
			Timeout: maxDuration(pinTimeout, getTimeout),
		},
		maxGetSize: maxGetSize,
	}
}

// retryDo retries an HTTP request up to maxRetries times with exponential backoff.
func (c *IPFSClient) retryDo(req *http.Request, maxRetries int) (*http.Response, error) {
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt*attempt) * 100 * time.Millisecond)
		}
		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode >= 500 {
			resp.Body.Close()
			lastErr = fmt.Errorf("IPFS: server error %d", resp.StatusCode)
			continue
		}
		return resp, nil
	}
	return nil, fmt.Errorf("IPFS: max retries exceeded: %w", lastErr)
}

// Pin pins a CID on the IPFS node.
func (c *IPFSClient) Pin(ctx context.Context, cid string) error {
	if err := validateCID(cid); err != nil {
		return err
	}
	url := fmt.Sprintf("%s/api/v0/pin/add?arg=%s", c.apiBase, url.QueryEscape(cid))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("ipfs pin: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("ipfs pin failed (%d): %s", resp.StatusCode, body)
	}
	return nil
}

// Unpin removes a pin from the IPFS node.
func (c *IPFSClient) Unpin(ctx context.Context, cid string) error {
	if err := validateCID(cid); err != nil {
		return err
	}
	url := fmt.Sprintf("%s/api/v0/pin/rm?arg=%s", c.apiBase, url.QueryEscape(cid))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("ipfs unpin: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("ipfs unpin failed (%d): %s", resp.StatusCode, body)
	}
	return nil
}

// Get retrieves data from IPFS by CID, limited by maxGetSize.
func (c *IPFSClient) Get(ctx context.Context, cid string) ([]byte, error) {
	if err := validateCID(cid); err != nil {
		return nil, err
	}
	url := fmt.Sprintf("%s/api/v0/cat?arg=%s", c.apiBase, url.QueryEscape(cid))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ipfs get: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("ipfs get failed (%d): %s", resp.StatusCode, body)
	}
	return io.ReadAll(io.LimitReader(resp.Body, c.maxGetSize))
}

// Stat returns metadata about an IPFS object.
func (c *IPFSClient) Stat(ctx context.Context, cid string) (*ObjectStat, error) {
	if err := validateCID(cid); err != nil {
		return nil, err
	}
	url := fmt.Sprintf("%s/api/v0/object/stat?arg=%s", c.apiBase, url.QueryEscape(cid))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ipfs stat: %w", err)
	}
	defer resp.Body.Close()
	var stat ObjectStat
	if err := json.NewDecoder(resp.Body).Decode(&stat); err != nil {
		return nil, err
	}
	return &stat, nil
}

func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}

// Bridge is the main storage bridge service.
type Bridge struct {
	cfg    *conf.StorageCfg
	client *IPFSClient

	ctx    context.Context
	cancel context.CancelFunc
	once   sync.Once
}

// NewBridge creates a storage bridge.
func NewBridge(cfg *conf.StorageCfg) *Bridge {
	ctx, cancel := context.WithCancel(context.Background())
	client := NewIPFSClient(
		cfg.IPFSAPIAddr,
		time.Duration(cfg.PinTimeout)*time.Second,
		time.Duration(cfg.GetTimeout)*time.Second,
		cfg.MaxGetSize,
	)
	return &Bridge{cfg: cfg, client: client, ctx: ctx, cancel: cancel}
}

// Start begins the storage bridge service.
func (b *Bridge) Start() {
	log.Info("Storage bridge started", "ipfs", b.cfg.IPFSAPIAddr, "gateway", b.cfg.GatewayURL)
}

// Stop shuts down the bridge.
func (b *Bridge) Stop() {
	b.once.Do(func() {
		b.cancel()
		log.Info("Storage bridge stopped")
	})
}

// Pin pins a CID.
func (b *Bridge) Pin(ctx context.Context, cid string) error {
	return b.client.Pin(ctx, cid)
}

// Unpin removes a pin.
func (b *Bridge) Unpin(ctx context.Context, cid string) error {
	return b.client.Unpin(ctx, cid)
}

// Get retrieves data by CID.
func (b *Bridge) Get(ctx context.Context, cid string) ([]byte, error) {
	return b.client.Get(ctx, cid)
}

// Stat returns object metadata.
func (b *Bridge) Stat(ctx context.Context, cid string) (*ObjectStat, error) {
	return b.client.Stat(ctx, cid)
}
