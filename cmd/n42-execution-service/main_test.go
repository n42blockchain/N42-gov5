package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExecutionServiceRejectsProductionAndMissingPaths(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{"--genesis", "/tmp/performance.json", "--data-dir", "/data/blockchain/qs-node0"},
		{"--genesis", "/tmp/performance.json", "--data-dir", "/home/n42/src/n42/N42-gov5/mainnet"},
		{"--genesis", "relative.json", "--data-dir", "/data/blockchain/gov5-performance-5m/runtime/execution"},
		{"--genesis", "/tmp/performance.json", "--data-dir", "/data/blockchain/gov5-performance-5m/runtime/execution", "--capacity", "0"},
	} {
		if err := run(context.Background(), args); err == nil {
			t.Fatal("unsafe execution service selection accepted")
		}
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "not-created")
	if err := run(context.Background(), []string{"--genesis", filepath.Join(dir, "missing"), "--data-dir", target}); err == nil {
		t.Fatal("missing execution datadir accepted")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("rejected execution service created runtime state")
	}
}

func TestExecutionServiceDiagnosticsRequireExplicitBoundedIngress(t *testing.T) {
	dir := t.TempDir()
	base := []string{"--genesis", filepath.Join(dir, "missing.json"), "--data-dir", "/data/blockchain/gov5-performance-5m/runtime/missing"}
	for _, extra := range [][]string{
		{"--resume-diagnostics-jsonl", filepath.Join(dir, "trace.jsonl")},
		{"--bounded-ingress-plan", filepath.Join(dir, "plan.json"), "--resume-diagnostics-jsonl", "relative.jsonl"},
	} {
		err := run(context.Background(), append(append([]string(nil), base...), extra...))
		if err == nil || !strings.Contains(err.Error(), "diagnostics requires explicit bounded ingress") {
			t.Fatalf("diagnostics flag boundary: %v", err)
		}
	}
	err := run(context.Background(), append(append([]string(nil), base...), "--bounded-ingress-plan", filepath.Join(dir, "plan.json"), "--resume-diagnostics-jsonl", filepath.Join(dir, "explicit.jsonl")))
	if err == nil || !strings.Contains(err.Error(), "bounded ingress requires explicit source, manifest, plan, proof and exact plan ID") {
		t.Fatalf("absolute observer flag did not reach bounded ingress validation: %v", err)
	}
	t.Setenv("N42_RESUME_DIAGNOSTICS_JSONL", filepath.Join(dir, "inherited.jsonl"))
	err = run(context.Background(), append(base, "--bounded-ingress-plan", filepath.Join(dir, "plan.json"), "--resume-diagnostics-jsonl", filepath.Join(dir, "explicit.jsonl")))
	if err == nil || !strings.Contains(err.Error(), "remove inherited N42_RESUME_DIAGNOSTICS_JSONL") {
		t.Fatalf("inherited observer configuration accepted: %v", err)
	}
}
