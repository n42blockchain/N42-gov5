package coldresolve

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

// fakeFreezerSetter records SetColdResolver calls and returns a fixed result.
type fakeFreezerSetter struct {
	ok          bool
	gotTable    string
	gotResolver freezer.ColdResolver
	called      bool
}

func (f *fakeFreezerSetter) SetColdResolver(table string, r freezer.ColdResolver) bool {
	f.called = true
	f.gotTable = table
	f.gotResolver = r
	return f.ok
}

// fakeColdResolver is a minimal freezer.ColdResolver stub.
type fakeColdResolver struct{}

func (fakeColdResolver) ResolveDataFile(fileName string) (string, error) {
	return "", nil
}

func TestFreezerInstallService_Name(t *testing.T) {
	svc := NewFreezerInstallService(nil, "receipts", nil)
	if got := svc.Name(); got != "cold-resolver-receipts" {
		t.Errorf("Name() = %q, want %q", got, "cold-resolver-receipts")
	}
}

func TestFreezerInstallService_NilFields(t *testing.T) {
	svc := NewFreezerInstallService(nil, "receipts", nil)
	if err := svc.Start(context.Background()); err != nil {
		t.Fatalf("Start with nil fz/resolver: %v", err)
	}
	if err := svc.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

func TestFreezerInstallService_Success(t *testing.T) {
	fz := &fakeFreezerSetter{ok: true}
	svc := NewFreezerInstallService(fz, "receipts", fakeColdResolver{})
	if err := svc.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !fz.called || fz.gotTable != "receipts" {
		t.Errorf("SetColdResolver not called correctly: called=%v table=%q", fz.called, fz.gotTable)
	}
}

func TestFreezerInstallService_TableNotOpen(t *testing.T) {
	fz := &fakeFreezerSetter{ok: false}
	svc := NewFreezerInstallService(fz, "receipts", fakeColdResolver{})
	if err := svc.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !fz.called {
		t.Error("expected SetColdResolver to be called")
	}
}
