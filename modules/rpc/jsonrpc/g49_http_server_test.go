package jsonrpc

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

// g49HTTPService is a trivial service for exercising Server.ServeHTTP
// over httptest, covering newHTTPServerConn/httpServerConn and
// serveSingleRequest's allowSubscribe=false path.
type g49HTTPService struct{}

func (g49HTTPService) Echo(msg string) string { return msg }

func newG49HTTPServer(t *testing.T) *Server {
	t.Helper()
	srv := NewServer()
	if err := srv.RegisterName("g49http", g49HTTPService{}); err != nil {
		t.Fatalf("RegisterName: %v", err)
	}
	return srv
}

// TestServeHTTP_GETHealthCheck covers the GET-with-empty-body branch
// (used as a liveness probe by load balancers) — returns 200 with no
// RPC processing.
func TestServeHTTP_GETHealthCheck(t *testing.T) {
	srv := newG49HTTPServer(t)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("GET health check: got status %d, want 200", w.Code)
	}
}

// TestServeHTTP_RejectsPUTAndDELETE covers validateRequest's method
// gate.
func TestServeHTTP_RejectsPUTAndDELETE(t *testing.T) {
	srv := newG49HTTPServer(t)
	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		req := httptest.NewRequest(method, "/", bytes.NewReader([]byte("{}")))
		req.Header.Set("Content-Type", contentType)
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: got status %d, want %d", method, w.Code, http.StatusMethodNotAllowed)
		}
	}
}

// TestServeHTTP_ValidPOSTCallSucceeds drives a full JSON-RPC call
// through the real net/http handler: newHTTPServerConn wraps the
// request body/response writer into a ServerCodec, and
// serveSingleRequest dispatches it with allowSubscribe=false.
func TestServeHTTP_ValidPOSTCallSucceeds(t *testing.T) {
	srv := newG49HTTPServer(t)
	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"g49http_echo","params":["hi"]}`)
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("User-Agent", "g49-test-agent")
	req.Header.Set("Origin", "http://example.test")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("content-type"); ct != contentType {
		t.Errorf("content-type header = %q, want %q", ct, contentType)
	}
	if !bytes.Contains(w.Body.Bytes(), []byte(`"hi"`)) {
		t.Errorf("expected echoed value in response body, got: %s", w.Body.String())
	}
}

// TestServeHTTP_SubscribeOverHTTPIsRejected covers serveSingleRequest's
// allowSubscribe=false branch in handleSubscribe (ErrNotificationsUnsupported),
// reached only over the plain HTTP transport (the in-process/IPC/WS
// codecs all default allowSubscribe=true).
func TestServeHTTP_SubscribeOverHTTPIsRejected(t *testing.T) {
	srv := NewServer()
	if err := srv.RegisterName("g49", &g49PubSubService{}); err != nil {
		t.Fatalf("RegisterName: %v", err)
	}
	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"g49_subscribe","params":["ticks",1]}`)
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if !bytes.Contains(w.Body.Bytes(), []byte("notifications not supported")) {
		t.Errorf("expected a notifications-not-supported error, got: %s", w.Body.String())
	}
}
