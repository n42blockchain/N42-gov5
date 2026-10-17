package main

import (
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/sharedexec"
)

// These local inputs select an explicit owner startup path. The owner still
// authenticates original source bytes and fresh canonical state itself.
func resumeIngressOptions(planPath, proofPath, sourceRoot, manifestPath, expectedPlan string, maxTx, maxBytes int) (*sharedexec.ResumeIngressOptions, error) {
	if planPath == "" && proofPath == "" && sourceRoot == "" && manifestPath == "" && expectedPlan == "" {
		return nil, nil
	}
	if planPath == "" || proofPath == "" || !filepath.IsAbs(sourceRoot) || manifestPath == "" || len(expectedPlan) != 66 || !strings.HasPrefix(expectedPlan, "0x") {
		return nil, errors.New("resume ingress requires explicit source, manifest, plan, proof and exact plan ID")
	}
	digest, err := hex.DecodeString(expectedPlan[2:])
	if err != nil {
		return nil, err
	}
	var planID types.Hash
	copy(planID[:], digest)
	if planID == (types.Hash{}) {
		return nil, errors.New("resume ingress requires nonzero expected plan ID")
	}
	plan, err := readResumeMetadata(planPath, sharedexec.ResumeMaxPlanDocumentBytes)
	if err != nil {
		return nil, err
	}
	document, err := sharedexec.DecodeResumePlanDocument(plan)
	if err != nil {
		return nil, err
	}
	if document.PlanID != planID {
		return nil, errors.New("resume ingress plan differs from local expected ID")
	}
	proof, err := readResumeMetadata(proofPath, 16<<20)
	if err != nil {
		return nil, err
	}
	manifest, err := readResumeMetadata(manifestPath, sharedexec.CorpusMaxManifestBytes)
	if err != nil {
		return nil, err
	}
	if _, err := sharedexec.DecodeCorpusManifest(manifest); err != nil {
		return nil, err
	}
	if maxTx == 0 {
		maxTx = 1417
	}
	if maxBytes == 0 {
		maxBytes = 1 << 20
	}
	if maxTx < 1417 || maxTx > 2_000_000 || maxBytes < 1 || maxBytes > 512<<20 {
		return nil, errors.New("resume ingress credits exceed bounded limits")
	}
	return &sharedexec.ResumeIngressOptions{ManifestRaw: manifest, SourceRoot: sourceRoot, PlanDocumentRaw: plan, SourceProofRaw: proof, ExpectedPlanID: planID, MaxTransactions: maxTx, MaxEncodedBytes: maxBytes}, nil
}

func readResumeMetadata(path string, limit int64) ([]byte, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || !filepath.IsAbs(path) || resolved != path {
		return nil, errors.New("resume metadata must be absolute without symlinks")
	}
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	stat, ok := before.Sys().(*syscall.Stat_t)
	if !ok || !before.Mode().IsRegular() || before.Mode().Perm() != 0600 || stat.Uid != uint32(os.Getuid()) || stat.Nlink != 1 || before.Size() < 1 || before.Size() > limit {
		return nil, errors.New("resume metadata must be private owned bounded regular file")
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !sameResumeMetadata(before, opened) {
		return nil, errors.New("resume metadata changed before open")
	}
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	after, err := f.Stat()
	if err != nil || !sameResumeMetadata(before, after) || int64(len(raw)) != before.Size() || int64(len(raw)) > limit {
		return nil, errors.New("resume metadata changed during read")
	}
	current, err := os.Lstat(path)
	if err != nil || !sameResumeMetadata(before, current) {
		return nil, errors.New("resume metadata path changed during read")
	}
	return raw, nil
}

func sameResumeMetadata(before, after os.FileInfo) bool {
	if before == nil || after == nil || !os.SameFile(before, after) || before.Mode() != after.Mode() || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return false
	}
	a, aok := before.Sys().(*syscall.Stat_t)
	b, bok := after.Sys().(*syscall.Stat_t)
	return aok && bok && a.Uid == b.Uid && a.Nlink == b.Nlink && a.Ctim == b.Ctim
}
