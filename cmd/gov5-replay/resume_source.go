package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"path/filepath"
	"reflect"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/sharedexec"
)

type resumeSourceMetadata = sharedexec.ResumeSourceMetadata
type resumeSourceWitness = sharedexec.ResumeSourceWitness
type resumeSourceScan = sharedexec.ResumeSourceScan

// Retain the fixture-facing wrapper without trusting a caller-supplied Digest.
func scanResumeSources(ctx context.Context, root string, m supplyManifest, genesisRaw []byte, baseline []sharedexec.CanonicalAccount) (resumeSourceScan, error) {
	raw, err := strictMetadata(filepath.Join(root, "manifest.json"), sharedexec.CorpusMaxManifestBytes)
	if err != nil {
		return resumeSourceScan{}, err
	}
	actual, err := sharedexec.DecodeCorpusManifest(raw)
	if err != nil || types.Hash(sha256.Sum256(raw)) != m.Digest || !reflect.DeepEqual(actual, m.CorpusManifest) {
		return resumeSourceScan{}, errors.New("resume actual manifest differs from fixture input")
	}
	return sharedexec.ScanResumeSources(ctx, root, raw, genesisRaw, baseline)
}
