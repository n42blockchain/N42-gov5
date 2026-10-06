package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	chain "github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/ddn/provider"
	"github.com/n42blockchain/N42/internal/ddn/receipt"
	d "github.com/n42blockchain/N42/internal/ddn/types"
	"github.com/n42blockchain/N42/internal/mcp"
	"strings"
)

func identity() provider.Identity {
	return provider.Identity{DID: "did:n42:test", ModelHash: chain.Hash{1}, Tasks: []string{"node.anomaly"}, Schemas: []string{"health-v1"}}
}
func config() Config {
	return Config{ChainID: 94, ShadowMode: true, MaxConcurrency: 1, QueueSize: 1, MaxItems: 8, MaxInputBytes: 1024, MaxLatency: time.Second, ReceiptTTL: time.Minute}
}
func request(n uint64) d.DecisionRequest {
	return d.DecisionRequest{ChainID: 94, Version: 1, Task: "node.anomaly", SchemaID: "health-v1", InputHash: crypto.Keccak256Hash([]byte("healthy")), PrivacyMode: "public", Quorum: 1, MaxLatencyMs: 1000, MaxCost: "0", Deadline: uint64(time.Now().Add(time.Minute).UnixMilli()), Nonce: n, Requester: "did:n42:requester"}
}
func awaitRecord(t *testing.T, g *Gateway, id chain.Hash) *Record {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		r, err := g.GetReceipt(id)
		if err != nil {
			t.Fatal(err)
		}
		if r != nil && r.Status != "pending" {
			return r
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("decision did not finish")
	return nil
}
func TestSidecarGatewayEndToEnd(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p struct {
			Request d.DecisionRequest `json:"request"`
			Input   string            `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			t.Error(err)
		}
		if p.Input != "healthy" {
			t.Error("input mismatch")
		}
		json.NewEncoder(w).Encode(map[string]any{"request_id": p.Request.RequestID, "model_hash": identity().ModelHash, "result": d.DecisionResult{Label: "NORMAL", ConfidencePPM: 900000, ProbabilitiesPPM: []uint32{900000, 100000}}})
	}))
	defer srv.Close()
	p, err := provider.NewSidecar(srv.URL, identity(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	g, err := New(config(), p)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Stop()
	req := request(1)
	id, err := g.Submit(req, "healthy")
	if err != nil {
		t.Fatal(err)
	}
	same, err := g.Submit(req, "healthy")
	if err != nil || same != id {
		t.Fatal("idempotency failed")
	}
	rec := awaitRecord(t, g, id)
	if rec.Status != "complete" || !rec.ShadowMode || rec.Receipt.RequestID != id {
		t.Fatalf("unexpected record %+v", rec)
	}
	rec.Receipt.Result.ProbabilitiesPPM[0] = 0
	copy, _ := g.GetReceipt(id)
	if copy.Receipt.Result.ProbabilitiesPPM[0] != 900000 {
		t.Fatal("receipt leaked mutable slices")
	}
	req.Task = "wallet.risk"
	if _, err = g.Submit(req, "healthy"); err == nil {
		t.Fatal("unsupported task accepted")
	}
}

type blocking struct{ entered chan struct{} }

func (p blocking) Identity() provider.Identity { return identity() }
func (p blocking) Decide(ctx context.Context, _ d.DecisionRequest, _ string) (d.DecisionResult, error) {
	select {
	case p.entered <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return d.DecisionResult{}, ctx.Err()
}
func TestBackpressureNonceAndStop(t *testing.T) {
	p := blocking{make(chan struct{}, 1)}
	g, err := New(config(), p)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Stop()
	if _, err = g.Submit(request(1), "healthy"); err != nil {
		t.Fatal(err)
	}
	<-p.entered
	r := request(1)
	r.PolicyParameters.RequireHuman = true
	if _, err = g.Submit(r, "healthy"); err == nil {
		t.Fatal("nonce replay accepted")
	}
	if _, err = g.Submit(request(2), "healthy"); err != nil {
		t.Fatal(err)
	}
	if _, err = g.Submit(request(3), "healthy"); err != ErrBusy {
		t.Fatalf("expected bounded queue rejection, got %v", err)
	}
	g.Stop()
	if _, err = g.Submit(request(4), "healthy"); err != ErrClosed {
		t.Fatal("stopped gateway accepted work")
	}
}
func TestInputAndTimeoutChecks(t *testing.T) {
	c := config()
	c.MaxLatency = 5 * time.Millisecond
	g, _ := New(c, blocking{make(chan struct{}, 1)})
	defer g.Stop()
	r := request(1)
	if _, err := g.Submit(r, "api_key=secret-value"); err == nil {
		t.Fatal("secret input accepted")
	}
	if _, err := g.Submit(r, "different"); err == nil {
		t.Fatal("wrong input accepted")
	}
	r.PrivacyMode = "private"
	if _, err := g.Submit(r, "healthy"); err == nil {
		t.Fatal("unsupported private input accepted")
	}
	r.PrivacyMode = "public"
	id, err := g.Submit(r, "healthy")
	if err != nil {
		t.Fatal(err)
	}
	if rec := awaitRecord(t, g, id); rec.Status != "failed" || rec.Receipt != nil {
		t.Fatal("timeout manufactured receipt")
	}
}
func TestMCPAllowlistAndRPC(t *testing.T) {
	g, _ := New(config(), provider.Stub{ID: identity(), Result: d.DecisionResult{Label: "UNKNOWN", NeedEscalation: true}})
	defer g.Stop()
	srv := mcp.NewServer(nil, []string{"ddn.getReceipt"})
	RegisterMCPTools(srv, g)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, httptest.NewRequest("POST", "/", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)))
	if !strings.Contains(w.Body.String(), "ddn.getReceipt") || strings.Contains(w.Body.String(), "ddn.decide") {
		t.Fatal("MCP allowlist ignored")
	}
	api := &API{g}
	id, err := api.DdnSubmit(context.Background(), request(1), "healthy")
	if err != nil {
		t.Fatal(err)
	}
	rec := awaitRecord(t, g, id)
	if !rec.Receipt.Result.NeedEscalation {
		t.Fatal("UNKNOWN failed to escalate")
	}
}

func TestSignedShadowReceipt(t *testing.T) {
	key, _ := crypto.GenerateKey()
	signer, _ := receipt.NewSigner(key)
	defer signer.Close()
	id := identity()
	id.DID = signer.DID()
	g, err := New(config(), provider.Stub{ID: id, Result: d.DecisionResult{Label: "NORMAL", ConfidencePPM: 900000}})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Stop()
	if err = g.SetSigner(signer); err != nil {
		t.Fatal(err)
	}
	req := request(9)
	requestID, err := g.Submit(req, "healthy")
	if err != nil {
		t.Fatal(err)
	}
	rec := awaitRecord(t, g, requestID)
	if rec.Receipt == nil {
		t.Fatalf("signing failed: %+v", rec)
	}
	if err = receipt.Verify(*rec.Receipt, req, signer.Address(), uint64(time.Now().UnixMilli())); err != nil {
		t.Fatal(err)
	}
}
