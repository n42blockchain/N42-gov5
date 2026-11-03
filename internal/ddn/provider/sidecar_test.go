package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	chain "github.com/n42blockchain/N42/common/types"
	d "github.com/n42blockchain/N42/internal/ddn/types"
)

func TestSidecarRejectsUnboundAndOversizedResponse(t *testing.T) {
	id := Identity{DID: "did:n42:test", ModelHash: chain.Hash{1}}
	for _, body := range []string{`{"request_id":"0x0200000000000000000000000000000000000000000000000000000000000000","model_hash":"0x0100000000000000000000000000000000000000000000000000000000000000","result":{"label":"NORMAL"}}`, string(make([]byte, (1<<20)+1)), `{} {}`} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
		p, err := NewSidecar(s.URL, id, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = p.Decide(context.Background(), d.DecisionRequest{}, "x"); err == nil {
			t.Fatal("invalid response accepted")
		}
		s.Close()
	}
}
