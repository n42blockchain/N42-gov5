package main

import (
	"bytes"
	"context"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/sharedexec"
)

func TestBoundedResumeSourceWholePrefixWithoutLegacyFourBuckets(t *testing.T) {
	f := makeResumeSourceFixture(t)
	raw, err := os.ReadFile(filepath.Join(f.root, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	original := append([]sharedexec.CanonicalAccount(nil), f.baseline...)
	// The account vector is synthetic test input. No chain nonce is modified.
	for i, nonce := range []uint64{0, 41, 542, 1000} {
		address := crypto.PubkeyToAddress(f.keys[i].PublicKey)
		for j := range f.baseline {
			if f.baseline[j].Address == address {
				balance, _ := new(big.Int).SetString(f.baseline[j].Balance, 10)
				balance.Sub(balance, new(big.Int).SetUint64((nonce-f.baseline[j].Nonce)*21001))
				f.baseline[j].Nonce, f.baseline[j].Balance = nonce, balance.String()
			}
		}
	}
	got, err := sharedexec.ScanBoundedResumeSources(context.Background(), f.root, raw, f.genesis, f.baseline, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if got.SourceFrames != 8 || got.SourceTransactions != 4000 || got.NewTransactions != 2417 || got.Source.MappingSignatureChecks != 4 || got.Source.SelectedFrameSignatureChecks != 0 || got.Source.FullSourceShards != 2 || len(got.Frames) != 8 || !reflect.DeepEqual(got.Source.FilesBefore, got.Source.FilesAfter) {
		t.Fatal("bounded original source range/mapping accounting differs")
	}
	var skipped uint64
	for _, frame := range got.Frames {
		skipped += uint64(frame.CanonicalSkipped)
		if frame.SenderIndex == 2 && frame.FirstNonce == 500 && (frame.CanonicalSkipped != 42 || frame.NewlyAdmitted != 458) {
			t.Fatal("source nonce542 was rounded at a frame boundary")
		}
	}
	if skipped != 1583 {
		t.Fatal("fixed arming canonical prefixes differ")
	}
	// Semantic source proof bytes bind original mapping, not current nonces or
	// the independent v1/v2 selection policy.
	legacy, err := sharedexec.ScanResumeSources(context.Background(), f.root, raw, f.genesis, original)
	if err != nil {
		t.Fatal(err)
	}
	_, oldProof, err := sharedexec.EncodeResumeSourceProof(raw, legacy)
	if err != nil {
		t.Fatal(err)
	}
	_, newProof, err := sharedexec.EncodeResumeSourceProof(raw, got.Source)
	if err != nil || !bytes.Equal(oldProof, newProof) {
		t.Fatal("mapping proof bytes changed between explicit plan versions", err)
	}
	if _, err := sharedexec.ScanResumeSources(context.Background(), f.root, raw, f.genesis, f.baseline); err == nil {
		t.Fatal("legacy four-frame policy silently upgraded to an arbitrary prefix")
	}
}

func TestBoundedResumeSourceHashesUnconsumedTailBeforeMapping(t *testing.T) {
	f := makeResumeSourceFixture(t)
	raw, err := os.ReadFile(filepath.Join(f.root, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(f.root, "shard-01.gcf")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)-1] ^= 1 // Outside the nonce[0,500) prefix being requested.
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	got, err := sharedexec.ScanBoundedResumeSources(context.Background(), f.root, raw, f.genesis, f.baseline, 500)
	if err == nil || !reflect.DeepEqual(got, sharedexec.BoundedResumeSourceScan{}) {
		t.Fatal("unconsumed corrupt tail yielded a partial or successful mapping")
	}
}

func TestBoundedResumeSourceRejectsBadEndBeforeSourceIO(t *testing.T) {
	f := makeResumeSourceFixture(t)
	raw, err := os.ReadFile(filepath.Join(f.root, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := sharedexec.ScanBoundedResumeSources(context.Background(), filepath.Join(f.root, "absent"), raw, nil, nil, 501)
	if err == nil || !strings.Contains(err.Error(), "positive 500 multiple") || !reflect.DeepEqual(got, sharedexec.BoundedResumeSourceScan{}) {
		t.Fatal("invalid geometry reached source opening or returned partial data")
	}
}
