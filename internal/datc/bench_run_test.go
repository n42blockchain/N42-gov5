// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// bench_run_test.go covers proveAt (the per-query prove+verify unit bench
// uses) and runBench itself, end to end, against the run-worker fixture's
// headerc freezer and acctcs/storcs changeset freezer.
package datc

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/ethel"
)

// TestProveAtLiveAndAbsent exercises proveAt directly — bench.go's own 0%
// function — against the fixture archive's querier, for a live account with
// storage, a live EOA (no storage), and an absent account.
func TestProveAtLiveAndAbsent(t *testing.T) {
	f := newRunFixture(t)
	q, _, closeQ := f.openQuerier(t)
	defer closeQ()

	const height = uint64(200)
	root := f.sc.roots[height]

	addr := f.sc.big[0]
	var sampleSlots []types.Hash
	for s := range f.sc.storageAt(addr, height) {
		sampleSlots = append(sampleSlots, s)
		if len(sampleSlots) == 2 {
			break
		}
	}
	r := proveAt(q, benchSample{Addr: addr, Slots: sampleSlots, Height: height}, root)
	if r.Err != "" {
		t.Fatalf("proveAt(big contract): %s", r.Err)
	}
	if !r.Live {
		t.Fatal("big contract must be live")
	}

	eoa := f.sc.eoas[0]
	r2 := proveAt(q, benchSample{Addr: eoa, Height: height}, root)
	if r2.Err != "" {
		t.Fatalf("proveAt(eoa): %s", r2.Err)
	}

	r3 := proveAt(q, benchSample{Addr: f.sc.absent, Height: height}, root)
	if r3.Err != "" {
		t.Fatalf("proveAt(absent): %s", r3.Err)
	}
	if r3.Live {
		t.Fatal("absent account must not be live")
	}

	// A wrong root must fail proof verification, not silently succeed.
	var wrongRoot types.Hash
	wrongRoot[0] = 0xAB
	r4 := proveAt(q, benchSample{Addr: addr, Height: height}, wrongRoot)
	if r4.Err == "" {
		t.Fatal("proveAt against a wrong root must report an error")
	}
}

func TestRunBenchHappyPath(t *testing.T) {
	f := newRunFixture(t)
	jsonOut := filepath.Join(t.TempDir(), "bench.json")
	out := captureStdout(t, func() {
		runBench([]string{
			"--out", f.archiveDir,
			"--headers", f.headersDir,
			"--changesets", f.csDir,
			"--samples", "20",
			"--slots", "2",
			"--mode", "mixed",
			"--parallel", "2",
			"--seed", "7",
			"--map.gb", "4",
			"--json", jsonOut,
		})
	})
	if !strings.Contains(out, "queries=20") {
		t.Fatalf("unexpected bench output: %s", out)
	}
	if strings.Contains(out, "failed=20") || !strings.Contains(out, "failed=0") {
		t.Fatalf("expected zero failures, got: %s", out)
	}
	raw, err := os.ReadFile(jsonOut)
	if err != nil {
		t.Fatal(err)
	}
	var results []benchResult
	if err := json.Unmarshal(raw, &results); err != nil {
		t.Fatal(err)
	}
	if len(results) != 20 {
		t.Fatalf("json results: got %d, want 20", len(results))
	}
	for _, r := range results {
		if r.Err != "" {
			t.Fatalf("result error: %s", r.Err)
		}
	}
}

// TestRunBenchFromChangesetsDiag confirms the changeset freezer the fixture
// wrote is readable by the exact same decoder runDiag uses, independent of
// runDiag itself (covered separately), as a guard that bench/diag share one
// upstream input correctly.
func TestRunBenchFromChangesetsDiag(t *testing.T) {
	f := newRunFixture(t)
	acctTbl := openCS(f.csDir, "acctcs")
	defer acctTbl.Close()
	blob, err := acctTbl.Retrieve(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(blob) == 0 {
		t.Skip("block 1 touched no accounts in this scenario")
	}
	entries, err := ethel.DecodeAccountChanges(blob)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("expected at least one decoded account change")
	}
}
