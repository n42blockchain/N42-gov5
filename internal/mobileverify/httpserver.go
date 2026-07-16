// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package mobileverify

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/log"
)

// HTTPServer is the phone-facing surface (design §3, §5b transitional
// form, §5c): registration, packet fetch, receipt submission, and cert
// queries. Deliberately its own listener — mobile traffic never shares
// the consensus/RPC ports, and the whole server exists only when
// MobileVerifyCfg enables it.
//
//	POST /mobileverify/register  {"pubkey":"<48B hex>","pop":"<96B hex>"}  -> {"index":N}
//	GET  /mobileverify/packet/{blockHash}                                  -> raw StreamPacket bytes
//	POST /mobileverify/receipt   {receipt JSON, hex fields}                -> {"index":N}
//	GET  /mobileverify/cert/{blockHash}                                    -> JSON cert list
//	GET  /mobileverify/health                                              -> {"registry":N,...}
type HTTPServer struct {
	reg     *Registry
	packets *PacketService
	windows *WindowManager
	certs   *CertStore

	srv     *http.Server
	regRate *ipRateLimiter
}

// NewHTTPServer wires the server against the pipeline components.
func NewHTTPServer(addr string, reg *Registry, packets *PacketService, windows *WindowManager, certs *CertStore) *HTTPServer {
	s := &HTTPServer{
		reg:     reg,
		packets: packets,
		windows: windows,
		certs:   certs,
		regRate: newIPRateLimiter(10, time.Minute), // 10 registrations/min/IP
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/mobileverify/register", s.handleRegister)
	mux.HandleFunc("/mobileverify/packet/", s.handlePacket)
	mux.HandleFunc("/mobileverify/receipt", s.handleReceipt)
	mux.HandleFunc("/mobileverify/cert/", s.handleCert)
	mux.HandleFunc("/mobileverify/health", s.handleHealth)
	s.srv = &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	return s
}

// Start listens and serves in the background.
func (s *HTTPServer) Start() error {
	ln, err := net.Listen("tcp", s.srv.Addr)
	if err != nil {
		return fmt.Errorf("mobileverify: http listen %s: %w", s.srv.Addr, err)
	}
	go func() {
		if err := s.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("mobileverify: http server failed", "err", err)
		}
	}()
	log.Info("mobileverify: http server started", "addr", s.srv.Addr)
	return nil
}

// Stop shuts the server down gracefully.
func (s *HTTPServer) Stop() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = s.srv.Shutdown(ctx)
}

type registerRequest struct {
	Pubkey string `json:"pubkey"`
	PoP    string `json:"pop"`
}

func (s *HTTPServer) handleRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	if !s.regRate.allow(clientIP(r)) {
		httpError(w, http.StatusTooManyRequests, "registration rate limit")
		return
	}
	var req registerRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		httpError(w, http.StatusBadRequest, "bad json")
		return
	}
	var pubkey [48]byte
	var pop [96]byte
	if !hexInto(req.Pubkey, pubkey[:]) || !hexInto(req.PoP, pop[:]) {
		httpError(w, http.StatusBadRequest, "pubkey must be 48 hex bytes, pop 96")
		return
	}
	idx, err := s.reg.Register(pubkey, pop)
	if err != nil {
		httpError(w, http.StatusForbidden, err.Error())
		return
	}
	writeJSON(w, map[string]any{"index": idx})
}

