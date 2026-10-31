package mobileverify

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

// GET /mobileverify/latest reports the canonical head and whether this
// node holds its packet; without a head it answers 503.
func TestHandleLatest(t *testing.T) {
	s := newHTTPServerTestStub()
	get := func(method string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		s.srv.Handler.ServeHTTP(rec, httptest.NewRequest(method, "/mobileverify/latest", nil))
		return rec
	}
	if rec := get(http.MethodGet); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("unwired head: status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}

	var (
		number uint64
		hash   types.Hash
		ok     bool
	)
	s.SetHead(func() (uint64, types.Hash, bool) { return number, hash, ok })
	if rec := get(http.MethodGet); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("no head yet: status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if rec := get(http.MethodPost); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST: status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}

	number, hash, ok = 42, h(0x42), true
	type latest struct {
		Number uint64 `json:"number"`
		Hash   string `json:"hash"`
		Packet bool   `json:"packet"`
	}
	read := func() latest {
		rec := get(http.MethodGet)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
		}
		var out latest
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	if got := read(); got != (latest{Number: 42, Hash: hash.Hex(), Packet: false}) {
		t.Fatalf("before the packet: %+v", got)
	}
	s.packets.cache.Put(hash, number, []byte{0x01})
	if got := read(); got != (latest{Number: 42, Hash: hash.Hex(), Packet: true}) {
		t.Fatalf("after the packet: %+v", got)
	}
}
