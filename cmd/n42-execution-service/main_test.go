package main

import (
	"context"
	"os"
	"path/filepath"
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
