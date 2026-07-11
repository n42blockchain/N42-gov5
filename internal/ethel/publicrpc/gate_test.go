package publicrpc

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/n42blockchain/N42/internal/ethel/rpccaps"
)

// recordingHandler is a downstream that notes whether it was reached and echoes
// the (restored) request body so the gate's body-preservation is exercised.
type recordingHandler struct {
	called bool
	body   string
}

func (h *recordingHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.called = true
	b, _ := io.ReadAll(r.Body)
	h.body = string(b)
	w.WriteHeader(http.StatusOK)
}

func post(gate http.Handler, payload string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(payload))
	rec := httptest.NewRecorder()
	gate.ServeHTTP(rec, req)
	return rec
}

// TestCapabilityGateM1 checks the rpccaps gate rejects methods M1 cannot serve
// and passes serviceable / non-rpccaps methods through with the body intact.
func TestCapabilityGateM1(t *testing.T) {
	// M1 cannot serve tx-by-hash (no global tx index) → reject.
	{
		h := &recordingHandler{}
		gate := newCapabilityGate(rpccaps.M1, nil, h)
		rec := post(gate, `{"jsonrpc":"2.0","id":1,"method":"eth_getTransactionByHash","params":["0x00"]}`)
		if h.called {
			t.Fatal("M1: eth_getTransactionByHash should be rejected, not forwarded")
		}
		if !strings.Contains(rec.Body.String(), "not available") {
			t.Fatalf("expected not-available error, got %q", rec.Body.String())
		}
	}
	// M1 serves meta → pass through, body preserved.
	{
		h := &recordingHandler{}
		gate := newCapabilityGate(rpccaps.M1, nil, h)
		payload := `{"jsonrpc":"2.0","id":2,"method":"eth_blockNumber","params":[]}`
		post(gate, payload)
		if !h.called {
			t.Fatal("M1: eth_blockNumber should be forwarded")
		}
		if h.body != payload {
			t.Fatalf("body not preserved downstream: got %q", h.body)
		}
	}
	// A method rpccaps has no opinion about (debug_) is never gated.
	{
		h := &recordingHandler{}
		gate := newCapabilityGate(rpccaps.M1, nil, h)
		post(gate, `{"jsonrpc":"2.0","id":3,"method":"debug_traceTransaction","params":["0x00"]}`)
		if !h.called {
			t.Fatal("M1: debug_traceTransaction (not in rpccaps) should be forwarded")
		}
	}
}

// TestCapabilityGateArchive checks Archive forwards everything rpccaps knows.
func TestCapabilityGateArchive(t *testing.T) {
	for _, m := range []string{"eth_getTransactionByHash", "eth_getLogs", "eth_getBalance", "eth_call"} {
		h := &recordingHandler{}
		gate := newCapabilityGate(rpccaps.Archive, nil, h)
		post(gate, `{"jsonrpc":"2.0","id":1,"method":"`+m+`","params":[]}`)
		if !h.called {
			t.Fatalf("Archive should forward %s", m)
		}
	}
}

// TestCapabilityGateFullScope checks Full mode serves latest state but rejects
// historical state via block-argument scope detection (head = 1000).
func TestCapabilityGateFullScope(t *testing.T) {
	head := func() uint64 { return 1000 }

	// eth_getBalance at "latest" → serviceable in Full.
	{
		h := &recordingHandler{}
		gate := newCapabilityGate(rpccaps.Full, head, h)
		post(gate, `{"jsonrpc":"2.0","id":1,"method":"eth_getBalance","params":["0xabc","latest"]}`)
		if !h.called {
			t.Fatal("Full: eth_getBalance@latest should be served")
		}
	}
	// eth_getBalance at an old block (0x64 = 100 < 1000) → Historical → rejected in Full.
	{
		h := &recordingHandler{}
		gate := newCapabilityGate(rpccaps.Full, head, h)
		rec := post(gate, `{"jsonrpc":"2.0","id":1,"method":"eth_getBalance","params":["0xabc","0x64"]}`)
		if h.called {
			t.Fatal("Full: eth_getBalance@historical should be rejected")
		}
		if !strings.Contains(rec.Body.String(), "not available") {
			t.Fatalf("expected not-available, got %q", rec.Body.String())
		}
	}
	// Archive serves the same historical query.
	{
		h := &recordingHandler{}
		gate := newCapabilityGate(rpccaps.Archive, head, h)
		post(gate, `{"jsonrpc":"2.0","id":1,"method":"eth_getBalance","params":["0xabc","0x64"]}`)
		if !h.called {
			t.Fatal("Archive: eth_getBalance@historical should be served")
		}
	}
	// eth_getLogs with an old fromBlock → Historical → rejected in Full.
	{
		h := &recordingHandler{}
		gate := newCapabilityGate(rpccaps.Full, head, h)
		rec := post(gate, `{"jsonrpc":"2.0","id":1,"method":"eth_getLogs","params":[{"fromBlock":"0x1","toBlock":"0x64"}]}`)
		if h.called {
			t.Fatal("Full: eth_getLogs over historical range should be rejected")
		}
		if !strings.Contains(rec.Body.String(), "not available") {
			t.Fatalf("expected not-available, got %q", rec.Body.String())
		}
	}
}

// TestCapabilityGateBatch checks a batch containing one unsupported method is
// rejected as a whole.
func TestCapabilityGateBatch(t *testing.T) {
	h := &recordingHandler{}
	gate := newCapabilityGate(rpccaps.M1, nil, h)
	rec := post(gate, `[{"jsonrpc":"2.0","id":1,"method":"eth_blockNumber","params":[]},{"jsonrpc":"2.0","id":2,"method":"eth_getTransactionByHash","params":["0x00"]}]`)
	if h.called {
		t.Fatal("batch with an unsupported method should be rejected")
	}
	if !strings.Contains(rec.Body.String(), "eth_getTransactionByHash") {
		t.Fatalf("error should name the offending method, got %q", rec.Body.String())
	}
}
