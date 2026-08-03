package node

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func gzipRoundTrip(t *testing.T, body []byte, status int) *httptest.ResponseRecorder {
	t.Helper()
	h := newGzipHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != 0 {
			w.WriteHeader(status)
		}
		if _, err := w.Write(body); err != nil {
			t.Errorf("handler write: %v", err)
		}
	}))
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestGzipSkipsSmallResponses is the point of the change: a JSON-RPC reply is
// typically a few dozen bytes, and handing it to gzip runs a full deflate
// reset that costs far more than the bytes it saves.
func TestGzipSkipsSmallResponses(t *testing.T) {
	body := []byte(`{"jsonrpc":"2.0","id":1,"result":"0x` + strings.Repeat("ab", 32) + `"}`)
	rec := gzipRoundTrip(t, body, 0)

	if enc := rec.Header().Get("Content-Encoding"); enc != "" {
		t.Fatalf("Content-Encoding = %q, want none for a %d-byte body", enc, len(body))
	}
	if got := rec.Body.Bytes(); !bytes.Equal(got, body) {
		t.Fatalf("body = %q, want %q", got, body)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

// TestGzipCompressesLargeResponses: the threshold must not disable compression
// for the responses that actually benefit (eth_getLogs, big blocks, traces).
func TestGzipCompressesLargeResponses(t *testing.T) {
	body := bytes.Repeat([]byte(`{"address":"0x1234","data":"0xdeadbeef"},`), 400)
	rec := gzipRoundTrip(t, body, 0)

	if enc := rec.Header().Get("Content-Encoding"); enc != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip for a %d-byte body", enc, len(body))
	}
	zr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	got, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("decompressed body differs from what the handler wrote")
	}
}

// TestGzipPreservesStatus: the header is now sent by the wrapper rather than
// the handler, so an error status must still reach the client — a 500 that
// arrives as a 200 turns a server failure into a malformed success.
func TestGzipPreservesStatus(t *testing.T) {
	for _, tc := range []struct {
		name string
		body []byte
	}{
		{"small", []byte(`{"error":"boom"}`)},
		{"large", bytes.Repeat([]byte(`{"error":"boom"},`), 200)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := gzipRoundTrip(t, tc.body, http.StatusInternalServerError)
			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500", rec.Code)
			}
		})
	}
}

// TestGzipEmptyResponse covers a handler that writes nothing: the wrapper still
// owes the client a response header.
func TestGzipEmptyResponse(t *testing.T) {
	h := newGzipHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("body = %q, want empty", rec.Body.Bytes())
	}
}

// TestGzipFlushEmitsBufferedBytes: a handler that flushes must not have its
// bytes held back in the threshold buffer.
func TestGzipFlushEmitsBufferedBytes(t *testing.T) {
	h := newGzipHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("partial"))
		w.(http.Flusher).Flush()
		_, _ = w.Write([]byte("-rest"))
	}))
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Body.String(); got != "partial-rest" {
		t.Fatalf("body = %q, want %q", got, "partial-rest")
	}
}

// TestGzipPassesThroughWithoutAcceptEncoding keeps the wrapper out of the way
// for clients that did not ask for compression.
func TestGzipPassesThroughWithoutAcceptEncoding(t *testing.T) {
	body := bytes.Repeat([]byte("x"), gzipMinSize*2)
	h := newGzipHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", nil))

	if enc := rec.Header().Get("Content-Encoding"); enc != "" {
		t.Fatalf("Content-Encoding = %q, want none", enc)
	}
	if rec.Body.Len() != len(body) {
		t.Fatalf("body length = %d, want %d", rec.Body.Len(), len(body))
	}
}

func benchmarkGzipHandler(b *testing.B, size int) {
	b.Helper()
	body := bytes.Repeat([]byte("a"), size)
	h := newGzipHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h.ServeHTTP(httptest.NewRecorder(), req)
	}
}

// BenchmarkGzipHandlerSmall is the shape that dominates a submission-heavy
// node: one transaction hash per reply.
func BenchmarkGzipHandlerSmall(b *testing.B) { benchmarkGzipHandler(b, 66) }
func BenchmarkGzipHandlerLarge(b *testing.B) { benchmarkGzipHandler(b, 64*1024) }
