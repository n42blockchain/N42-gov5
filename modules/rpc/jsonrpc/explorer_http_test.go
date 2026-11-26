package jsonrpc

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func gzipBytes(t *testing.T, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestServeHTTP_GzipRequestBody: a gzip-encoded request body (Blockscout's
// default for trace_*, debug_* and eth_getBlockReceipts) is decoded, single
// and batch; a broken gzip stream is a 400.
func TestServeHTTP_GzipRequestBody(t *testing.T) {
	srv := newG49HTTPServer(t)
	for _, tc := range []struct {
		name, body, want string
	}{
		{"single", `{"jsonrpc":"2.0","id":1,"method":"g49http_echo","params":["zipped"]}`, `"zipped"`},
		{"batch", `[{"jsonrpc":"2.0","id":1,"method":"g49http_echo","params":["a"]},{"jsonrpc":"2.0","id":2,"method":"g49http_echo","params":["b"]}]`, `"b"`},
	} {
		for _, enc := range []string{"gzip", "x-gzip", " GZIP "} {
			req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(gzipBytes(t, []byte(tc.body))))
			req.Header.Set("Content-Type", contentType)
			req.Header.Set("Content-Encoding", enc)
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, req)
			if w.Code != http.StatusOK || !bytes.Contains(w.Body.Bytes(), []byte(tc.want)) {
				t.Fatalf("%s/%q: status %d body %s", tc.name, enc, w.Code, w.Body.String())
			}
		}
	}

	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader([]byte("not gzip")))
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Content-Encoding", "gzip")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("broken gzip: status %d, want 400", w.Code)
	}
}

type batchProbeService struct {
	mu       sync.Mutex
	inflight int
	peak     int
}

func (s *batchProbeService) Slow(n int) int {
	s.mu.Lock()
	s.inflight++
	if s.inflight > s.peak {
		s.peak = s.inflight
	}
	s.mu.Unlock()
	time.Sleep(40 * time.Millisecond)
	s.mu.Lock()
	s.inflight--
	s.mu.Unlock()
	return n
}

// TestServeHTTP_BatchRunsConcurrentlyInOrder: the calls of one batch run
// concurrently up to batchConcurrency and the answers keep request order.
func TestServeHTTP_BatchRunsConcurrentlyInOrder(t *testing.T) {
	probe := &batchProbeService{}
	srv := NewServer()
	if err := srv.RegisterName("probe", probe); err != nil {
		t.Fatal(err)
	}
	const n = 16
	var reqs []string
	for i := 0; i < n; i++ {
		reqs = append(reqs, fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"probe_slow","params":[%d]}`, i, i))
	}
	body := "[" + strings.Join(reqs, ",") + "]"
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	w := httptest.NewRecorder()
	start := time.Now()
	srv.ServeHTTP(w, req)
	elapsed := time.Since(start)

	var answers []struct {
		ID     int `json:"id"`
		Result int `json:"result"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &answers); err != nil {
		t.Fatalf("decode %s: %v", w.Body.String(), err)
	}
	if len(answers) != n {
		t.Fatalf("got %d answers", len(answers))
	}
	for i, a := range answers {
		if a.ID != i || a.Result != i {
			t.Fatalf("answer %d = %+v, want id/result %d", i, a, i)
		}
	}
	if probe.peak < 2 || probe.peak > batchConcurrency {
		t.Fatalf("peak in-flight %d, want 2..%d", probe.peak, batchConcurrency)
	}
	serial := time.Duration(n) * 40 * time.Millisecond
	if elapsed > serial/2 {
		t.Fatalf("batch took %v, serial would be %v", elapsed, serial)
	}
}

// TestServeHTTP_BatchSerialWhenConcurrencyOne: batchConcurrency 1 keeps the
// geth order (one call at a time).
func TestServeHTTP_BatchSerialWhenConcurrencyOne(t *testing.T) {
	old := batchConcurrency
	batchConcurrency = 1
	defer func() { batchConcurrency = old }()
	probe := &batchProbeService{}
	srv := NewServer()
	if err := srv.RegisterName("probe", probe); err != nil {
		t.Fatal(err)
	}
	body := `[{"jsonrpc":"2.0","id":1,"method":"probe_slow","params":[1]},{"jsonrpc":"2.0","id":2,"method":"probe_slow","params":[2]}]`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if probe.peak != 1 {
		t.Fatalf("peak in-flight %d, want 1", probe.peak)
	}
	if !strings.Contains(w.Body.String(), `"result":2`) {
		t.Fatalf("body %s", w.Body.String())
	}
}
