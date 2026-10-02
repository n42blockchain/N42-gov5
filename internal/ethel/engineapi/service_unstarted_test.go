package engineapi

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

// TestName pins the Service.Name() identifier used by the services registry.
func TestName(t *testing.T) {
	svc := &Service{}
	if got := svc.Name(); got != "engineAPI" {
		t.Errorf("Name() = %q, want %q", got, "engineAPI")
	}
}

// TestSetTxPool verifies the setter stores the pool.
func TestSetTxPool(t *testing.T) {
	svc := &Service{}
	svc.SetTxPool(nil) // nil is a valid common.ITxsPool value here
	// No getter exists; this exercises the setter line for coverage and
	// documents that it does not panic on a nil pool.
}

// TestSetMissingAncestorObserver_NoV1 covers the branch where s.v1 is nil
// (service not started) — the observer is stored but never forwarded.
func TestSetMissingAncestorObserver_NoV1(t *testing.T) {
	svc := &Service{}
	called := false
	svc.SetMissingAncestorObserver(func(types.Hash) { called = true })
	if svc.missingAncestorObserver == nil {
		t.Fatal("expected observer to be stored")
	}
	svc.missingAncestorObserver(types.Hash{})
	if !called {
		t.Error("stored observer was not the one passed in")
	}
}

// TestHasBlockHash_NilService / Unstarted cover HasBlockHash's nil-safety
// guards: a nil *Service, and a non-nil Service with no v1 adapter.
func TestHasBlockHash_NilService(t *testing.T) {
	var svc *Service
	if svc.HasBlockHash(types.Hash{}) {
		t.Error("expected false for nil Service")
	}
}

func TestHasBlockHash_Unstarted(t *testing.T) {
	svc := &Service{}
	if svc.HasBlockHash(types.Hash{}) {
		t.Error("expected false when v1 is nil")
	}
}

// TestImportSyncedBlock_NilService / Unstarted cover the not-started guard.
func TestImportSyncedBlock_NilService(t *testing.T) {
	var svc *Service
	_, _, err := svc.ImportSyncedBlock(nil, nil)
	if err == nil {
		t.Error("expected error for nil Service")
	}
}

func TestImportSyncedBlock_Unstarted(t *testing.T) {
	svc := &Service{}
	_, _, err := svc.ImportSyncedBlock(nil, nil)
	if err == nil {
		t.Error("expected error when v1 is nil")
	}
}

// TestMarkRejectedPayloadHash_NilGuards covers the nil-safety guards: a nil
// *Service and a non-nil Service with no apiCore.
func TestMarkRejectedPayloadHash_NilGuards(t *testing.T) {
	var svc *Service
	svc.MarkRejectedPayloadHash(types.Hash{}, types.Hash{}) // must not panic

	svc2 := &Service{}
	svc2.MarkRejectedPayloadHash(types.Hash{}, types.Hash{}) // must not panic (apiCore nil)
}

// TestStop_NeverStarted covers Stop's early-return when server is nil.
func TestStop_NeverStarted(t *testing.T) {
	svc := &Service{}
	if err := svc.Stop(); err != nil {
		t.Fatalf("Stop on never-started service: %v", err)
	}
}
