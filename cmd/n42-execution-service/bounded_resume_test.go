package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/sharedexec"
)

func boundedOwnerProfileFixture(t *testing.T) (sharedexec.BoundedResumePlan, []byte, []byte) {
	t.Helper()
	var old sharedexec.CorpusManifest
	if err := json.Unmarshal(ingressManifestForTest(t), &old); err != nil {
		t.Fatal(err)
	}
	d := old.Descriptor
	d.SenderCount, d.NoncesPerSender, d.ShardCount, d.FrameTransactions = 6000, 32000, 64, 500
	id, err := d.ID()
	if err != nil {
		t.Fatal(err)
	}
	m := sharedexec.CorpusManifest{Descriptor: d, CorpusID: id}
	for shard := uint16(0); shard < 64; shard++ {
		senders := (uint64(5999)-uint64(shard))/64 + 1
		txs, frames := senders*32000, senders*64
		m.Shards = append(m.Shards, sharedexec.CorpusShard{Index: shard, File: fmt.Sprintf("shard-%02d.gcf", shard), SHA256: types.Hash{byte(shard + 1)}, Bytes: txs*5 + frames*84, Transactions: txs, Frames: frames})
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	proof := []byte("synthetic fixture; does not authorize any owner")
	p := sharedexec.BoundedResumePlan{Version: 2, Identity: d.Identity, ManifestSHA256: types.Hash(sha256.Sum256(raw)), CorpusID: id, GenesisFileSHA256: d.GenesisFileHash, SourceProofSHA256: types.Hash(sha256.Sum256(proof)), Senders: make([]sharedexec.ResumeSender, 6000), NonceEndExclusive: 10000, MaxSourceFrames: 120000, MaxSourceTransactions: 60_000_000, CanonicalNonceSum: 211917, MaxNewTransactions: 59_788_083, SourceEncodedBytes: 6_000_000_000, MaxSourceEncodedBytes: 6_370_080_000}
	return p, raw, proof
}

func TestBoundedResumeOwnerExplicitProfileDoesNotArmFromParsedContent(t *testing.T) {
	p, manifest, proof := boundedOwnerProfileFixture(t)
	if err := validateBoundedOwnerProfile(p, manifest, proof); err != nil {
		t.Fatal(err)
	}
	mutations := []func(*sharedexec.BoundedResumePlan){
		func(p *sharedexec.BoundedResumePlan) { p.NonceEndExclusive = 1000 },
		func(p *sharedexec.BoundedResumePlan) { p.Senders = p.Senders[:4] },
		func(p *sharedexec.BoundedResumePlan) { p.MaxSourceFrames-- },
		func(p *sharedexec.BoundedResumePlan) { p.MaxSourceTransactions++ },
		func(p *sharedexec.BoundedResumePlan) { p.MaxSourceEncodedBytes++ },
		func(p *sharedexec.BoundedResumePlan) { p.MaxNewTransactions++ },
		func(p *sharedexec.BoundedResumePlan) { p.CanonicalNonceSum = 60_000_001 },
		func(p *sharedexec.BoundedResumePlan) { p.SourceEncodedBytes = p.MaxSourceEncodedBytes + 1 },
		func(p *sharedexec.BoundedResumePlan) { p.ManifestSHA256[0] ^= 1 },
		func(p *sharedexec.BoundedResumePlan) { p.SourceProofSHA256[0] ^= 1 },
	}
	for i, mutate := range mutations {
		bad := p
		mutate(&bad)
		if err := validateBoundedOwnerProfile(bad, manifest, proof); err == nil {
			t.Fatalf("production geometry or binding mutation %d accepted", i)
		}
	}
	dir := t.TempDir()
	planID := types.Hash{1}
	doc, err := json.Marshal(sharedexec.BoundedResumePlanDocument{Kind: "gov5-bounded-resume-prefix-plan-v2", Plan: p, PlanID: planID})
	if err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string][]byte{"plan": doc, "manifest": manifest, "proof": proof} {
		if err := os.WriteFile(filepath.Join(dir, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	opts, err := boundedResumeIngressOptions(filepath.Join(dir, "plan"), filepath.Join(dir, "proof"), dir, filepath.Join(dir, "manifest"), planID.Hex(), 0, 0)
	if err != nil || opts == nil || opts.MaxTransactions != 2_000_000 || opts.MaxEncodedBytes != 512<<20 || opts.ExpectedPlanID != planID || opts.GenesisRaw != nil {
		t.Fatalf("explicit bounded metadata: %+v %v", opts, err)
	}
	// The synthetic mapping and anchor still require the owner's independent
	// verification; this helper only selects inputs and enforces the profile.
	if opts, err := boundedResumeIngressOptions("", "", "", "", "", 0, 0); err != nil || opts != nil {
		t.Fatal("default selected bounded ingress")
	}
	if _, err := boundedResumeIngressOptions(filepath.Join(dir, "plan"), filepath.Join(dir, "proof"), dir, filepath.Join(dir, "manifest"), (types.Hash{2}).Hex(), 0, 0); err == nil {
		t.Fatal("different local plan pin accepted")
	}
}

func TestBoundedResumeOwnerFlagsRejectMixedAndIncompleteModesBeforeDatabase(t *testing.T) {
	dir := t.TempDir()
	datadir := filepath.Join(dir, "not-created")
	base := []string{"--genesis", filepath.Join(dir, "unread-genesis"), "--data-dir", datadir}
	for _, extra := range [][]string{
		{"--bounded-ingress-plan", "/missing"},
		{"--bounded-ingress-source", "/source"},
		{"--bounded-ingress-plan-id", "0x00"},
		{"--bounded-ingress-plan", "/missing", "--resume-ingress-source", "/source"},
		{"--bounded-ingress-plan", "/missing", "--strict-ingress-manifest", "/manifest"},
	} {
		if err := run(context.Background(), append(append([]string(nil), base...), extra...)); err == nil || !strings.Contains(err.Error(), "ingress") {
			t.Fatalf("bounded input gate: %v", err)
		}
		if _, err := os.Lstat(datadir); !os.IsNotExist(err) {
			t.Fatal("invalid bounded mode created runtime data")
		}
	}
}
