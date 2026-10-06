// Package gateway runs opt-in DDN work independently of block execution.
package gateway

import (
	"context"
	"errors"
	"fmt"
	metrics "github.com/n42blockchain/N42/common/metrics"
	"github.com/n42blockchain/N42/log"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	chain "github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/ddn/provider"
	d "github.com/n42blockchain/N42/internal/ddn/types"
)

var ErrBusy = errors.New("DDN capacity exhausted")
var ErrClosed = errors.New("DDN gateway stopped")

type Config struct {
	ChainID                                            uint64
	ShadowMode                                         bool
	MaxConcurrency, QueueSize, MaxItems, MaxInputBytes int
	MaxLatency                                         time.Duration
	ReceiptTTL                                         time.Duration
}
type Record struct {
	Status     string             `json:"status"`
	Receipt    *d.DecisionReceipt `json:"receipt"`
	Error      string             `json:"error"`
	ShadowMode bool               `json:"shadow_mode"`
}
type Metrics struct {
	Requests, Completed, Errors, Escalations, Dropped atomic.Uint64
	LatencyMs                                         atomic.Uint64
}
type item struct {
	r     d.DecisionRequest
	input string
}
type entry struct {
	record   Record
	expires  uint64
	nonceKey string
}
type ReceiptSigner interface {
	DID() string
	Sign(d.DecisionReceipt) (d.DecisionReceipt, error)
}

type Gateway struct {
	signer   ReceiptSigner
	cfg      Config
	provider provider.DecisionProvider
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	mu       sync.Mutex
	closed   bool
	records  map[chain.Hash]entry
	nonces   map[string]chain.Hash
	queue    chan item
	Metrics  Metrics
}

func New(cfg Config, p provider.DecisionProvider) (*Gateway, error) {
	if !cfg.ShadowMode {
		return nil, errors.New("DDN v1 gateway supports shadow mode only")
	}
	if p == nil || cfg.ChainID == 0 || cfg.MaxConcurrency < 1 || cfg.MaxConcurrency > 64 || cfg.QueueSize < 1 || cfg.QueueSize > 4096 || cfg.MaxItems < 1 || cfg.MaxItems > 100000 || cfg.MaxInputBytes < 1 || cfg.MaxInputBytes > 1<<20 || cfg.MaxLatency <= 0 || cfg.MaxLatency > time.Minute || cfg.ReceiptTTL <= 0 || cfg.ReceiptTTL > 24*time.Hour {
		return nil, errors.New("invalid bounded DDN configuration")
	}
	id := p.Identity()
	if id.DID == "" || id.ModelHash == (chain.Hash{}) {
		return nil, errors.New("provider identity and model hash required")
	}
	ctx, cancel := context.WithCancel(context.Background())
	g := &Gateway{cfg: cfg, provider: p, ctx: ctx, cancel: cancel, records: map[chain.Hash]entry{}, nonces: map[string]chain.Hash{}, queue: make(chan item, cfg.QueueSize)}
	for i := 0; i < cfg.MaxConcurrency; i++ {
		g.wg.Add(1)
		go g.worker()
	}
	return g, nil
}

var secrets = regexp.MustCompile(`(?i)(bearer\s+[^\s]+|(?:api[_-]?key|authorization|password|secret|token)\s*[:=]\s*[^\s,;]+|(?:0x)?[0-9a-f]{64})`)

