package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/sharedexec"
	"github.com/n42blockchain/N42/internal/sharedvalidator"
)

type boundedHistoricalOuter struct {
	Kind           string             `json:"kind"`
	Passed         bool               `json:"passed"`
	Error          string             `json:"error"`
	Recoveries     []string           `json:"recovery_results"`
	RecoveryHashes []string           `json:"recovery_sha256"`
	NonceSum       uint64             `json:"canonical_nonce_sum"`
	VectorSHA256   types.Hash         `json:"account_vector_sha256"`
	Canonical      resumeReportAnchor `json:"canonical"`
	Dispatched     uint64             `json:"dispatched_transactions"`
	TPSMeasured    bool               `json:"tps_measured"`
}

type boundedPlanResult struct {
	resumePlanResult
	NonceEndExclusive  uint64     `json:"nonce_end_exclusive"`
	SourceFrames       uint64     `json:"source_frames"`
	SourceTransactions uint64     `json:"source_transactions"`
	SourceEncodedBytes uint64     `json:"source_encoded_bytes"`
	WhitelistSHA256    types.Hash `json:"whitelist_sha256"`
}

// This consumes a pinned historical audit, not a claim that the retained state
// is unchanged now. EnableBoundedResumeIngress repeats the fresh state checks.
func boundedHistoricalBaseline(inputs resumePlanInputs, m supplyManifest, runtime strictRuntime, outerRaw, recoveryRaw, originalRaw []byte) (strictRecoveryResult, resumeReportAnchor, error) {
	var recovery strictRecoveryResult
	var original resumeOriginalReport
	fail := func() (strictRecoveryResult, resumeReportAnchor, error) {
		return strictRecoveryResult{}, resumeReportAnchor{}, errors.New("bounded historical recovery/proof binding differs")
	}
	if err := resumeDecodeJSON(recoveryRaw, &recovery, true); err != nil {
		return strictRecoveryResult{}, resumeReportAnchor{}, err
	}
	if err := resumeDecodeJSON(originalRaw, &original, false); err != nil {
		return strictRecoveryResult{}, resumeReportAnchor{}, err
	}
	outer, err := decodeBoundedHistoricalOuter(inputs, m, recovery, outerRaw)
	if err != nil {
		return fail()
	}
	a := original.Audit
	if original.Kind != "gov5-qmdb-original-two-leaf-restore-v1" || !original.Passed || original.Error != "" || !a.Passed || a.Kind != "gov5-qmdb-missing-genesis-leaves-readonly-audit-v2" || a.Genesis.Number != 0 || a.Genesis.Hash != m.Descriptor.Identity.GenesisHash || a.GenesisSHA256 != strings.TrimPrefix(m.Descriptor.GenesisFileHash.Hex(), "0x") || a.Canonical != a.Applied || a.Canonical.Number > outer.Canonical.Number || (a.Canonical.Number == outer.Canonical.Number && a.Canonical != outer.Canonical) {
		return fail()
	}
	if outer.Canonical.Number == 0 || outer.Canonical.Hash == (types.Hash{}) || outer.Canonical.Root == (types.Hash{}) || recovery.Kind != "strict-owner-queue-recovery-no-dispatch-not-tps" || !recovery.Passed || recovery.Error != "" || recovery.Identity != m.Descriptor.Identity || runtime.Identity != recovery.Identity || recovery.ManifestHash != m.Digest || recovery.RuntimeManifest != inputs.Runtime.Path || recovery.Dispatched != 0 || recovery.NonceSum != outer.NonceSum || recovery.VectorSHA256 != outer.VectorSHA256 || len(recovery.Accounts) != int(m.Descriptor.SenderCount) || len(recovery.Initial) != 7 || len(recovery.Final) != 7 {
		return fail()
	}
	raw, err := json.Marshal(recovery.Accounts)
	if err != nil || types.Hash(sha256.Sum256(raw)) != recovery.VectorSHA256 {
		return fail()
	}
	roots := recovery.Roots
	if roots == nil || roots.CanonicalNumber != outer.Canonical.Number || roots.CanonicalHash != outer.Canonical.Hash || roots.Roots.Root != outer.Canonical.Root || roots.Roots.AppliedRoot != outer.Canonical.Root || !roots.FinalRead || roots.AccountBatches != uint64((len(recovery.Accounts)+999)/1000) {
		return fail()
	}
	epoch := recovery.OwnerFinal.Epoch
	if epoch == ([16]byte{}) || recovery.OwnerInitial.Epoch != epoch {
		return fail()
	}
	for _, owner := range []sharedexec.StrictIngressStatus{recovery.OwnerInitial, recovery.OwnerFinal} {
		if err := recoverySession(owner, epoch, m); err != nil || !strictQueueEmpty(owner) || owner.CanonicalNumber != outer.Canonical.Number || owner.CanonicalHash != outer.Canonical.Hash {
			return fail()
		}
	}
	for _, samples := range [][]sharedvalidator.StatusResult{recovery.Initial, recovery.Final} {
		for _, status := range samples {
			if err := strictValidatorBinding(status, epoch, m); err != nil || status.ProducerRunning || status.QueuedTransactions != 0 || status.Number != outer.Canonical.Number || status.BlockHash != outer.Canonical.Hash || status.CommittedExecutedTransactions != outer.NonceSum {
				return fail()
			}
		}
	}
	return recovery, outer.Canonical, nil
}

