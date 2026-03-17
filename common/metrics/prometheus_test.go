package prometheus

import (
	"net/http/httptest"
	"sync"
	"testing"

	clientprometheus "github.com/prometheus/client_golang/prometheus"
)

func TestHandlerCanBeConstructedMultipleTimes(t *testing.T) {
	oldRegisterer := clientprometheus.DefaultRegisterer
	oldGatherer := clientprometheus.DefaultGatherer
	registry := clientprometheus.NewRegistry()
	clientprometheus.DefaultRegisterer = registry
	clientprometheus.DefaultGatherer = registry
	defer func() {
		clientprometheus.DefaultRegisterer = oldRegisterer
		clientprometheus.DefaultGatherer = oldGatherer
	}()

	registerDefaultSetOnce = sync.Once{}

	reg := NewRegistry()
	h1 := Handler(reg)
	h2 := Handler(reg)

	if h1 == nil || h2 == nil {
		t.Fatal("Handler() returned nil")
	}

	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	h2.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("ServeHTTP() status = %d, want 200", rec.Code)
	}
}
