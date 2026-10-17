package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/sharedexec"
	"github.com/n42blockchain/N42/internal/sharedvalidator"
)

type resumePlanFixture struct {
	inputs                  resumePlanInputs
	runtimeBase, outputBase string
	output                  string
	manifest                supplyManifest
	runtime                 strictRuntime
	outer                   resumeOuterReport
	recovery                strictRecoveryResult
	original                resumeOriginalReport
}

// Reports below are synthetic test inputs, not canonical execution evidence.
// Original corpus transactions are genuinely signed by the source fixture.
func makeResumePlanFixture(t *testing.T) resumePlanFixture {
	t.Helper()
	source := makeResumeSourceFixture(t)
	base := t.TempDir()
	f := resumePlanFixture{manifest: source.manifest, runtimeBase: filepath.Join(base, "runtime"), outputBase: filepath.Join(base, "validation")}
	runtimeRoot := filepath.Join(f.runtimeBase, "retained")
	artifact := filepath.Join(f.outputBase, "fixture")
	for _, dir := range []string{runtimeRoot, artifact} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	f.output = filepath.Join(artifact, "plan-output")
	f.inputs.SourceRoot = source.root
	f.inputs.Manifest = resumePinnedInput{Path: filepath.Join(source.root, "manifest.json"), SHA256: source.manifest.Digest}
	f.inputs.Runtime.Path = filepath.Join(runtimeRoot, "run.json")
	f.inputs.Outer.Path = filepath.Join(artifact, "outer.json")
	f.inputs.Recovery.Path = filepath.Join(artifact, "recovery.json")
	f.inputs.Original.Path = filepath.Join(artifact, "original.json")
	genesisPath := filepath.Join(runtimeRoot, "genesis.json")
	if err := os.WriteFile(genesisPath, source.genesis, 0600); err != nil {
		t.Fatal(err)
	}
	genesisHash := strings.TrimPrefix(source.manifest.Descriptor.GenesisFileHash.Hex(), "0x")
	validators := make([]map[string]any, 7)
	for i := range validators {
		dir := filepath.Join(runtimeRoot, fmt.Sprintf("v%d", i))
		validators[i] = map[string]any{"index": i, "address": (types.Address{byte(i + 1)}).Hex(), "peer_id": fmt.Sprintf("fixture-peer-%d", i), "data_dir": dir, "config": filepath.Join(dir, "node.json"), "api_socket": filepath.Join(dir, "validator-rpc", "validator.sock"), "tcp_port": 25000 + i}
	}
	runtimeDocument := map[string]any{"schema_version": 2, "status": "provisioned-not-initialized", "root": runtimeRoot, "genesis": genesisPath, "genesis_sha256": genesisHash, "identity": source.manifest.Descriptor.Identity, "owner_dir": filepath.Join(runtimeRoot, "e"), "owner_socket": filepath.Join(runtimeRoot, "e", "execution-rpc", "execution.sock"), "owner_parallel_evm": true, "validators": validators, "transactions": "ethereum-secp256k1", "gas_price": "1", "supply": map[string]any{"root": source.root, "manifest_sha256": strings.TrimPrefix(source.manifest.Digest.Hex(), "0x"), "corpus_id": source.manifest.CorpusID, "genesis_file_sha256": genesisHash, "block_interval_ms": 100}}
	f.inputs.Runtime = writeResumeFixturePin(t, f.inputs.Runtime.Path, runtimeDocument)
	var err error
	f.runtime, err = loadStrictRuntime(f.inputs.Runtime.Path, source.root, f.runtimeBase, source.manifest)
	if err != nil {
		t.Fatal(err)
	}
	anchor := resumeReportAnchor{Number: 17, Hash: types.Hash{1}, Root: types.Hash{2}}
	owner := sharedexec.StrictIngressStatus{Identity: source.manifest.Descriptor.Identity, Epoch: [16]byte{1}, ManifestHash: source.manifest.Digest, CanonicalHash: anchor.Hash, CanonicalNumber: anchor.Number}
	f.recovery = strictRecoveryResult{Kind: "strict-owner-queue-recovery-no-dispatch-not-tps", Passed: true, Identity: source.manifest.Descriptor.Identity, ManifestHash: source.manifest.Digest, RuntimeManifest: f.inputs.Runtime.Path, OwnerInitial: owner, OwnerFinal: owner, Accounts: source.baseline, NonceSum: 583}
	vector, err := json.Marshal(f.recovery.Accounts)
	if err != nil {
		t.Fatal(err)
	}
	f.recovery.VectorSHA256 = types.Hash(sha256.Sum256(vector))
	for i := 0; i < 7; i++ {
		status := sharedvalidator.StatusResult{CanonicalProgress: sharedvalidator.CanonicalProgress{Identity: owner.Identity, Number: anchor.Number, BlockHash: anchor.Hash, CommittedExecutedTransactions: 583}, OwnerQueue: &sharedvalidator.OwnerQueueBinding{Epoch: owner.Epoch, ManifestHash: owner.ManifestHash, MaxTransactions: sharedvalidator.DefaultOwnerQueueTransactions, MaxEncodedBytes: sharedexec.MaxQueueBuildEncodedBytes}}
		f.recovery.Initial = append(f.recovery.Initial, status)
		f.recovery.Final = append(f.recovery.Final, status)
	}
	f.inputs.Recovery = writeResumeFixturePin(t, f.inputs.Recovery.Path, f.recovery)
	f.original.Kind, f.original.Passed = "gov5-qmdb-original-two-leaf-restore-v1", true
	f.original.Audit.Kind, f.original.Audit.Passed = "gov5-qmdb-missing-genesis-leaves-readonly-audit-v2", true
	f.original.Audit.GenesisSHA256 = genesisHash
	f.original.Audit.Genesis = resumeReportAnchor{Hash: owner.Identity.GenesisHash}
	f.original.Audit.Canonical, f.original.Audit.Applied = anchor, anchor
	f.inputs.Original = writeResumeFixturePin(t, f.inputs.Original.Path, f.original)
	f.outer = resumeOuterReport{Kind: "same-data-recovery-q2-canonical-nonce-audit-not-tps", Passed: true, OriginalHash: strings.TrimPrefix(f.inputs.Original.SHA256.Hex(), "0x"), RecoveryPath: f.inputs.Recovery.Path, NonceSum: 583, VectorSHA256: f.recovery.VectorSHA256, Canonical: anchor}
	f.inputs.Outer = writeResumeFixturePin(t, f.inputs.Outer.Path, f.outer)
	return f
}

