package jsonrpc

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"testing"
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
