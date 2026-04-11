// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// webrtc_fetcher.go — WebRTC data-channel Fetcher implementation.
//
// WebRTCFetcher implements the "variant B" transport from WEBRTC_NOTES:
// a simple HTTPS request-response signaling exchange followed by a
// one-way DataChannel stream from a single origin sender. It is not
// WebTorrent — there is no swarm, tracker or mesh. The shape matches
// HTTPFetcher and TorrentFetcher so MultiSourceFetcher can dispatch it
// transparently, and the on-disk artifact is written atomically via
// .part → rename after SHA256 verification.

package fetch

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/pion/webrtc/v4"

	"github.com/n42blockchain/N42/log"
)

// WebRTCFetcher implements Fetcher for WebRTC data-channel sources. The
// transport is "variant B" from WEBRTC_NOTES.md: a simple request-
// response signaling exchange over HTTPS followed by a one-way
// DataChannel stream. This is NOT WebTorrent — there is no swarm,
// tracker, or peer-to-peer mesh. The origin is a single sender
// running on a server we control.
//
// The shape matches HTTPFetcher / TorrentFetcher so the
// MultiSourceFetcher dispatches transparently: Kinds() returns
// SourceWebRTC, Fetch takes exactly one Source, and the on-disk
// artifact is written atomically via `.part → rename` after SHA256
// verification.
//
// Why variant B and not WebTorrent: a full WebTorrent bridge requires
// a tracker client, the BitTorrent wire protocol over DataChannels,
// and swarm management. That is multiple weeks of protocol work. The
// direct DataChannel shape implemented here is ~300 lines, ships
// today, and covers the "CDN is blocked, BT is blocked, but WebRTC
// punches through" case that motivated the feature request in the
// first place. When the strategic need for browser seeders arrives,
// add a second WebRTCFetcher variant or promote this to a tagged
// interface — the MultiSourceFetcher dispatch does not care.
type WebRTCFetcher struct {
	opts WebRTCFetcherOptions
}

// WebRTCFetcherOptions tunes a WebRTCFetcher.
type WebRTCFetcherOptions struct {
	// Client is the HTTP client used for signaling. nil → a fresh
	// client with a 30s timeout, which covers SDP exchange but not
	// DataChannel transfer (the transfer happens after signaling
	// completes and does not go through this client).
	Client *http.Client

	// ICEServers is the STUN / TURN configuration for the peer
	// connection. Nil falls back to a public Google STUN endpoint,
	// which is good enough for NAT traversal in most environments but
	// should be replaced with a self-hosted STUN/TURN for production.
	ICEServers []webrtc.ICEServer

	// GatherTimeout caps how long the fetcher waits for ICE gathering
	// to finish before sending the SDP offer. Default 10s. Non-trickle
	// ICE is used for simplicity: a single offer carries every local
	// candidate, and a single answer carries every remote candidate.
	GatherTimeout time.Duration

	// DataTimeout caps the total DataChannel transfer duration. The
	// per-asset budget should exceed the expected transfer time plus
	// a safety margin. Default 30 minutes.
	DataTimeout time.Duration

	// ProgressInterval throttles ProgressFunc invocations. Default 1s.
	ProgressInterval time.Duration
}

// NewWebRTCFetcher constructs a WebRTCFetcher with the given options.
// Zero fields fall back to documented defaults.
func NewWebRTCFetcher(opts WebRTCFetcherOptions) *WebRTCFetcher {
	if opts.Client == nil {
		opts.Client = &http.Client{Timeout: 30 * time.Second}
	}
	if opts.ICEServers == nil {
		opts.ICEServers = []webrtc.ICEServer{
			{URLs: []string{"stun:stun.l.google.com:19302"}},
		}
	}
	if opts.GatherTimeout <= 0 {
		opts.GatherTimeout = 10 * time.Second
	}
	if opts.DataTimeout <= 0 {
		opts.DataTimeout = 30 * time.Minute
	}
	if opts.ProgressInterval <= 0 {
		opts.ProgressInterval = time.Second
	}
	return &WebRTCFetcher{opts: opts}
}

// Kinds reports WebRTC support.
func (f *WebRTCFetcher) Kinds() []SourceKind {
	return []SourceKind{SourceWebRTC}
}

// signalingOffer is the JSON envelope sent to the signaling endpoint.
type signalingOffer struct {
	Type  string `json:"type"`  // always "offer"
	SDP   string `json:"sdp"`   // local description's SDP
	Asset string `json:"asset"` // asset.Name, so the server knows what to serve
}

// signalingAnswer is the JSON envelope the signaling endpoint returns.
type signalingAnswer struct {
	Type string `json:"type"` // always "answer"
	SDP  string `json:"sdp"`  // remote description's SDP
}

// Fetch downloads asset via WebRTC. The Source URI is the signaling
// URL (HTTPS POST endpoint).
func (f *WebRTCFetcher) Fetch(ctx context.Context, asset Asset, dstDir string, progress ProgressFunc) error {
	if err := asset.Validate(); err != nil {
		return err
	}
	if len(asset.Sources) != 1 {
		return fmt.Errorf("webrtc fetcher: expected exactly one source per Fetch call, got %d", len(asset.Sources))
	}
	src := asset.Sources[0]
	if src.Kind != SourceWebRTC {
		return fmt.Errorf("webrtc fetcher: unsupported source kind %q", src.Kind)
	}

	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return fmt.Errorf("webrtc fetcher: mkdir %s: %w", dstDir, err)
	}

	finalPath := filepath.Join(dstDir, asset.Name)
	if ok, _ := verifyExistingFile(finalPath, asset); ok {
		log.Debug("download: existing file already matches, skipping",
			"asset", asset.Name, "path", finalPath)
		return nil
	}

	partPath := finalPath + ".part"
	if err := os.Remove(partPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove stale .part: %w", err)
	}

	return f.fetchOnce(ctx, asset, src.URI, partPath, finalPath, progress)
}