func writeResumeFixturePin(t *testing.T, path string, value any) resumePinnedInput {
	t.Helper()
	raw, err := resumeJSONBytes(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return resumePinnedInput{Path: path, SHA256: types.Hash(sha256.Sum256(raw))}
}

func TestResumePlanOnlyPublishesBoundArtifactsWithoutSession(t *testing.T) {
	f := makeResumePlanFixture(t)
	if err := runResumePlanOnly(context.Background(), f.inputs, f.output, f.runtimeBase, f.outputBase); err != nil {
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
	var result resumePlanResult
	read("result-candidate.json", &result)
	var sourceProof resumeSourceProof
	proofRaw := read("source-proof.json", &sourceProof)
	var document resumePlanDocument
	planRaw := read("plan.json", &document)
	manifest, err := os.ReadFile(f.inputs.Manifest.Path)
	if err != nil {
		t.Fatal(err)
	}
	id, err := sharedexec.ResumePlanID(document.Plan, manifest, f.recovery.Accounts)
	if err != nil || id != document.PlanID || id != result.PlanID || types.Hash(sha256.Sum256(proofRaw)) != result.SourceProofSHA256 || result.SourceProofSHA256 != document.Plan.SourceProofSHA256 || types.Hash(sha256.Sum256(planRaw)) != result.PlanFileSHA256 {
		t.Fatalf("artifact chain differs: %v", err)
	}
	if !result.Passed || result.MappingSignatureChecks != 4 || result.SelectedSignatureChecks != 2000 || result.FullSourceShards != 2 || result.CanonicalSkipped != 583 || result.NewlyAdmitted != 1417 || result.OwnerSessionEnabled || result.CurrentSnapshotRechecked || result.Dispatched != 0 || !result.TemporaryGenesisMDBXUsed {
		t.Fatalf("incorrect plan-only boundaries: %+v", result)
	}
	for _, forbidden := range []string{"epoch", "plan_id", "verified", "mtime", f.inputs.SourceRoot} {
		if bytes.Contains(proofRaw, []byte(forbidden)) {
			t.Fatalf("source proof contains session/audit field %q", forbidden)
		}
	}
	before := result.PlanFileSHA256
	if err := runResumePlanOnly(context.Background(), f.inputs, f.output, f.runtimeBase, f.outputBase); err == nil {
		t.Fatal("existing output was overwritten")
	}
	raw, err := os.ReadFile(filepath.Join(f.output, "plan.json"))
	if err != nil || types.Hash(sha256.Sum256(raw)) != before {
		t.Fatal("existing artifact changed")
	}
}

func TestResumePlanOnlyHistoricalProofRejectsMismatches(t *testing.T) {
	f := makeResumePlanFixture(t)
	encode := func(value any) []byte {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	outerRaw, recoveryRaw, originalRaw := encode(f.outer), encode(f.recovery), encode(f.original)
	if _, _, err := resumeHistoricalBaseline(f.inputs, f.manifest, f.runtime, outerRaw, recoveryRaw, originalRaw); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"owner-canonical", "producer-active", "vector-hash", "root", "wrong-original", "wrong-inner-path", "dispatch", "duplicate-key"} {
		t.Run(name, func(t *testing.T) {
			var outer resumeOuterReport
			var recovery strictRecoveryResult
			if err := json.Unmarshal(outerRaw, &outer); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(recoveryRaw, &recovery); err != nil {
				t.Fatal(err)
			}
			switch name {
			case "owner-canonical":
				recovery.OwnerFinal.CanonicalNumber++
			case "producer-active":
				recovery.Final[6].ProducerRunning = true
			case "vector-hash":
				recovery.Accounts[0].Nonce++
			case "root":
				outer.Canonical.Root[0]++
			case "wrong-original":
				outer.OriginalHash = (types.Hash{9}).Hex()
			case "wrong-inner-path":
				outer.RecoveryPath += ".other"
			case "dispatch":
				recovery.Dispatched = 1
			}
			badOuter := encode(outer)
			if name == "duplicate-key" {
				badOuter = append([]byte(`{"passed":false,`), badOuter[1:]...)
			}
			if _, _, err := resumeHistoricalBaseline(f.inputs, f.manifest, f.runtime, badOuter, encode(recovery), originalRaw); err == nil {
				t.Fatal("inconsistent historical proof accepted")
			}
		})
	}
}

func TestResumePlanOnlyRejectsPinBeforeOutputAndRetainsScanFailure(t *testing.T) {
	f := makeResumePlanFixture(t)
	bad := f.inputs
	bad.Outer.SHA256[0]++
	if err := runResumePlanOnly(context.Background(), bad, f.output, f.runtimeBase, f.outputBase); err == nil {
		t.Fatal("wrong pin accepted")
	}
	if _, err := os.Lstat(f.output); !os.IsNotExist(err) {
		t.Fatal("invalid input created output")
	}
	path := filepath.Join(f.inputs.SourceRoot, f.manifest.Shards[1].File)
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	var last [1]byte
	if _, err := file.ReadAt(last[:], int64(f.manifest.Shards[1].Bytes-1)); err != nil {
		t.Fatal(err)
	}
	last[0] ^= 1
	if _, err := file.WriteAt(last[:], int64(f.manifest.Shards[1].Bytes-1)); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := runResumePlanOnly(context.Background(), f.inputs, f.output, f.runtimeBase, f.outputBase); err == nil {
		t.Fatal("corrupt full source accepted")
	}
	for _, name := range []string{"source-proof.json", "plan.json", "result-candidate.json", "result.json"} {
		if _, err := os.Lstat(filepath.Join(f.output, name)); !os.IsNotExist(err) {
			t.Fatalf("failure published %s", name)
		}
	}
	raw, err := os.ReadFile(filepath.Join(f.output, "failure.json"))
	if err != nil {
		t.Fatal(err)
	}
	var failure resumePlanResult
	if err := json.Unmarshal(raw, &failure); err != nil || failure.Passed || failure.Error == "" {
		t.Fatal("failure evidence missing or incorrectly passed")
	}
}

func TestResumePlanJSONRejectsAmbiguousDocuments(t *testing.T) {
	for _, text := range []string{`{"x":1,"x":2}`, `{"x":{"a":1,"a":2}}`, `{"passed":false,"Passed":true}`, `{"outer":{"identity":{},"Identity":{}}}`, `{"Kind":1}`, `{"x":[]} {}`, `{"x":[1,]}`} {
		var value any
		if err := resumeDecodeJSON([]byte(text), &value, false); err == nil {
			t.Fatalf("ambiguous document accepted: %s", text)
		}
	}
	for _, text := range []string{"", "0x00", strings.Repeat("0", 64), strings.Repeat("g", 64)} {
		if _, err := resumeHashText(text); err == nil {
			t.Fatalf("invalid pin accepted: %q", text)
		}
	}
}

func TestResumePlanOnlyLatePublicationFailureNeverPromotes(t *testing.T) {
	f := makeResumePlanFixture(t)
	for _, stage := range []string{"late-write", "readback", "final-directory-sync"} {
		t.Run(stage, func(t *testing.T) {
			parent := filepath.Join(f.outputBase, stage)
			if err := os.Mkdir(parent, 0700); err != nil {
				t.Fatal(err)
			}
			output := filepath.Join(parent, "plan-output")
			injected := errors.New("injected publication failure: " + stage)
			directorySyncs := 0
			publication := resumePlanPublication{
				write: func(path string, value any) (types.Hash, error) {
					if filepath.Base(path) != "result-candidate.json" {
						return writeResumeArtifact(path, value)
					}
					if stage == "late-write" {
						// Simulate an interrupted final write after file creation.
						return types.Hash{}, errors.Join(os.WriteFile(path, []byte(`{"passed":true`), 0600), injected)
					}
					hash, err := writeResumeArtifact(path, value)
					if stage == "readback" {
						return hash, errors.Join(err, injected)
					}
					return hash, err
				},
				sync: func(path string) error {
					if path == output {
						directorySyncs++
						if stage == "final-directory-sync" && directorySyncs == 2 {
							return injected
						}
					}
					return syncResumeDirectory(path)
				},
			}
			err := runResumePlanOnlyPublication(context.Background(), f.inputs, output, f.runtimeBase, f.outputBase, publication)
			if !errors.Is(err, injected) {
				t.Fatalf("late failure was not returned: %v", err)
			}
			if _, err := os.Lstat(filepath.Join(output, "result.json")); !os.IsNotExist(err) {
				t.Fatal("failure promoted a success marker")
			}
			raw, err := os.ReadFile(filepath.Join(output, "failure.json"))
			if err != nil {
				t.Fatal(err)
			}
			var failure resumePlanResult
			if err := json.Unmarshal(raw, &failure); err != nil || failure.Passed || !strings.Contains(failure.Error, injected.Error()) {
				t.Fatal("late publication failure was not preserved")
			}
			// A complete passed candidate may remain after a readback/fsync
			// failure. Its existence must never substitute for child rc=0,
			// absence of failure.json, and normal outer claim-tail completion.
		})
	}
}
