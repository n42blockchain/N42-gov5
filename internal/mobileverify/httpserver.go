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
	"strconv"
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
	alarms  *AlarmBuffer

	srv     *http.Server
	regRate *ipRateLimiter

	// Sybil gates for /register (design §7 item 2). powBits > 0 demands a
	// registration proof-of-work; a non-nil attestor demands (and verifies) a
	// platform device attestation. Both default off.
	powBits  int
	attestor DeviceAttestor
}

// SetAlarms wires the divergence alarm buffer so GET /mobileverify/alarms
// can serve it. Optional; nil alarms just report empty.
func (s *HTTPServer) SetAlarms(a *AlarmBuffer) { s.alarms = a }

// SetRegisterPoWBits enables the registration proof-of-work gate at the given
// difficulty (leading zero bits, capped at MaxRegisterPoWBits). 0 disables.
func (s *HTTPServer) SetRegisterPoWBits(bits int) {
	if bits > MaxRegisterPoWBits {
		bits = MaxRegisterPoWBits
	}
	s.powBits = bits
}

// SetDeviceAttestor enables the device-attestation gate. nil disables.
func (s *HTTPServer) SetDeviceAttestor(a DeviceAttestor) { s.attestor = a }

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
	mux.HandleFunc("/mobileverify/magnet/", s.handleMagnet)
	mux.HandleFunc("/mobileverify/alarms", s.handleAlarms)
	mux.HandleFunc("/mobileverify/snapshot", s.handleSnapshot)
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
	// PowNonce is the registration proof-of-work solution (decimal uint64
	// string) — required when the server enforces RegisterPoWBits (§7 item 2).
	PowNonce string `json:"pow_nonce,omitempty"`
	// Attestation is an opaque platform device-attestation blob (hex) —
	// required when the server has a DeviceAttestor configured.
	Attestation string `json:"attestation,omitempty"`
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
	// Sybil gates run BEFORE the (more expensive) PoP signature check inside
	// Register. PoW first — one hash to verify; a missing/invalid solution is
	// answered with 428 + the demanded difficulty so clients can solve+retry.
	if s.powBits > 0 {
		nonce, perr := strconv.ParseUint(req.PowNonce, 10, 64)
		if req.PowNonce == "" || perr != nil || !VerifyPoW(pubkey, nonce, s.powBits) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusPreconditionRequired)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": ErrPoWRequired.Error(), "pow_bits": s.powBits,
			})
			return
		}
	}
	if s.attestor != nil {
		att, aerr := hex.DecodeString(strings.TrimPrefix(req.Attestation, "0x"))
		if req.Attestation == "" || aerr != nil {
			httpError(w, http.StatusForbidden, ErrAttestationRequired.Error())
			return
		}
		if verr := s.attestor.VerifyDevice(pubkey, att); verr != nil {
			httpError(w, http.StatusForbidden, "device attestation rejected: "+verr.Error())
			return
		}
	}
	idx, committed, err := s.reg.Register(pubkey, pop)
	if err != nil {
		httpError(w, http.StatusForbidden, err.Error())
		return
	}
	// committed=false means "registered, effective next epoch" — report it
	// so a client knows its attestations do not count until then.
	writeJSON(w, map[string]any{"index": idx, "committed": committed})
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
	writeJSON(w, encodeCerts(certs, s.reg.IndexBound()))
}

// handleMagnet returns the swarm URI for a seeded packet (design §5b
// target form) — phones that prefer the torrent transport resolve the
// magnet here (or from any IDC node; the URI is identical everywhere
// since the torrent is derived from the packet bytes).
func (s *HTTPServer) handleMagnet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpError(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	hash, ok := hashFromPath(r.URL.Path, "/mobileverify/magnet/")
	if !ok {
		httpError(w, http.StatusBadRequest, "bad block hash")
		return
	}
	magnet, found := s.packets.Magnet(hash)
	if !found {
		httpError(w, http.StatusNotFound, "packet not seeded")
		return
	}
	writeJSON(w, map[string]string{"magnet": magnet})
}

// handleAlarms serves recent divergence alarms (design §6) newest-first.
func (s *HTTPServer) handleAlarms(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpError(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	if s.alarms == nil {
		writeJSON(w, []DivergenceAlarm{})
		return
	}
	writeJSON(w, s.alarms.Recent(100))
}

// handleSnapshot serves the committed member set for peer bulk-sync. The body
// is the raw ExportCommitted payload; the fetching peer imports it into a
// fresh registry and MUST verify the resulting root against the header-anchored
// root before trusting it (a tampered snapshot yields a different root). The
// current root is echoed in a header for a cheap pre-check.
func (s *HTTPServer) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpError(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	blob := s.reg.ExportCommitted()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Mobileverify-Root", s.reg.Root().Hex())
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(blob)
}

func (s *HTTPServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	alarmCount := 0
	if s.alarms != nil {
		alarmCount = s.alarms.Len()
	}
	writeJSON(w, map[string]any{
		"registry":    s.reg.Count(),
		"pending":     s.reg.PendingCount(),
		"accumulator": s.reg.Root().Hex(),
		"openWindows": s.windows.OpenWindows(),
		"certBlocks":  s.certs.Len(),
		"alarms":      alarmCount,
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
