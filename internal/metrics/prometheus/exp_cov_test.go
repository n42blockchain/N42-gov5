package prometheus

import (
	"net/http/httptest"
	"testing"

	"github.com/n42blockchain/N42/log"
)

// TestSetupRegistersHandler exercises Setup's mux wiring without depending on
// the background ListenAndServe goroutine actually binding a port in time;
// the returned mux already has the /debug/metrics/prometheus route wired
// synchronously, so it can be driven directly via httptest.
func TestSetupRegistersHandler(t *testing.T) {
	mux := Setup("127.0.0.1:0", log.New())
	if mux == nil {
		t.Fatal("Setup returned a nil mux")
	}

	req := httptest.NewRequest("GET", "/debug/metrics/prometheus", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("mux ServeHTTP status = %d, want 200", rec.Code)
	}
}
