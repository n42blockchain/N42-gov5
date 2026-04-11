// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package fetch

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
)

// TestWebRTCFetcher_KindsContract pins that WebRTCFetcher advertises
// exactly SourceWebRTC so the MultiSourceFetcher dispatch stays
// predictable.
func TestWebRTCFetcher_KindsContract(t *testing.T) {
	f := NewWebRTCFetcher(WebRTCFetcherOptions{})
	kinds := f.Kinds()
	if len(kinds) != 1 || kinds[0] != SourceWebRTC {
		t.Fatalf("Kinds: got %v, want [webrtc]", kinds)
	}
}

// TestWebRTCFetcher_RejectsWrongKind confirms the dispatch guard.
func TestWebRTCFetcher_RejectsWrongKind(t *testing.T) {
	f := NewWebRTCFetcher(WebRTCFetcherOptions{})
	asset := Asset{
		Name:      "x",
		SizeBytes: 1,
		Sources:   []Source{{Kind: SourceHTTPS, URI: "https://x"}},
	}
	err := f.Fetch(context.Background(), asset, t.TempDir(), nil)
	if err == nil || !strings.Contains(err.Error(), "unsupported source kind") {
		t.Fatalf("expected unsupported source kind, got %v", err)
	}
}

// webrtcSender stands up a pion PeerConnection in the test process
// that plays the role of the remote origin. Given an SDP offer, it:
//
//   - Parses the offer and installs it as the remote description,
//   - Intercepts the client's DataChannel via OnDataChannel,
//   - When the channel opens, streams payload bytes through it,
//   - Closes the channel to signal EOF.
//
// This lets the end-to-end test exercise the real WebRTC stack
// without needing an external server.
type webrtcSender struct {
	t       *testing.T
	payload []byte
}

func (s *webrtcSender) handleOffer(offerSDP string) (string, error) {
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return "", err
	}

	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		dc.OnOpen(func() {
			// Send payload in small chunks so the receiver's
			// progressWriter updates more than once.
			chunkSize := 256
			for off := 0; off < len(s.payload); off += chunkSize {
				end := off + chunkSize
				if end > len(s.payload) {
					end = len(s.payload)
				}
				if err := dc.Send(s.payload[off:end]); err != nil {
					s.t.Errorf("sender Send: %v", err)
					return
				}
			}
			// Flush by closing the channel — receiver's OnClose
			// wakes up and completes the transfer.
			_ = dc.Close()
		})
	})

	if err := pc.SetRemoteDescription(webrtc.SessionDescription{
		Type: webrtc.SDPTypeOffer,
		SDP:  offerSDP,
	}); err != nil {
		return "", err
	}

	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		return "", err
	}
	if err := pc.SetLocalDescription(answer); err != nil {
		return "", err
	}
	gatherComplete := webrtc.GatheringCompletePromise(pc)
	select {
	case <-gatherComplete:
	case <-time.After(10 * time.Second):
		return "", errTimeout("gather")
	}
	return pc.LocalDescription().SDP, nil
}

// errTimeout is a small helper so the test doesn't pull in errors
// package just for a message.
type errTimeout string

func (e errTimeout) Error() string { return "timeout: " + string(e) }

// TestWebRTCFetcher_EndToEnd wires a pion sender into the httptest
// signaling server and drives a full fetch. It verifies:
//
//   - SDP exchange works through the HTTPS signaling endpoint,
//   - The DataChannel transfers every byte of the payload,
//   - SHA256 is verified on completion,
//   - The partial file is committed atomically to the final path.
func TestWebRTCFetcher_EndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("pulls in full WebRTC stack; skipped in -short mode")
	}

	payload := makePayload(4096)
	sender := &webrtcSender{t: t, payload: payload}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var offer signalingOffer
		if err := json.NewDecoder(r.Body).Decode(&offer); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if offer.Type != "offer" || offer.SDP == "" {
			http.Error(w, "bad offer", http.StatusBadRequest)
			return
		}
		answerSDP, err := sender.handleOffer(offer.SDP)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(signalingAnswer{
			Type: "answer",
			SDP:  answerSDP,
		})
	}))
	defer srv.Close()

	f := NewWebRTCFetcher(WebRTCFetcherOptions{
		Client:        srv.Client(),
		GatherTimeout: 20 * time.Second,
		DataTimeout:   20 * time.Second,
	})

	asset := Asset{
		Name:      "file.bin",
		SizeBytes: uint64(len(payload)),
		SHA256:    sha256.Sum256(payload),
		Sources:   []Source{{Kind: SourceWebRTC, URI: srv.URL}},
	}
	dir := t.TempDir()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := f.Fetch(ctx, asset, dir, nil); err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dir, "file.bin"))
	if err != nil {
		t.Fatalf("read final: %v", err)
	}
	if len(got) != len(payload) {
		t.Fatalf("len: got %d, want %d", len(got), len(payload))
	}
	if sha256.Sum256(got) != asset.SHA256 {
		t.Fatalf("content mismatch")
	}
	// The .part file should have been renamed away.
	if _, err := os.Stat(filepath.Join(dir, "file.bin.part")); err == nil {
		t.Fatalf(".part file should be absent after successful commit")
	}
}

// TestWebRTCFetcher_SignalingServerError confirms the fetcher
// surfaces a signaling-server 500 with a meaningful wrap.
func TestWebRTCFetcher_SignalingServerError(t *testing.T) {
	if testing.Short() {
		t.Skip("pulls in full WebRTC stack; skipped in -short mode")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream exploded", http.StatusInternalServerError)
	}))
	defer srv.Close()

	f := NewWebRTCFetcher(WebRTCFetcherOptions{
		Client:        srv.Client(),
		GatherTimeout: 5 * time.Second,
	})
	asset := Asset{
		Name:      "x",
		SizeBytes: 1,
		Sources:   []Source{{Kind: SourceWebRTC, URI: srv.URL}},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	err := f.Fetch(ctx, asset, t.TempDir(), nil)
	if err == nil {
		t.Fatal("expected signaling error, got nil")
	}
	if !strings.Contains(err.Error(), "status 500") {
		t.Fatalf("expected status 500 in error, got %v", err)
	}
}

// Unused identifier silencer so imports stay clean if the test file
// is temporarily trimmed during debugging.
var _ = io.Copy
