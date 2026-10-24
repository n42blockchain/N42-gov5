package main

import (
	"crypto/sha256"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

// This focused integration check reads the immutable accepted Q21 proof when
// an operator supplies its path. Ordinary package tests skip the external data.
func TestBoundedQ21HistoricalProjection(t *testing.T) {
	outerPath := os.Getenv("GOV5_Q21_PROJECTION_FIXTURE")
	if outerPath == "" {
		t.Skip("external accepted Q21 proof not provided")
	}
	recoveryPath := os.Getenv("GOV5_Q21_RECOVERY_FIXTURE")
	manifestPath := os.Getenv("GOV5_Q21_MANIFEST_FIXTURE")
	if recoveryPath == "" || manifestPath == "" {
		t.Fatal("all three Q21 fixture paths required")
	}
	raw, err := os.ReadFile(outerPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := types.Hash(sha256.Sum256(raw)); got.Hex() != "0x9d61e433b954330a90a317e4509f116cf7be63853d6f03996c0ee6b0f91ea433" {
		t.Fatalf("Q21 accepted proof hash changed: %s", got.Hex())
	}
	var outer boundedQ21HistoricalOuter
	if err := resumeDecodeJSON(raw, &outer, false); err != nil {
		t.Fatal(err)
	}
	recoveryRaw, err := os.ReadFile(recoveryPath)
	if err != nil {
		t.Fatal(err)
	}
	var recovery strictRecoveryResult
	if err := resumeDecodeJSON(recoveryRaw, &recovery, true); err != nil {
		t.Fatal(err)
	}
	manifestRaw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	m, err := parseSupplyManifest(manifestRaw)
	if err != nil {
		t.Fatal(err)
	}
	inputs := resumePlanInputs{Recovery: resumePinnedInput{Path: recoveryPath, SHA256: types.Hash(sha256.Sum256(recoveryRaw))}}
	check := func(name string, value boundedQ21HistoricalOuter, wantOK bool) {
		t.Helper()
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		got, err := decodeBoundedQ21HistoricalOuter(inputs, m, recovery, encoded)
		if wantOK && (err != nil || got.NonceSum != 16871500 || got.Canonical != outer.Canonical) {
			t.Fatalf("%s: accepted projection differs: %v %+v", name, err, got)
		}
		if !wantOK && err == nil {
			t.Fatalf("%s: malformed historical proof accepted", name)
		}
	}
	clone := func() boundedQ21HistoricalOuter {
		t.Helper()
		encoded, err := json.Marshal(outer)
		if err != nil {
			t.Fatal(err)
		}
		var value boundedQ21HistoricalOuter
		if err := json.Unmarshal(encoded, &value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	check("accepted Q21", outer, true)
	if got, err := decodeBoundedHistoricalOuter(inputs, m, recovery, raw); err != nil || got.NonceSum != outer.NonceSum {
		t.Fatalf("Q21 public projection route failed: %v", err)
	}
	unknown := clone()
	unknown.Kind = "unaccepted-historical-kind"
	unknownRaw, err := json.Marshal(unknown)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeBoundedHistoricalOuter(inputs, m, recovery, unknownRaw); err == nil {
		t.Fatal("unknown historical kind accepted")
	}
	mutated := clone()
	mutated.RecoveryHash = "0x" + strings.Repeat("00", 32)
	check("wrong recovery hash", mutated, false)
	mutated = clone()
	mutated.OwnerFinal.Epoch = recovery.OwnerFinal.Epoch
	mutated.Live.Owner.Epoch = recovery.OwnerFinal.Epoch
	for i := range mutated.Live.Validators {
		mutated.Live.Validators[i].OwnerQueue.Epoch = recovery.OwnerFinal.Epoch
	}
	check("reused recovery epoch", mutated, false)
	mutated = clone()
	mutated.Live.Roots = nil
	check("missing canonical roots", mutated, false)
	mutated = clone()
	part := mutated.Postproof.Lifecycle["all-process-lifecycle.json"]
	part.Processes[0].Stopped = part.Processes[9].Started + 1
	mutated.Postproof.Lifecycle["all-process-lifecycle.json"] = part
	production := mutated.Postproof.Lifecycle["production-process-lifecycle.json"]
	production.Processes[0].Stopped = part.Processes[0].Stopped
	mutated.Postproof.Lifecycle["production-process-lifecycle.json"] = production
	check("overlapping original and reopened owners", mutated, false)
	mutated = clone()
	part = mutated.Postproof.Lifecycle["all-process-lifecycle.json"]
	clean := false
	part.CleanupClean = &clean
	mutated.Postproof.Lifecycle["all-process-lifecycle.json"] = part
	check("unclean lifecycle", mutated, false)
	mutated = clone()
	part = mutated.Postproof.Lifecycle["all-process-lifecycle.json"]
	part.CleanupClean = nil
	mutated.Postproof.Lifecycle["all-process-lifecycle.json"] = part
	check("missing lifecycle cleanup result", mutated, false)
}
