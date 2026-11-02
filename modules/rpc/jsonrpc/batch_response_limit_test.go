package jsonrpc

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type bigResultService struct{ calls int }

func (s *bigResultService) Big(size int) string {
	s.calls++
	return strings.Repeat("x", size)
}

// A batch is limited by count only, and each answer stays in memory until the
// last call returns. Large answers must end the batch rather than accumulate.
func TestBatchStopsExecutingPastTheResponseLimit(t *testing.T) {
	svc := new(bigResultService)
	srv := NewServer()
	defer srv.Stop()
	if err := srv.RegisterName("test", svc); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	const (
		calls = 8
		size  = maxBatchResponseBytes / 2
	)
	var batch strings.Builder
	batch.WriteByte('[')
	for i := 0; i < calls; i++ {
		if i > 0 {
			batch.WriteByte(',')
		}
		fmt.Fprintf(&batch, `{"jsonrpc":"2.0","id":%d,"method":"test_big","params":[%d]}`, i, size)
	}
	batch.WriteByte(']')

	resp, err := http.Post(ts.URL, "application/json", strings.NewReader(batch.String()))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var answers []jsonrpcMessage
	if err := json.NewDecoder(resp.Body).Decode(&answers); err != nil {
		t.Fatal(err)
	}

	if svc.calls != 2 {
		t.Fatalf("executed %d calls, want 2", svc.calls)
	}
	if len(answers) != calls {
		t.Fatalf("got %d answers, want one per call (%d)", len(answers), calls)
	}
	for i, answer := range answers[2:] {
		if answer.Error == nil {
			t.Fatalf("answer %d past the limit has no error", i+2)
		}
	}
}
