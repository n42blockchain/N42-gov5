package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/ddn/gateway"
	"github.com/n42blockchain/N42/internal/ddn/provider"
	d "github.com/n42blockchain/N42/internal/ddn/types"
)

const token = "test-provider-token-12345"

func request() d.DecisionRequest {
	r := d.DecisionRequest{ChainID: 94, Version: 1, Task: "node.anomaly", SchemaID: "health-v1", InputHash: crypto.Keccak256Hash([]byte("disk full")), PrivacyMode: "public", Quorum: 1, MaxLatencyMs: 1000, MaxCost: "0", Deadline: uint64(time.Now().Add(time.Minute).UnixMilli()), Requester: "did:n42:test", Nonce: 1}
	r.RequestID, _ = r.CanonicalHash()
	return r
}
func config() Config {
	return Config{ChainID: 94, MaxConcurrency: 1, MaxInputBytes: 65536, MaxLatency: time.Second, Token: token}
}
func payload(r d.DecisionRequest, input string) []byte {
	b, _ := json.Marshal(struct {
		Request d.DecisionRequest `json:"request"`
		Input   string            `json:"input"`
	}{r, input})
	return b
}
func TestOwnedNativeHTTPGatewayRoundTrip(t *testing.T) {
	p, err := provider.NewNativeRules("did:n42:source-provider")
	if err != nil {
		t.Fatal(err)
	}
	handler, err := New(config(), p)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(handler)
	defer srv.Close()
	transport, err := provider.NewAuthenticatedSidecar(srv.URL+"/decide", p.Identity(), time.Second, token)
	if err != nil {
		t.Fatal(err)
	}
	g, err := gateway.New(gateway.Config{ChainID: 94, ShadowMode: true, MaxConcurrency: 1, QueueSize: 1, MaxItems: 8, MaxInputBytes: 65536, MaxLatency: time.Second, ReceiptTTL: time.Minute}, transport)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Stop()
	id, err := g.Submit(request(), "disk full")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		rec, err := g.GetReceipt(id)
		if err != nil {
			t.Fatal(err)
		}
		if rec.Status != "pending" {
			if rec.Receipt == nil || rec.Receipt.Result.Label != "STORAGE" || !rec.NeedEscalation {
				t.Fatal(rec)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("owned HTTP provider did not complete")
}
func TestAuthBindingsAndInfo(t *testing.T) {
	p, _ := provider.NewNativeRules("did:n42:local")
	s, _ := New(config(), p)
	cases := []struct {
		path, auth string
		req        d.DecisionRequest
		input      string
		status     int
	}{{"/decide", "", request(), "disk full", 401}, {"/decide", "Bearer wrong", request(), "disk full", 401}, {"/decide", "Bearer " + token, request(), "different", 400}, {"/decide", "Bearer " + token, request(), "disk full", 200}}
	wrong := request()
	wrong.ChainID = 95
	wrong.RequestID, _ = wrong.CanonicalHash()
	cases = append(cases, struct {
		path, auth string
		req        d.DecisionRequest
		input      string
		status     int
	}{"/decide", "Bearer " + token, wrong, "disk full", 400})
	for _, c := range cases {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", c.path, bytes.NewReader(payload(c.req, c.input)))
		r.Header.Set("Authorization", c.auth)
		s.ServeHTTP(w, r)
		if w.Code != c.status {
			t.Fatalf("%s %s: %d", c.path, c.auth, w.Code)
		}
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/info", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	s.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), p.Identity().ModelHash.Hex()) {
		t.Fatal(w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	r = httptest.NewRequest("POST", "/decide", strings.NewReader(`{"request":{},"input":"x","secret":"leak"}`))
	r.Header.Set("Authorization", "Bearer "+token)
	s.ServeHTTP(w, r)
	if w.Code != 400 || strings.Contains(w.Body.String(), "leak") {
		t.Fatal("invalid input leaked", w.Body.String())
	}
}

type blocked struct {
	provider.DecisionProvider
	entered chan struct{}
}

func (p blocked) Decide(ctx context.Context, r d.DecisionRequest, input string) (d.DecisionResult, error) {
	p.entered <- struct{}{}
	<-ctx.Done()
	return d.DecisionResult{}, ctx.Err()
}
func TestBackpressureAndDeadline(t *testing.T) {
	p, _ := provider.NewNativeRules("did:n42:local")
	entered := make(chan struct{}, 1)
	c := config()
	c.MaxLatency = 30 * time.Millisecond
	s, _ := New(c, blocked{p, entered})
	done := make(chan int, 1)
	invoke := func() int {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/decide", bytes.NewReader(payload(request(), "disk full")))
		r.Header.Set("Authorization", "Bearer "+token)
		s.ServeHTTP(w, r)
		return w.Code
	}
	go func() { done <- invoke() }()
	<-entered
	if code := invoke(); code != http.StatusTooManyRequests {
		t.Fatal("unbounded dispatch", code)
	}
	if code := <-done; code != http.StatusGatewayTimeout {
		t.Fatal("deadline did not fail", code)
	}
}