// fetchOnce runs the full signaling + transfer cycle once. A retry
// loop on top would need the whole PeerConnection dance repeated, so
// this method owns every goroutine it spawns and cleans up on every
// exit path.
func (f *WebRTCFetcher) fetchOnce(ctx context.Context, asset Asset, signalingURL, partPath, finalPath string, progress ProgressFunc) error {
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{
		ICEServers: f.opts.ICEServers,
	})
	if err != nil {
		return fmt.Errorf("new peer connection: %w", err)
	}
	defer pc.Close()

	// The DataChannel is created on OUR side so the sender can see it
	// in OnDataChannel and attach its stream. Ordered + reliable are
	// the defaults for DataChannels in pion.
	dc, err := pc.CreateDataChannel("asset", nil)
	if err != nil {
		return fmt.Errorf("create data channel: %w", err)
	}

	out, err := os.OpenFile(partPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("open .part: %w", err)
	}
	// Close is handled explicitly at the commit point below so we can
	// surface rename errors; defer is a belt-and-braces.
	defer out.Close()

	hasher := sha256.New()
	pw := &progressWriter{
		dst:      io.MultiWriter(out, hasher),
		assetSiz: asset.SizeBytes,
		assetNam: asset.Name,
		source:   signalingURL,
		progress: progress,
		interval: f.opts.ProgressInterval,
	}

	dataDone := make(chan error, 1)
	dc.OnOpen(func() {
		log.Debug("webrtc: datachannel open", "asset", asset.Name)
	})
	dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		if _, err := pw.Write(msg.Data); err != nil {
			select {
			case dataDone <- err:
			default:
			}
		}
	})
	dc.OnClose(func() {
		select {
		case dataDone <- nil:
		default:
		}
	})
	dc.OnError(func(err error) {
		select {
		case dataDone <- err:
		default:
		}
	})

	// 1. Create SDP offer, set local description, wait for ICE
	//    gathering to finish. Non-trickle ICE keeps the signaling
	//    protocol trivial (one POST / one response).
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		return fmt.Errorf("create offer: %w", err)
	}
	if err := pc.SetLocalDescription(offer); err != nil {
		return fmt.Errorf("set local description: %w", err)
	}

	gatherCtx, cancelGather := context.WithTimeout(ctx, f.opts.GatherTimeout)
	defer cancelGather()
	gatherComplete := webrtc.GatheringCompletePromise(pc)
	select {
	case <-gatherComplete:
	case <-gatherCtx.Done():
		return fmt.Errorf("ice gathering: %w", gatherCtx.Err())
	}

	localDesc := pc.LocalDescription()
	if localDesc == nil {
		return errors.New("webrtc fetcher: no local description after gathering")
	}

	// 2. POST the finalised offer to the signaling endpoint. The
	//    server is expected to return a matching answer synchronously.
	answer, err := f.exchangeSignaling(ctx, signalingURL, asset.Name, localDesc.SDP)
	if err != nil {
		return fmt.Errorf("signaling: %w", err)
	}
	if err := pc.SetRemoteDescription(webrtc.SessionDescription{
		Type: webrtc.SDPTypeAnswer,
		SDP:  answer.SDP,
	}); err != nil {
		return fmt.Errorf("set remote description: %w", err)
	}

	// 3. Wait for DataChannel transfer to complete. Abort if the
	//    transfer budget is exhausted or the caller cancels.
	dataCtx, cancelData := context.WithTimeout(ctx, f.opts.DataTimeout)
	defer cancelData()
	select {
	case err := <-dataDone:
		if err != nil {
			_ = os.Remove(partPath)
			return fmt.Errorf("datachannel: %w", err)
		}
	case <-dataCtx.Done():
		_ = os.Remove(partPath)
		return fmt.Errorf("datachannel transfer: %w", dataCtx.Err())
	}

	pw.flush()
	if err := out.Close(); err != nil {
		_ = os.Remove(partPath)
		return fmt.Errorf("close .part: %w", err)
	}

	// 4. Verify bytes and commit atomically.
	if pw.current != asset.SizeBytes {
		_ = os.Remove(partPath)
		return fmt.Errorf("webrtc fetcher: short transfer: got %d, want %d", pw.current, asset.SizeBytes)
	}
	if !isZeroHash(asset.SHA256) {
		var got [32]byte
		copy(got[:], hasher.Sum(nil))
		if got != asset.SHA256 {
			_ = os.Remove(partPath)
			return fmt.Errorf("%w: %s", ErrChecksumMismatch, asset.Name)
		}
	}
	if err := os.Rename(partPath, finalPath); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// exchangeSignaling POSTs a signalingOffer to url and decodes the
// signalingAnswer the server returns. The HTTP client is the one
// configured on the fetcher options, which means the same TLS /
// timeout settings apply uniformly across all WebRTC sources.
func (f *WebRTCFetcher) exchangeSignaling(ctx context.Context, url, assetName, sdp string) (*signalingAnswer, error) {
	body, err := json.Marshal(signalingOffer{
		Type:  "offer",
		SDP:   sdp,
		Asset: assetName,
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := f.opts.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("signaling %s: status %d", url, resp.StatusCode)
	}
	var answer signalingAnswer
	if err := json.NewDecoder(resp.Body).Decode(&answer); err != nil {
		return nil, fmt.Errorf("decode signaling answer: %w", err)
	}
	if answer.Type != "answer" || answer.SDP == "" {
		return nil, fmt.Errorf("signaling: unexpected answer %+v", answer)
	}
	return &answer, nil
}
