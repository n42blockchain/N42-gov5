package mobileverify

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

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

func TestRegistrationServiceStartWithoutP2PErrors(t *testing.T) {
	reg := NewRegistry()
	s := NewRegistrationService(reg, nil, "/test")
	if err := s.Start(); err == nil {
		t.Fatal("expected error when no p2p publisher is wired")
	}
	s.Stop()
}

func TestPublishLocalRejectsNilPacket(t *testing.T) {
	cache := NewPacketCache(8)
	svc := NewPacketService(cache, nil, "/test")
	if err := svc.PublishLocal(nil, 1); err == nil {
		t.Fatal("expected error for a nil StreamPacket")
	}
}

func TestNewCohortRelayStartStopWithoutP2P(t *testing.T) {
	reg := NewRegistry()
	cfg := CohortConfig{IndexAnnounceDelay: 1, ReconcileDelay: 2, MergeDelay: 4}
	coord := NewCohortCoordinator(reg, fixedLookupAt(types.Hash{}, 0), NewCertStore(8), types.Address{1}, cfg)
	relay := NewCohortRelay(coord, nil, reg, "/index", "/cert")
	if err := relay.Start(); err != nil {
		t.Fatalf("Start() error = %v, want nil when p2p is unwired", err)
	}
	relay.Stop()
}

func TestNewCohortRelayWithAuthWiresTopics(t *testing.T) {
	reg := NewRegistry()
	cfg := CohortConfig{IndexAnnounceDelay: 1, ReconcileDelay: 2, MergeDelay: 4}
	coord := NewCohortCoordinator(reg, fixedLookupAt(types.Hash{}, 0), NewCertStore(8), types.Address{1}, cfg)
	relay := NewCohortRelayWithAuth(coord, nil, reg, "/index", "/reveal", "/cert", nil, nil)
	if relay.indexTopic != "/index" || relay.revealTopic != "/reveal" || relay.certTopic != "/cert" {
		t.Fatalf("relay topics = %+v, want index=/index reveal=/reveal cert=/cert", relay)
	}
	relay.Stop()
}

func TestCohortCoordinatorStopIsIdempotent(t *testing.T) {
	reg := NewRegistry()
	cfg := CohortConfig{IndexAnnounceDelay: 1, ReconcileDelay: 2, MergeDelay: 4}
	c := NewCohortCoordinator(reg, fixedLookupAt(types.Hash{}, 0), NewCertStore(8), types.Address{1}, cfg)
	c.Stop()
	// Calling Stop again must not panic.
	c.Stop()
}
