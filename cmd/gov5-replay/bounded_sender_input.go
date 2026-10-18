package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"path/filepath"
	"reflect"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/sharedexec"
)

// Both the original window and the finite diagnostic use this exact local
// source verification and hold the rehashed file descriptors until completion.
// Its result is private command state, never an owner activation capability.
type boundedSenderInput struct {
	manifest     supplyManifest
	runtime      strictRuntime
	runtimeRaw   []byte
	document     sharedexec.BoundedResumePlanDocument
	baseline     strictRecoveryResult
	scan         sharedexec.BoundedResumeSourceScan
	sources      []strictSource
	owner        *sharedexec.Client
	initialOwner sharedexec.StrictIngressStatus
}

func (in *boundedSenderInput) close() {
	closeStrictSources(in.sources)
	in.sources = nil
	if in.owner != nil {
		in.owner.Close()
		in.owner = nil
	}
}

func loadBoundedSenderInput(ctx context.Context, root, runtimePath string, planPin, baselinePin resumePinnedInput, expectedPlan types.Hash) (_ *boundedSenderInput, returnErr error) {
	input := new(boundedSenderInput)
	defer func() {
		if returnErr != nil {
			input.close()
		}
	}()
	m, err := loadSupplyManifest(root)
	if err != nil {
		return nil, err
	}
	manifestRaw, err := strictMetadata(filepath.Join(root, "manifest.json"), sharedexec.CorpusMaxManifestBytes)
	if err != nil {
		return nil, err
	}
	if types.Hash(sha256.Sum256(manifestRaw)) != m.Digest {
		return nil, errors.New("bounded source manifest changed")
	}
	runtimeRaw, err := strictMetadata(runtimePath, 2<<20)
	if err != nil {
		return nil, err
	}
	var runtimeDocument strictRuntime
	if err := resumeDecodeJSON(runtimeRaw, &runtimeDocument, true); err != nil {
		return nil, err
	}
	runtime, err := loadStrictRuntime(runtimePath, root, strictRuntimeBase, m)
	if err != nil || !reflect.DeepEqual(runtime, runtimeDocument) {
		return nil, errors.New("bounded runtime differs from pinned bytes")
	}
	planRaw, err := readResumePin(planPin, sharedexec.ResumeMaxPlanDocumentBytes)
	if err != nil {
		return nil, err
	}
	doc, err := sharedexec.DecodeBoundedResumePlanDocument(planRaw)
	if err != nil {
		return nil, err
	}
	p := doc.Plan
	if doc.PlanID != expectedPlan || p.Identity != runtime.Identity || p.ManifestSHA256 != m.Digest || p.CorpusID != m.CorpusID || p.GenesisFileSHA256 != m.Descriptor.GenesisFileHash || p.NonceEndExclusive != 10000 || p.MaxSourceTransactions != 60_000_000 || p.MaxSourceFrames != 120000 || len(p.Senders) != 6000 {
		return nil, errors.New("bounded production plan/source/runtime geometry differs")
	}
	// The historical vector is an explicit path in the plan's pinned outer
	// audit. Resolve it from runtime evidence, never an arbitrary socket value.
	if baselinePin.SHA256 != p.RecoverySHA256 || !stringsHasPrefixPath(baselinePin.Path, filepath.Join(runtime.Root, "strict-attempts")) {
		return nil, errors.New("bounded historical vector path/hash differs from local plan")
	}
	baselineRaw, err := readResumePin(baselinePin, 16<<20)
	if err != nil {
		return nil, err
	}
	var baseline strictRecoveryResult
	if err := resumeDecodeJSON(baselineRaw, &baseline, true); err != nil {
		return nil, err
	}
	if id, err := sharedexec.BoundedResumePlanID(p, manifestRaw, baseline.Accounts); err != nil || id != expectedPlan {
		return nil, errors.New("bounded sender complete baseline/plan ID differs")
	}
	genesis, err := strictMetadata(filepath.Join(root, "genesis.json"), 16<<20)
	if err != nil {
		return nil, err
	}
	scan, err := sharedexec.ScanBoundedResumeSources(ctx, root, manifestRaw, genesis, baseline.Accounts, p.NonceEndExclusive)
	if err != nil {
		return nil, err
	}
	_, proof, err := sharedexec.EncodeResumeSourceProof(manifestRaw, scan.Source)
	if err != nil {
		return nil, err
	}
	if types.Hash(sha256.Sum256(proof)) != p.SourceProofSHA256 || scan.Source.MappingSHA256 != p.MappingSHA256 || scan.WhitelistSHA256 != p.WhitelistSHA256 || scan.SourceFrames != p.MaxSourceFrames || scan.SourceTransactions != p.MaxSourceTransactions || scan.SourceEncodedBytes != p.SourceEncodedBytes || scan.NewTransactions != p.MaxNewTransactions || !reflect.DeepEqual(scan.Source.Senders, p.Senders) {
		return nil, errors.New("bounded sender independently reconstructed source/whitelist differs")
	}
	sources, err := openStrictSources(ctx, root, m)
	if err != nil {
		return nil, err
	}
	input.sources = sources
	if _, err := checkBoundedSources(sources, scan.Source.FilesBefore); err != nil {
		return nil, err
	}
	owner := sharedexec.NewClient(runtime.OwnerSocket, runtime.Identity)
	input.owner = owner
	probe, cancelProbe := context.WithTimeout(ctx, 2*time.Second)
	initialOwner, err := owner.StrictIngressStatus(probe)
	cancelProbe()
	if err != nil {
		return nil, err
	}
	if initialOwner.Identity != p.Identity || initialOwner.ManifestHash != p.ManifestSHA256 || initialOwner.Epoch == ([16]byte{}) || initialOwner.CanonicalHash != p.Canonical.Hash || initialOwner.CanonicalNumber != p.Canonical.Number || initialOwner.NewlyAdmitted != 0 || initialOwner.CryptoVerified != 0 || !strictQueueEmpty(initialOwner) {
		return nil, errors.New("bounded sender requires newly armed empty epoch at exact baseline")
	}

	input.manifest, input.runtime, input.runtimeRaw = m, runtime, runtimeRaw
	input.document, input.baseline, input.scan, input.initialOwner = doc, baseline, scan, initialOwner
	return input, nil
}
