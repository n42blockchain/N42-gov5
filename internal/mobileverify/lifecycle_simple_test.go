package mobileverify

import "testing"

func TestHTTPServerStartStop(t *testing.T) {
	s := newHTTPServerTestStub()
	if err := s.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	s.Stop()
}

func TestPacketServiceStartWithoutP2PErrors(t *testing.T) {
	cache := NewPacketCache(8)
	ps := NewPacketService(cache, nil, "/test")
	if err := ps.Start(); err == nil {
		t.Fatal("expected error when no p2p publisher is wired")
	}
	// Stop before a successful Start must be a safe no-op.
	ps.Stop()
}
