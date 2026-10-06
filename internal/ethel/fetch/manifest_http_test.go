package fetch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestLoadManifest_HTTP covers readManifestHTTP's success path via a real
// httptest server (LoadManifest dispatches http(s):// URLs through it).
func TestLoadManifest_HTTP(t *testing.T) {
	body := `{"version":1,"kind":"segments","chain_id":1,"assets":[{"name":"a","size_bytes":1,"sha256":"0000000000000000000000000000000000000000000000000000000000000000","sources":[{"kind":"https","uri":"https://x/a"}]}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	m, err := LoadManifest(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if m.Kind != ManifestKindSegments || m.ChainID != 1 {
		t.Errorf("unexpected manifest: %+v", m)
	}
}

// TestLoadManifest_HTTPNon200 covers the non-200 status branch.
func TestLoadManifest_HTTPNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer srv.Close()

	if _, err := LoadManifest(context.Background(), srv.URL); err == nil {
		t.Fatal("expected error for 500 response")
	}
}

// TestLoadManifest_HTTPBadJSON covers the JSON-decode error branch reached
// over HTTP.
func TestLoadManifest_HTTPBadJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()

	if _, err := LoadManifest(context.Background(), srv.URL); err == nil {
		t.Fatal("expected decode error")
	}
}
