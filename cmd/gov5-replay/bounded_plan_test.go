package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/sharedexec"
)

func makeBoundedPlanFixture(t *testing.T) (resumePlanFixture, boundedHistoricalOuter) {
	t.Helper()
	f := makeResumePlanFixture(t)
	a := f.outer.Canonical
	f.recovery.Roots = &recoveryRootEvidence{CanonicalHash: a.Hash, CanonicalNumber: a.Number, Roots: sharedexec.CanonicalRoots{Root: a.Root, AppliedRoot: a.Root}, AccountBatches: 1, FinalRead: true}
	f.inputs.Recovery = writeResumeFixturePin(t, f.inputs.Recovery.Path, f.recovery)
	outer := boundedHistoricalOuter{Kind: "gov5-post-resume-double-physical-reopen-accepted-not-tps", Passed: true, Recoveries: []string{"synthetic-first-not-used", f.inputs.Recovery.Path}, RecoveryHashes: []string{(types.Hash{9}).Hex(), strings.TrimPrefix(f.inputs.Recovery.SHA256.Hex(), "0x")}, NonceSum: f.recovery.NonceSum, VectorSHA256: f.recovery.VectorSHA256, Canonical: a}
	f.inputs.Outer = writeResumeFixturePin(t, f.inputs.Outer.Path, outer)
	return f, outer
}

func TestBoundedResumePlanOnlyBindsPrefixCapsAndHistoricalRoots(t *testing.T) {
	f, _ := makeBoundedPlanFixture(t)
	if err := runBoundedResumePlanOnly(context.Background(), f.inputs, f.output, f.runtimeBase, f.outputBase, 1000); err != nil {
		t.Fatal(err)
	}
	read := func(name string, out any) []byte {
		t.Helper()
		raw, err := strictMetadata(filepath.Join(f.output, name), 16<<20)
		if err != nil {
			t.Fatal(err)
		}
		if err := resumeDecodeJSON(raw, out, true); err != nil {
			t.Fatal(err)
		}
		return raw
	}
	var result boundedPlanResult
	read("result-candidate.json", &result)
	var doc sharedexec.BoundedResumePlanDocument
	planRaw := read("plan.json", &doc)
	var proof sharedexec.ResumeSourceProof
	proofRaw := read("source-proof.json", &proof)
	manifest, err := readResumePin(f.inputs.Manifest, sharedexec.CorpusMaxManifestBytes)
	if err != nil {
		t.Fatal(err)
	}
	id, err := sharedexec.BoundedResumePlanID(doc.Plan, manifest, f.recovery.Accounts)
	if err != nil || id != doc.PlanID || id != result.PlanID || result.PlanFileSHA256 != types.Hash(sha256.Sum256(planRaw)) || result.SourceProofSHA256 != types.Hash(sha256.Sum256(proofRaw)) || doc.Plan.SourceProofSHA256 != result.SourceProofSHA256 {
		t.Fatal("bounded plan artifact chain differs", err)
	}
	if !result.Passed || result.Kind != "gov5-bounded-resume-plan-only-candidate-no-dispatch-v2" || result.SourceFrames != 8 || result.SourceTransactions != 4000 || result.NewlyAdmitted != 3417 || result.CanonicalSkipped != 583 || result.NonceEndExclusive != 1000 || result.MappingSignatureChecks != 4 || result.SelectedSignatureChecks != 0 || result.SourceEncodedBytes != doc.Plan.SourceEncodedBytes || result.WhitelistSHA256 != doc.Plan.WhitelistSHA256 || result.OwnerSessionEnabled || result.CurrentSnapshotRechecked || result.Dispatched != 0 {
		t.Fatalf("bounded plan-only boundaries differ: %+v", result)
	}
	if _, err := os.Lstat(filepath.Join(f.output, "result.json")); !os.IsNotExist(err) {
		t.Fatal("plan-only candidate promoted itself")
	}
	if err := runBoundedResumePlanOnly(context.Background(), f.inputs, f.output, f.runtimeBase, f.outputBase, 1000); err == nil {
		t.Fatal("existing bounded plan output reused")
	}
}