func runBoundedResumePlanOnly(ctx context.Context, inputs resumePlanInputs, output, runtimeBase, outputBase string, end uint64) error {
	return runBoundedResumePlanOnlyPublication(ctx, inputs, output, runtimeBase, outputBase, end, resumePlanPublication{write: writeResumeArtifact, sync: syncResumeDirectory})
}

func runBoundedResumePlanOnlyPublication(ctx context.Context, inputs resumePlanInputs, output, runtimeBase, outputBase string, end uint64, publication resumePlanPublication) (returnErr error) {
	if ctx == nil || ctx.Err() != nil {
		return errors.New("active resume plan context required")
	}
	if inputs.Manifest.Path != filepath.Join(inputs.SourceRoot, "manifest.json") || !filepath.IsAbs(output) || filepath.Clean(output) != output || !stringsHasPrefixPath(filepath.Dir(output), outputBase) || filepath.Base(output) != "plan-output" {
		return errors.New("explicit source manifest and fresh validation plan-output required")
	}
	if err := strictPrivatePath(filepath.Dir(output), true, false); err != nil {
		return err
	}
	manifestRaw, err := readResumePin(inputs.Manifest, sharedexec.CorpusMaxManifestBytes)
	if err != nil {
		return err
	}
	var manifest sharedexec.CorpusManifest
	if err := resumeDecodeJSON(manifestRaw, &manifest, true); err != nil {
		return err
	}
	m, err := parseSupplyManifest(manifestRaw)
	if err != nil {
		return err
	}
	runtimeRaw, err := readResumePin(inputs.Runtime, 2<<20)
	if err != nil {
		return err
	}
	var runtimeDocument strictRuntime
	if err := resumeDecodeJSON(runtimeRaw, &runtimeDocument, true); err != nil {
		return err
	}
	runtime, err := loadStrictRuntime(inputs.Runtime.Path, inputs.SourceRoot, runtimeBase, m)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(runtime, runtimeDocument) {
		return errors.New("resume runtime validation read differs from pinned document")
	}
	outerRaw, err := readResumePin(inputs.Outer, 16<<20)
	if err != nil {
		return err
	}
	recoveryRaw, err := readResumePin(inputs.Recovery, 16<<20)
	if err != nil {
		return err
	}
	originalRaw, err := readResumePin(inputs.Original, 16<<20)
	if err != nil {
		return err
	}
	baseline, anchor, err := boundedHistoricalBaseline(inputs, m, runtime, outerRaw, recoveryRaw, originalRaw)
	if err != nil {
		return err
	}
	genesisRaw, err := strictMetadata(filepath.Join(inputs.SourceRoot, "genesis.json"), 16<<20)
	if err != nil {
		return err
	}
	runtimeGenesis, err := strictMetadata(runtime.Genesis, 16<<20)
	if err != nil || !bytes.Equal(genesisRaw, runtimeGenesis) {
		return errors.New("resume source/runtime exact genesis bytes differ")
	}
	if err := os.Mkdir(output, 0700); err != nil {
		return err
	}
	result := boundedPlanResult{resumePlanResult: resumePlanResult{Kind: "gov5-bounded-resume-plan-only-candidate-no-dispatch-v2", Inputs: inputs, TemporaryGenesisMDBXUsed: true}, NonceEndExclusive: end}
	defer func() {
		if returnErr != nil {
			result.Passed = false
			result.Error = returnErr.Error()
			returnErr = errors.Join(returnErr, exclusiveJSON(filepath.Join(output, "failure.json"), result))
		}
	}()
	if err := publication.sync(filepath.Dir(output)); err != nil {
		return err
	}
	scan, err := sharedexec.ScanBoundedResumeSources(ctx, inputs.SourceRoot, manifestRaw, genesisRaw, baseline.Accounts, end)
	if err != nil {
		return err
	}
	proof, proofBytes, err := sharedexec.EncodeResumeSourceProof(manifestRaw, scan.Source)
	if err != nil {
		return err
	}
	d := m.Descriptor
	p := sharedexec.BoundedResumePlan{Version: 2, Identity: d.Identity, ManifestSHA256: m.Digest, CorpusID: m.CorpusID, GenesisFileSHA256: d.GenesisFileHash, SourceProofSHA256: types.Hash(sha256.Sum256(proofBytes)), MappingSHA256: scan.Source.MappingSHA256, RecoveryOuterSHA256: inputs.Outer.SHA256, RecoverySHA256: inputs.Recovery.SHA256, AccountVectorSHA256: baseline.VectorSHA256, OriginalRestoreSHA256: inputs.Original.SHA256, Canonical: sharedexec.ResumeCanonicalAnchor{Number: anchor.Number, Hash: anchor.Hash, Root: anchor.Root}, CanonicalNonceSum: baseline.NonceSum, Senders: scan.Source.Senders, NonceEndExclusive: end, MaxSourceFrames: scan.SourceFrames, MaxSourceTransactions: scan.SourceTransactions, MaxNewTransactions: scan.NewTransactions, SourceEncodedBytes: scan.SourceEncodedBytes, MaxSourceEncodedBytes: scan.SourceTransactions*106 + scan.SourceFrames*sharedexec.CorpusWireHeaderSize, WhitelistSHA256: scan.WhitelistSHA256}
	planID, err := sharedexec.BoundedResumePlanID(p, manifestRaw, baseline.Accounts)
	if err != nil {
		return err
	}
	for _, input := range []resumePinnedInput{inputs.Manifest, inputs.Runtime, inputs.Outer, inputs.Recovery, inputs.Original} {
		if _, err := readResumePin(input, 16<<20); err != nil {
			return err
		}
	}
	for _, path := range []string{filepath.Join(inputs.SourceRoot, "genesis.json"), runtime.Genesis} {
		raw, err := strictMetadata(path, 16<<20)
		if err != nil || !bytes.Equal(raw, genesisRaw) {
			return errors.New("resume genesis bytes changed during scan")
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	result.SourceProofSHA256, err = publication.write(filepath.Join(output, "source-proof.json"), proof)
	if err != nil || result.SourceProofSHA256 != p.SourceProofSHA256 {
		return errors.New("resume source proof publication differs")
	}
	result.PlanFileSHA256, err = publication.write(filepath.Join(output, "plan.json"), sharedexec.BoundedResumePlanDocument{Kind: "gov5-bounded-resume-prefix-plan-v2", Plan: p, PlanID: planID})
	if err != nil {
		return err
	}
	result.PlanID = planID
	result.SourceFrames, result.SourceTransactions, result.SourceEncodedBytes = scan.SourceFrames, scan.SourceTransactions, scan.SourceEncodedBytes
	result.WhitelistSHA256 = scan.WhitelistSHA256
	result.FilesBefore, result.FilesAfter = scan.Source.FilesBefore, scan.Source.FilesAfter
	result.FullSourceBytes, result.FullSourceShards = scan.Source.FullSourceBytes, scan.Source.FullSourceShards
	result.MappingSignatureChecks, result.SelectedSignatureChecks = scan.Source.MappingSignatureChecks, scan.Source.SelectedFrameSignatureChecks
	for _, frame := range scan.Frames {
		result.CanonicalSkipped += uint64(frame.CanonicalSkipped)
		result.NewlyAdmitted += uint64(frame.NewlyAdmitted)
	}
	if err := publication.sync(output); err != nil {
		return err
	}
	result.Passed = true
	if _, err := publication.write(filepath.Join(output, "result-candidate.json"), result); err != nil {
		return err
	}
	// A candidate alone is never a promoted result: an external controller must
	// also require our rc=0, no failure record, exact artifact hashes, and normal
	// claim-tail completion before publishing its independent success evidence.
	if err := publication.sync(output); err != nil {
		return err
	}
	return ctx.Err()
}
