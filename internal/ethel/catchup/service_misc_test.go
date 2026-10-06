// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package catchup

import (
	"testing"

	"github.com/n42blockchain/N42/internal/ethel"
)

// TestName pins the Service.Name() identifier used by the services registry.
func TestName(t *testing.T) {
	svc := &Service{}
	if got := svc.Name(); got != "catch-up" {
		t.Errorf("Name() = %q, want %q", got, "catch-up")
	}
}

// TestNew exercises the production constructor, pinning that it wires the
// config, node, fetcher, and the real ethel.CatchUp runner through unchanged.
func TestNew(t *testing.T) {
	node := &ethel.Node{}
	svc := New(Config{Manifest: "m.json"}, node, nil)
	if svc.manifest != "m.json" {
		t.Errorf("manifest = %q, want %q", svc.manifest, "m.json")
	}
	if svc.node == nil {
		t.Error("node not set")
	}
	if svc.catchUp == nil {
		t.Error("catchUp runner not set")
	}
}