func (s *HTTPServer) handlePacket(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpError(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	hash, ok := hashFromPath(r.URL.Path, "/mobileverify/packet/")
	if !ok {
		httpError(w, http.StatusBadRequest, "bad block hash")
		return
	}
	data, found := s.packets.Get(hash)
	if !found {
		httpError(w, http.StatusNotFound, "packet not in window")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	// Packets are content-addressed by block hash: immutable, cache freely
	// (the CDN transitional form of design §5b leans on exactly this).
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	_, _ = w.Write(data)
}

type receiptRequest struct {
	BlockHash    string `json:"blockHash"`
	BlockNumber  uint64 `json:"blockNumber"`
	ReceiptsRoot string `json:"receiptsRoot"`
	Pubkey       string `json:"pubkey"`
	Signature    string `json:"signature"`
	TimestampMs  uint64 `json:"timestampMs"`
}

func (s *HTTPServer) handleReceipt(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var req receiptRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil {
		httpError(w, http.StatusBadRequest, "bad json")
		return
	}
	rcpt := &Receipt{BlockNumber: req.BlockNumber, TimestampMs: req.TimestampMs}
	if !hexInto(req.BlockHash, rcpt.BlockHash[:]) ||
		!hexInto(req.ReceiptsRoot, rcpt.ComputedReceiptsRoot[:]) ||
		!hexInto(req.Pubkey, rcpt.VerifierPubkey[:]) ||
		!hexInto(req.Signature, rcpt.Signature[:]) {
		httpError(w, http.StatusBadRequest, "bad hex field lengths")
		return
	}
	idx, err := s.windows.Submit(rcpt)
	if err != nil {
		status := http.StatusForbidden
		if errors.Is(err, ErrUnknownBlock) {
			status = http.StatusNotFound
		}
		httpError(w, status, err.Error())
		return
	}
	writeJSON(w, map[string]any{"index": idx})
}

type certJSON struct {
	BlockHash      string `json:"blockHash"`
	BlockNumber    uint64 `json:"blockNumber"`
	ReceiptsRoot   string `json:"receiptsRoot"`
	AggregateSig   string `json:"aggregateSig"`
	SignerMask     string `json:"signerMask"`
	Signers        int    `json:"signers"`
	WindowClosedAt uint64 `json:"windowClosedAt"`
}

func (s *HTTPServer) handleCert(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpError(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	hash, ok := hashFromPath(r.URL.Path, "/mobileverify/cert/")
	if !ok {
		httpError(w, http.StatusBadRequest, "bad block hash")
		return
	}
	certs := s.certs.Get(hash)
	if len(certs) == 0 {
		httpError(w, http.StatusNotFound, "no certificates for block")
		return
	}
	writeJSON(w, encodeCerts(certs, s.reg.Count()))
}

func (s *HTTPServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"registry":    s.reg.Count(),
		"openWindows": s.windows.OpenWindows(),
		"certBlocks":  s.certs.Len(),
	})
}

func encodeCerts(certs []*MobileAttestationCert, registryCount int) []certJSON {
	out := make([]certJSON, 0, len(certs))
	for _, c := range certs {
		signers := 0
		if idx, err := DecodeMask(c.SignerMask, registryCount); err == nil {
			signers = len(idx)
		}
		out = append(out, certJSON{
			BlockHash:      c.BlockHash.Hex(),
			BlockNumber:    c.BlockNumber,
			ReceiptsRoot:   c.ReceiptsRoot.Hex(),
			AggregateSig:   hex.EncodeToString(c.AggregateSig[:]),
			SignerMask:     hex.EncodeToString(c.SignerMask),
			Signers:        signers,
			WindowClosedAt: c.WindowClosedAt,
		})
	}
	return out
}

// --- helpers ---

func hexInto(s string, dst []byte) bool {
	s = strings.TrimPrefix(s, "0x")
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != len(dst) {
		return false
	}
	copy(dst, b)
	return true
}

func hashFromPath(path, prefix string) (types.Hash, bool) {
	var h types.Hash
	rest := strings.TrimPrefix(path, prefix)
	if rest == path || rest == "" || strings.Contains(rest, "/") {
		return h, false
	}
	if !hexInto(rest, h[:]) {
		return h, false
	}
	return h, true
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func httpError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func clientIP(r *http.Request) string {
	// Direct connection only — deliberately NOT trusting X-Forwarded-For:
	// a spoofable header would zero out the rate limit (the stateless-serve
	// audit hit exactly this; a TrustedProxies knob can come later).
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// ipRateLimiter is a fixed-window per-IP counter — deliberately simple;
// registration is the only endpoint it guards (receipts are gated by
// signature + registration instead).
type ipRateLimiter struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	start  time.Time
	counts map[string]int
}

func newIPRateLimiter(limit int, window time.Duration) *ipRateLimiter {
	return &ipRateLimiter{limit: limit, window: window, start: time.Now(), counts: make(map[string]int)}
}

func (l *ipRateLimiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if now.Sub(l.start) > l.window {
		l.start = now
		l.counts = make(map[string]int)
	}
	l.counts[ip]++
	return l.counts[ip] <= l.limit
}