func Redact(input string) string { return secrets.ReplaceAllString(input, "[REDACTED]") }
func (g *Gateway) Submit(r d.DecisionRequest, input string) (chain.Hash, error) {
	now := uint64(time.Now().UnixMilli())
	if len(input) > g.cfg.MaxInputBytes || !utf8.ValidString(input) {
		return chain.Hash{}, errors.New("DDN input too large")
	}
	if Redact(input) != input {
		return chain.Hash{}, errors.New("DDN input requires redaction before hashing")
	}
	if err := r.Validate(now); err != nil {
		return chain.Hash{}, err
	}
	if r.ChainID != g.cfg.ChainID {
		return chain.Hash{}, errors.New("DDN chain mismatch")
	}
	if r.PrivacyMode != "public" || r.InputLocation != "" {
		return chain.Hash{}, errors.New("v1 HTTP gateway accepts public inline input only")
	}
	if crypto.Keccak256Hash([]byte(input)) != r.InputHash {
		return chain.Hash{}, errors.New("DDN input hash mismatch")
	}
	id := g.provider.Identity()
	if r.Quorum != 1 {
		return chain.Hash{}, errors.New("single-provider gateway requires quorum 1")
	}
	if r.ModelRequirements.ModelHash != (chain.Hash{}) && r.ModelRequirements.ModelHash != id.ModelHash {
		return chain.Hash{}, errors.New("required model unavailable")
	}
	if r.ModelRequirements.Family != "" && r.ModelRequirements.Family != id.Family {
		return chain.Hash{}, errors.New("required model family unavailable")
	}
	if !contains(id.Tasks, r.Task) || !contains(id.Schemas, r.SchemaID) {
		return chain.Hash{}, errors.New("unsupported task/schema")
	}
	r.RequestID, _ = r.CanonicalHash()
	// Copy array-bearing request fields if the schema grows in a later version.
	key := fmt.Sprintf("%s/%d", r.Requester, r.Nonce)
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return chain.Hash{}, ErrClosed
	}
	g.pruneLocked(now)
	if _, ok := g.records[r.RequestID]; ok {
		return r.RequestID, nil
	}
	if _, ok := g.nonces[key]; ok {
		return chain.Hash{}, errors.New("DDN nonce already used")
	}
	if len(g.records) >= g.cfg.MaxItems {
		g.Metrics.Dropped.Add(1)
		metrics.GetOrCreateCounter("ddn_dropped_total", false).Inc()
		return chain.Hash{}, ErrBusy
	}
	select {
	case g.queue <- item{r, input}:
		g.records[r.RequestID] = entry{Record{Status: "pending", ShadowMode: true}, r.Deadline, key}
		g.nonces[key] = r.RequestID
		g.Metrics.Requests.Add(1)
		metrics.GetOrCreateCounter("ddn_requests_total", false).Inc()
		return r.RequestID, nil
	default:
		g.Metrics.Dropped.Add(1)
		metrics.GetOrCreateCounter("ddn_dropped_total", false).Inc()
		return chain.Hash{}, ErrBusy
	}
}
func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}
func (g *Gateway) worker() {
	defer g.wg.Done()
	for {
		select {
		case <-g.ctx.Done():
			return
		case task := <-g.queue:
			g.execute(task)
		}
	}
}
func (g *Gateway) execute(t item) {
	started := time.Now()
	deadline := time.UnixMilli(int64(t.r.Deadline))
	limit := g.cfg.MaxLatency
	if l := time.Duration(t.r.MaxLatencyMs) * time.Millisecond; l < limit {
		limit = l
	}
	if dline := started.Add(limit); dline.Before(deadline) {
		deadline = dline
	}
	ctx, cancel := context.WithDeadline(g.ctx, deadline)
	defer cancel()
	var result d.DecisionResult
	var err error
	func() {
		defer func() {
			if recover() != nil {
				err = errors.New("provider panic")
			}
		}()
		result, err = g.provider.Decide(ctx, t.r, t.input)
	}()
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err == nil {
		err = result.Validate()
	}
	finished := time.Now()
	elapsed := uint64(finished.Sub(started).Milliseconds())
	rec := Record{Status: "complete", ShadowMode: true}
	if err != nil {
		rec.Status = "failed"
		rec.Error = "provider unavailable or invalid response"
		g.Metrics.Errors.Add(1)
		metrics.GetOrCreateCounter("ddn_provider_errors_total", false).Inc()
	} else {
		id := g.provider.Identity()
		result.NeedEscalation = result.NeedEscalation || result.ConfidencePPM < t.r.PolicyParameters.MinConfidencePPM || t.r.PolicyParameters.RequireHuman || strings.EqualFold(result.Label, "UNKNOWN") || strings.EqualFold(result.Label, "ABSTAIN")
		expiry := uint64(finished.Add(g.cfg.ReceiptTTL).UnixMilli())
		if expiry > t.r.Deadline {
			expiry = t.r.Deadline
		}
		rec.Receipt = &d.DecisionReceipt{ChainID: t.r.ChainID, Version: d.Version, RequestID: t.r.RequestID, ProviderDID: id.DID, Model: id.Model, ModelVersion: id.ModelVersion, ModelHash: id.ModelHash, PolicyHash: t.r.PolicyHash, InputHash: t.r.InputHash, Result: result, StartedAt: uint64(started.UnixMilli()), CompletedAt: uint64(finished.UnixMilli()), LatencyMs: elapsed, Expiry: expiry, Nonce: t.r.Nonce}

		g.mu.Lock()
		signer := g.signer
		g.mu.Unlock()
		if signer != nil {
			signed, signErr := signer.Sign(*rec.Receipt)
			if signErr != nil {
				rec.Status = "failed"
				rec.Error = "receipt signing failed"
				rec.Receipt = nil
				g.Metrics.Errors.Add(1)
			} else {
				rec.Receipt = &signed
			}
		}
		g.Metrics.Completed.Add(1)
		g.Metrics.LatencyMs.Add(elapsed)
		metrics.GetOrCreateHistogram("ddn_provider_latency_seconds").Observe(finished.Sub(started).Seconds())
		if strings.EqualFold(result.Label, "UNKNOWN") {
			metrics.GetOrCreateCounter("ddn_unknown_total", false).Inc()
		}
		if result.NeedEscalation {
			g.Metrics.Escalations.Add(1)
			metrics.GetOrCreateCounter("ddn_escalations_total", false).Inc()
		}
	}
	log.Info("DDN shadow receipt", "request_id", t.r.RequestID, "provider", g.provider.Identity().DID, "model_hash", g.provider.Identity().ModelHash, "status", rec.Status, "latency_ms", elapsed)
	g.mu.Lock()
	if e, ok := g.records[t.r.RequestID]; ok {
		e.record = rec
		g.records[t.r.RequestID] = e
	}
	g.mu.Unlock()
}
func (g *Gateway) GetReceipt(id chain.Hash) (*Record, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.pruneLocked(uint64(time.Now().UnixMilli()))
	e, ok := g.records[id]
	if !ok {
		return nil, nil
	}
	r := e.record
	if r.Receipt != nil {
		c := *r.Receipt
		c.Result.ProbabilitiesPPM = append([]uint32{}, c.Result.ProbabilitiesPPM...)
		c.Result.Answers = append([]d.QuantizedAnswer{}, c.Result.Answers...)
		for i := range c.Result.Answers {
			c.Result.Answers[i].ProbabilitiesPPM = append([]uint32{}, c.Result.Answers[i].ProbabilitiesPPM...)
		}
		r.Receipt = &c
	}
	return &r, nil
}
func (g *Gateway) pruneLocked(now uint64) {
	for id, e := range g.records {
		if e.expires <= now {
			delete(g.records, id)
			delete(g.nonces, e.nonceKey)
		}
	}
}
func (g *Gateway) Stop() { g.mu.Lock(); g.closed = true; g.cancel(); g.mu.Unlock(); g.wg.Wait() }

// SetSigner installs an explicit DDN signer whose DID matches the configured
// provider identity. It does not borrow any consensus or wallet keys.
func (g *Gateway) SetSigner(s ReceiptSigner) error {
	if s == nil || s.DID() != g.provider.Identity().DID {
		return errors.New("DDN signer/provider DID mismatch")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return ErrClosed
	}
	g.signer = s
	return nil
}
