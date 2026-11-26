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

// TestAppendResponsesMatchesEncoder: the direct response writer produces the
// same bytes as json.Encoder for every message it accepts, and declines the
// ones it cannot reproduce exactly.
func TestAppendResponsesMatchesEncoder(t *testing.T) {
	results := []interface{}{
		nil, 0, "plain", "<html> & \u2028 \"quoted\" é", map[string]interface{}{"b": []int{1, 2}, "a": "x<y"},
		[]interface{}{map[string]string{"hash": "0xabc"}, nil, true}, json.RawMessage(`{"k":  [1, 2]}`),
	}
	ids := []string{`1`, `-7`, `123456789012`, `"abc"`, `"a-b_c.d:e"`, `"0x1f"`}
	encode := func(v interface{}) string {
		var buf bytes.Buffer
		if err := json.NewEncoder(&buf).Encode(v); err != nil {
			t.Fatal(err)
		}
		return buf.String()
	}
	var batch []*jsonrpcMessage
	for _, id := range ids {
		for _, r := range results {
			m := (&jsonrpcMessage{ID: json.RawMessage(id)}).response(r)
			got, ok := appendResponses(nil, m)
			if !ok {
				t.Fatalf("id %s result %v: declined", id, r)
			}
			if want := encode(m); string(got) != want {
				t.Fatalf("id %s result %v:\n got %q\nwant %q", id, r, got, want)
			}
			batch = append(batch, m)
		}
	}
	got, ok := appendResponses(nil, batch)
	if !ok || string(got) != encode(batch) {
		t.Fatalf("batch mismatch (ok=%v)", ok)
	}

	declined := []interface{}{
		(&jsonrpcMessage{ID: json.RawMessage(`1`)}).errorResponse(fmt.Errorf("boom")),
		&jsonrpcMessage{Version: vsn, ID: json.RawMessage(`1`), Result: json.RawMessage(`{ "spaced" : 1 }`)},
		(&jsonrpcMessage{ID: json.RawMessage(`"a b"`)}).response(1),
		(&jsonrpcMessage{ID: json.RawMessage(`"<x>"`)}).response(1),
		(&jsonrpcMessage{ID: json.RawMessage(`1.5`)}).response(1),
		&jsonrpcMessage{Version: vsn, Method: "eth_subscription", Params: json.RawMessage(`{}`)},
		[]*jsonrpcMessage{(&jsonrpcMessage{ID: json.RawMessage(`1`)}).response(1), (&jsonrpcMessage{ID: json.RawMessage(`2`)}).errorResponse(fmt.Errorf("x"))},
		"not a message",
	}
	for i, v := range declined {
		if _, ok := appendResponses(nil, v); ok {
			t.Fatalf("declined[%d] was accepted", i)
		}
	}
}
