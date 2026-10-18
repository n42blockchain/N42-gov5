package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/sharedexec"
)

// This production profile bounds the original source prefix independently of
// queue credits. Parsing it never authorizes a session: the owner subsequently
// authenticates every source file, mapping and fresh canonical account itself.
func boundedResumeIngressOptions(planPath, proofPath, sourceRoot, manifestPath, expectedPlan string, maxTx, maxBytes int) (*sharedexec.BoundedResumeIngressOptions, error) {
	if planPath == "" && proofPath == "" && sourceRoot == "" && manifestPath == "" && expectedPlan == "" {
		return nil, nil
	}
	if planPath == "" || proofPath == "" || !filepath.IsAbs(sourceRoot) || manifestPath == "" || len(expectedPlan) != 66 || !strings.HasPrefix(expectedPlan, "0x") {
		return nil, errors.New("bounded ingress requires explicit source, manifest, plan, proof and exact plan ID")
	}
	digest, err := hex.DecodeString(expectedPlan[2:])
	if err != nil {
		return nil, err
	}
	var planID types.Hash
	copy(planID[:], digest)
	if planID == (types.Hash{}) {
		return nil, errors.New("bounded ingress requires nonzero expected plan ID")
	}
	raw, err := readResumeMetadata(planPath, sharedexec.ResumeMaxPlanDocumentBytes)
	if err != nil {
		return nil, err
	}
	document, err := sharedexec.DecodeBoundedResumePlanDocument(raw)
	if err != nil {
		return nil, err
	}
	if document.PlanID != planID {
		return nil, errors.New("bounded ingress plan differs from local expected ID")
	}
	manifest, err := readResumeMetadata(manifestPath, sharedexec.CorpusMaxManifestBytes)
	if err != nil {
		return nil, err
	}
	proof, err := readResumeMetadata(proofPath, 16<<20)
	if err != nil {
		return nil, err
	}
	if err := validateBoundedOwnerProfile(document.Plan, manifest, proof); err != nil {
		return nil, err
	}
	if maxTx == 0 {
		maxTx = 2_000_000
	}
	if maxBytes == 0 {
		maxBytes = 512 << 20
	}
	if maxTx < 500 || maxTx > 2_000_000 || maxBytes < sharedexec.CorpusMaxFrameTxs*106 || maxBytes > 512<<20 {
		return nil, errors.New("bounded ingress queue credits outside explicit limits")
	}
	return &sharedexec.BoundedResumeIngressOptions{ManifestRaw: manifest, SourceRoot: sourceRoot, PlanDocumentRaw: raw, SourceProofRaw: proof, ExpectedPlanID: planID, MaxTransactions: maxTx, MaxEncodedBytes: maxBytes}, nil
}

func validateBoundedOwnerProfile(p sharedexec.BoundedResumePlan, manifestRaw, proof []byte) error {
	manifest, err := sharedexec.DecodeCorpusManifest(manifestRaw)
	if err != nil {
		return err
	}
	d := manifest.Descriptor
	if d.SenderCount != 6000 || d.NoncesPerSender != 32000 || d.NonceStart != 0 || d.ShardCount != 64 || d.FrameTransactions != 500 || len(p.Senders) != 6000 || p.NonceEndExclusive != 10000 || p.MaxSourceFrames != 120000 || p.MaxSourceTransactions != 60_000_000 || p.MaxSourceEncodedBytes != 6_370_080_000 {
		return errors.New("bounded ingress production profile requires 6000 senders and original nonce[0,10000) with exact 60M source caps")
	}
	if p.Version != 2 || p.Identity != d.Identity || p.ManifestSHA256 != types.Hash(sha256.Sum256(manifestRaw)) || p.CorpusID != manifest.CorpusID || p.GenesisFileSHA256 != d.GenesisFileHash || p.SourceProofSHA256 != types.Hash(sha256.Sum256(proof)) || p.CanonicalNonceSum > p.MaxSourceTransactions || p.MaxNewTransactions != p.MaxSourceTransactions-p.CanonicalNonceSum || p.SourceEncodedBytes == 0 || p.SourceEncodedBytes > p.MaxSourceEncodedBytes {
		return errors.New("bounded ingress local source, proof or lifetime cap binding differs")
	}
	return nil
}
