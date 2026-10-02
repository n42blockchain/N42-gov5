// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package bootstrap

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/internal/ethel"
)

// TestName pins the Service.Name() identifier used by the services registry.
func TestName(t *testing.T) {
	svc := &Service{}
	if got := svc.Name(); got != "bootstrap" {
		t.Errorf("Name() = %q, want %q", got, "bootstrap")
	}
}

// TestNew exercises the production constructor, pinning that it wires the
// config, node, fetcher, and the real ethel.RebuildState rebuilder through
// unchanged.
func TestNew(t *testing.T) {
	node := &ethel.Node{}
	svc := New(conf.BootstrapCfg{Manifest: "m.json"}, node, nil)
	if svc.cfg.Manifest != "m.json" {
		t.Errorf("manifest = %q, want %q", svc.cfg.Manifest, "m.json")
	}
	if svc.node == nil {
		t.Error("node not set")
	}
	if svc.rebuild == nil {
		t.Error("rebuild function not set")
	}
}

// TestStartSnapshot_NilNode / TestStartSnapshot_OK cover the "snapshot" mode
// branch of Start, which is otherwise untested: it performs no RebuildState
// and only guards against a nil node.
func TestStartSnapshot_NilNode(t *testing.T) {
	svc := newForTest(conf.BootstrapCfg{Mode: "snapshot"}, nil, nil, nil)
	if err := svc.Start(context.Background()); err == nil {
		t.Fatal("expected error for nil node")
	}
}

func TestStartSnapshot_OK(t *testing.T) {
	svc := newForTest(conf.BootstrapCfg{Mode: "snapshot"}, &stubNode{}, nil, nil)
	if err := svc.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
}

// TestStart_UnknownMode covers the default branch of the mode switch.
func TestStart_UnknownMode(t *testing.T) {
	svc := newForTest(conf.BootstrapCfg{Mode: "bogus"}, &stubNode{}, nil, nil)
	if err := svc.Start(context.Background()); err == nil {
		t.Fatal("expected error for unknown mode")
	}
}
