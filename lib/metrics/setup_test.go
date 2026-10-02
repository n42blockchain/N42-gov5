package metrics

import (
	"net/http/httptest"
	"testing"

	"github.com/n42blockchain/N42/lib/log/v3"
)

func TestSetupRegistersPrometheusHandler(t *testing.T) {
	logger := log.New()
	// Bind to an ephemeral loopback port; Setup's background ListenAndServe
	// goroutine is not joined, but it is loopback-only and the process is
	// short-lived, so no external network or sleeps are involved.
	mux := Setup("127.0.0.1:0", logger)
	if mux == nil {
		t.Fatal("expected non-nil mux")
	}

	req := httptest.NewRequest("GET", "/debug/metrics/prometheus", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if rr.Body.Len() == 0 {
		t.Fatal("expected non-empty Prometheus metrics body")
	}
}

func TestEnabledExpensiveDefault(t *testing.T) {
	if EnabledExpensive {
		t.Fatal("expected EnabledExpensive to default to false")
	}
}
