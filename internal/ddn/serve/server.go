// Package serve exposes repository-native providers over bounded HTTP. It does
// not fetch models, run third-party processes, sign receipts or alter node state.
package serve

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	chain "github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/ddn/gateway"
	"github.com/n42blockchain/N42/internal/ddn/provider"
	d "github.com/n42blockchain/N42/internal/ddn/types"
)

type Config struct {
	ChainID        uint64
	MaxConcurrency int
	MaxInputBytes  int
	MaxLatency     time.Duration
	Token          string
}
type Server struct {
	cfg     Config
	backend provider.DecisionProvider
	permits chan struct{}
	token   [32]byte
}

func New(c Config, p provider.DecisionProvider) (*Server, error) {
	if p == nil || c.ChainID == 0 || c.MaxConcurrency < 1 || c.MaxConcurrency > 64 || c.MaxInputBytes < 1 || c.MaxInputBytes > 65536 || c.MaxLatency <= 0 || c.MaxLatency > time.Minute {
		return nil, errors.New("invalid native provider bounds")
	}
	if c.Token != "" && (len(c.Token) < 16 || len(c.Token) > 256 || strings.ContainsAny(c.Token, "\r\n\t ")) {
		return nil, errors.New("invalid provider bearer token")
	}
	return &Server{cfg: c, backend: p, permits: make(chan struct{}, c.MaxConcurrency), token: sha256.Sum256([]byte(c.Token))}, nil
}
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Token != "" {
		auth := r.Header.Get("Authorization")
		candidate := ""
		if strings.HasPrefix(auth, "Bearer ") {
			candidate = strings.TrimPrefix(auth, "Bearer ")
		}
		digest := sha256.Sum256([]byte(candidate))
		if subtle.ConstantTimeCompare(s.token[:], digest[:]) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Path == "/info" && r.Method == http.MethodGet {
		labels := []string{}
		if p, ok := s.backend.(interface{ Labels() []string }); ok {
			labels = p.Labels()
		}
		json.NewEncoder(w).Encode(struct {
			Identity provider.Identity `json:"identity"`
			Labels   []string          `json:"labels"`
		}{s.backend.Identity(), labels})
		return
	}
	if r.URL.Path != "/decide" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	select {
	case s.permits <- struct{}{}:
		defer func() { <-s.permits }()
	default:
		http.Error(w, "provider busy", http.StatusTooManyRequests)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	defer r.Body.Close()
	var payload struct {
		Request d.DecisionRequest `json:"request"`
		Input   string            `json:"input"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&payload); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if dec.Decode(new(any)) != io.EOF {
		http.Error(w, "trailing request", http.StatusBadRequest)
		return
	}
	req := payload.Request
	input := payload.Input
	now := uint64(time.Now().UnixMilli())
	if req.Validate(now) != nil || req.ChainID != s.cfg.ChainID || req.PrivacyMode != "public" || req.InputLocation != "" || len(input) > s.cfg.MaxInputBytes || !utf8.ValidString(input) || gateway.Redact(input) != input || crypto.Keccak256Hash([]byte(input)) != req.InputHash {
		http.Error(w, "request binding or input rejected", http.StatusBadRequest)
		return
	}
	id := s.backend.Identity()
	if !contains(id.Tasks, req.Task) || !contains(id.Schemas, req.SchemaID) {
		http.Error(w, "unsupported task/schema", http.StatusBadRequest)
		return
	}
	if req.Quorum == 1 && ((req.ModelRequirements.ModelHash != ([32]byte{}) && req.ModelRequirements.ModelHash != id.ModelHash) || (req.ModelRequirements.Family != "" && req.ModelRequirements.Family != id.Family)) {
		http.Error(w, "model requirement mismatch", http.StatusBadRequest)
		return
	}
	req.RequestID, _ = req.CanonicalHash()
	limit := s.cfg.MaxLatency
	if max := time.Duration(req.MaxLatencyMs) * time.Millisecond; max < limit {
		limit = max
	}
	deadline := time.Now().Add(limit)
	if dline := time.UnixMilli(int64(req.Deadline)); dline.Before(deadline) {
		deadline = dline
	}
	ctx, cancel := context.WithDeadline(r.Context(), deadline)
	defer cancel()
	result, err := s.backend.Decide(ctx, req, input)
	if ctx.Err() != nil {
		http.Error(w, "decision deadline exceeded", http.StatusGatewayTimeout)
		return
	}
	if err != nil || result.Validate() != nil {
		http.Error(w, "decision unavailable", http.StatusBadGateway)
		return
	}
	result = result.EnforcePolicy(req)
	json.NewEncoder(w).Encode(struct {
		RequestID chain.Hash       `json:"request_id"`
		ModelHash chain.Hash       `json:"model_hash"`
		Result    d.DecisionResult `json:"result"`
	}{req.RequestID, id.ModelHash, result})
}
func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}
