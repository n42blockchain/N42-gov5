package hotstuff

import "testing"

// TestAPI_EngineEnabled covers the API accessors' success branches (engine
// present), complementing TestAPI_NilEngine's fallback-branch coverage.
func TestAPI_EngineEnabled(t *testing.T) {
	setup := newTestSetup(t, 4)
	h := New(nil, nil)
	h.Authorize(setup.validators[0].Address, setup.keys[0])
	if err := h.InitEngine(setup.validators, setup.f); err != nil {
		t.Fatal(err)
	}
	api := &API{hotstuff: h}

	if v := api.GetCurrentView(); v != h.Engine().CurrentView() {
		t.Fatalf("GetCurrentView: expected %v, got %v", h.Engine().CurrentView(), v)
	}
	if p := api.GetCurrentPhase(); p == "uninitialized" || p == "" {
		t.Fatalf("GetCurrentPhase: expected a real phase string, got %q", p)
	}
	if c := api.GetValidatorCount(); c != 4 {
		t.Fatalf("GetValidatorCount: expected 4, got %v", c)
	}
	if c := api.GetConsecutiveTimeouts(); c != h.Engine().ConsecutiveTimeouts() {
		t.Fatalf("GetConsecutiveTimeouts: expected %v, got %v", h.Engine().ConsecutiveTimeouts(), c)
	}
	// IsCurrentLeader must agree with the engine's own view of leadership.
	if api.IsCurrentLeader() != h.Engine().IsCurrentLeader() {
		t.Fatalf("IsCurrentLeader: API/engine disagree")
	}
}