func TestBoundedResumeHistoricalAuditRequiresActualRootEvidenceAndFinalReportPin(t *testing.T) {
	f, outer := makeBoundedPlanFixture(t)
	raw := func(v any) []byte {
		t.Helper()
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	for _, kind := range []string{"gov5-post-resume-double-physical-reopen-accepted-not-tps", "gov5-post-bounded-double-physical-reopen-accepted-not-tps", "gov5-quiescent-retained-child-double-physical-reopen-accepted-not-tps", "gov5-post-bounded-quiescent-retained-child-double-physical-reopen-accepted-not-tps"} {
		outer.Kind = kind
		if _, _, err := boundedHistoricalBaseline(f.inputs, f.manifest, f.runtime, raw(outer), raw(f.recovery), raw(f.original)); err != nil {
			t.Fatalf("complete pinned audit rejected for kind %s: %v", kind, err)
		}
	}
	outer.Kind = "unrecognized-audit"
	if _, _, err := boundedHistoricalBaseline(f.inputs, f.manifest, f.runtime, raw(outer), raw(f.recovery), raw(f.original)); err == nil {
		t.Fatal("unrecognized historical audit kind accepted")
	}
	outer.Kind = "gov5-post-bounded-double-physical-reopen-accepted-not-tps"
	for _, change := range []func(*strictRecoveryResult){
		func(r *strictRecoveryResult) { r.Roots = nil },
		func(r *strictRecoveryResult) {
			roots := *r.Roots
			roots.Roots.AppliedRoot = types.Hash{7}
			r.Roots = &roots
		},
		func(r *strictRecoveryResult) { roots := *r.Roots; roots.FinalRead = false; r.Roots = &roots },
		func(r *strictRecoveryResult) { roots := *r.Roots; roots.AccountBatches++; r.Roots = &roots },
	} {
		r := f.recovery
		change(&r)
		if _, _, err := boundedHistoricalBaseline(f.inputs, f.manifest, f.runtime, raw(outer), raw(r), raw(f.original)); err == nil {
			t.Fatal("historical root evidence mutation accepted")
		}
	}
	outer.RecoveryHashes[1] = (types.Hash{2}).Hex()
	if _, _, err := boundedHistoricalBaseline(f.inputs, f.manifest, f.runtime, raw(outer), raw(f.recovery), raw(f.original)); err == nil {
		t.Fatal("final recovery report hash not bound")
	}
}

func TestBoundedResumePlanLatePublicationFailureNeverPromotes(t *testing.T) {
	f, _ := makeBoundedPlanFixture(t)
	injected := errors.New("injected bounded candidate publication failure")
	publication := resumePlanPublication{sync: syncResumeDirectory, write: func(path string, value any) (types.Hash, error) {
		hash, err := writeResumeArtifact(path, value)
		if err == nil && filepath.Base(path) == "result-candidate.json" {
			return hash, injected
		}
		return hash, err
	}}
	if err := runBoundedResumePlanOnlyPublication(context.Background(), f.inputs, f.output, f.runtimeBase, f.outputBase, 1000, publication); !errors.Is(err, injected) {
		t.Fatalf("late failure lost: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(f.output, "result.json")); !os.IsNotExist(err) {
		t.Fatal("failed candidate promoted itself")
	}
	var failure boundedPlanResult
	raw, err := strictMetadata(filepath.Join(f.output, "failure.json"), 16<<20)
	if err != nil || resumeDecodeJSON(raw, &failure, true) != nil || failure.Passed || failure.Error == "" {
		t.Fatal("late failure missing failed evidence", err)
	}
}

func TestBoundedResumeQuiescentHistoricalKindRequiresPinnedSecondReopen(t *testing.T) {
	f, outer := makeBoundedPlanFixture(t)
	outer.Kind = "gov5-quiescent-retained-child-double-physical-reopen-accepted-not-tps"
	encode := func(v any) []byte {
		t.Helper()
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	for name, mutate := range map[string]func(*boundedHistoricalOuter){
		"failed":         func(o *boundedHistoricalOuter) { o.Passed = false },
		"error":          func(o *boundedHistoricalOuter) { o.Error = "unfinished" },
		"window":         func(o *boundedHistoricalOuter) { o.TPSMeasured = true },
		"dispatch":       func(o *boundedHistoricalOuter) { o.Dispatched = 1 },
		"missing-second": func(o *boundedHistoricalOuter) { o.Recoveries = o.Recoveries[:1] },
		"third":          func(o *boundedHistoricalOuter) { o.Recoveries = append(o.Recoveries, "third") },
		"missing-hash":   func(o *boundedHistoricalOuter) { o.RecoveryHashes = o.RecoveryHashes[:1] },
		"wrong-path":     func(o *boundedHistoricalOuter) { o.Recoveries[1] = "another-recovery.json" },
		"wrong-hash":     func(o *boundedHistoricalOuter) { o.RecoveryHashes[1] = (types.Hash{7}).Hex() },
		"unknown":        func(o *boundedHistoricalOuter) { o.Kind = "gov5-quiescent-not-accepted" },
	} {
		t.Run(name, func(t *testing.T) {
			copy := outer
			copy.Recoveries = append([]string(nil), outer.Recoveries...)
			copy.RecoveryHashes = append([]string(nil), outer.RecoveryHashes...)
			mutate(&copy)
			if _, _, err := boundedHistoricalBaseline(f.inputs, f.manifest, f.runtime, encode(copy), encode(f.recovery), encode(f.original)); err == nil {
				t.Fatal("changed quiescent proof accepted")
			}
		})
	}
	badShape := map[string]any{"kind": outer.Kind, "passed": true, "recovery_results": outer.Recoveries, "recovery_sha256": outer.RecoveryHashes[1]}
	if _, _, err := boundedHistoricalBaseline(f.inputs, f.manifest, f.runtime, encode(badShape), encode(f.recovery), encode(f.original)); err == nil {
		t.Fatal("scalar recovery hash accepted for double reopen")
	}
}

func TestBoundedResumePostBoundedQuiescentHistoricalKindRequiresPinnedSecondReopen(t *testing.T) {
	f, outer := makeBoundedPlanFixture(t)
	outer.Kind = "gov5-post-bounded-quiescent-retained-child-double-physical-reopen-accepted-not-tps"
	encode := func(v any) []byte {
		t.Helper()
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	for name, mutate := range map[string]func(*boundedHistoricalOuter){
		"failed":         func(o *boundedHistoricalOuter) { o.Passed = false },
		"error":          func(o *boundedHistoricalOuter) { o.Error = "unfinished" },
		"window":         func(o *boundedHistoricalOuter) { o.TPSMeasured = true },
		"dispatch":       func(o *boundedHistoricalOuter) { o.Dispatched = 1 },
		"missing-second": func(o *boundedHistoricalOuter) { o.Recoveries = o.Recoveries[:1] },
		"third":          func(o *boundedHistoricalOuter) { o.Recoveries = append(o.Recoveries, "third") },
		"missing-hash":   func(o *boundedHistoricalOuter) { o.RecoveryHashes = o.RecoveryHashes[:1] },
		"wrong-path":     func(o *boundedHistoricalOuter) { o.Recoveries[1] = "another-recovery.json" },
		"wrong-hash":     func(o *boundedHistoricalOuter) { o.RecoveryHashes[1] = (types.Hash{7}).Hex() },
		"unknown":        func(o *boundedHistoricalOuter) { o.Kind = "gov5-quiescent-not-accepted" },
	} {
		t.Run(name, func(t *testing.T) {
			copy := outer
			copy.Recoveries = append([]string(nil), outer.Recoveries...)
			copy.RecoveryHashes = append([]string(nil), outer.RecoveryHashes...)
			mutate(&copy)
			if _, _, err := boundedHistoricalBaseline(f.inputs, f.manifest, f.runtime, encode(copy), encode(f.recovery), encode(f.original)); err == nil {
				t.Fatal("changed quiescent proof accepted")
			}
		})
	}
	badShape := map[string]any{"kind": outer.Kind, "passed": true, "recovery_results": outer.Recoveries, "recovery_sha256": outer.RecoveryHashes[1]}
	if _, _, err := boundedHistoricalBaseline(f.inputs, f.manifest, f.runtime, encode(badShape), encode(f.recovery), encode(f.original)); err == nil {
		t.Fatal("scalar recovery hash accepted for double reopen")
	}
}
