package sharedexec

import (
	"crypto/sha256"
	"encoding/json"
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

// Pure metadata: source inclusion and actual account state are tested by the
// separate source/owner fixtures, never inferred from these constructed hashes.
func boundedResumeStructureFixture(t *testing.T) (BoundedResumePlan, []byte, []CanonicalAccount, []BoundedResumeFrame) {
	t.Helper()
	old, raw, baseline := resumeStructureFixture(t)
	old.CanonicalNonceSum = 0
	for i, nonce := range []uint64{0, 41, 542, 1000} {
		old.Senders[i].Nonce = nonce
		old.CanonicalNonceSum += nonce
		for j := range baseline {
			if baseline[j].Address == old.Senders[i].Address {
				baseline[j].Nonce = nonce
			}
		}
	}
	vector, _ := json.Marshal(baseline)
	p := BoundedResumePlan{Version: 2, Identity: old.Identity, ManifestSHA256: old.ManifestSHA256, CorpusID: old.CorpusID, GenesisFileSHA256: old.GenesisFileSHA256, SourceProofSHA256: old.SourceProofSHA256, MappingSHA256: old.MappingSHA256, RecoveryOuterSHA256: old.RecoveryOuterSHA256, RecoverySHA256: old.RecoverySHA256, AccountVectorSHA256: types.Hash(sha256.Sum256(vector)), OriginalRestoreSHA256: old.OriginalRestoreSHA256, Canonical: old.Canonical, CanonicalNonceSum: old.CanonicalNonceSum, Senders: old.Senders, NonceEndExclusive: 1000}
	m, err := DecodeCorpusManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	p.MaxSourceFrames, p.MaxSourceTransactions, p.MaxSourceEncodedBytes, err = boundedResumeGeometry(m.Descriptor, p.NonceEndExclusive)
	if err != nil {
		t.Fatal(err)
	}
	var frames []BoundedResumeFrame
	for shard := uint16(0); shard < 2; shard++ {
		for seq := uint64(0); seq < 4; seq++ {
			index, nonce, count, err := m.Descriptor.FrameRange(shard, seq)
			if err != nil {
				t.Fatal(err)
			}
			sender := p.Senders[index]
			var skip uint64
			if sender.Nonce > nonce {
				skip = sender.Nonce - nonce
				if skip > 500 {
					skip = 500
				}
			}
			frames = append(frames, BoundedResumeFrame{ID: CorpusFrameID{CorpusID: p.CorpusID, Shard: shard, Sequence: seq}, SenderIndex: index, Address: sender.Address, FirstNonce: nonce, Transactions: count, PayloadHash: types.Hash{byte(len(frames) + 1)}, PayloadBytes: 52000, CanonicalSkipped: uint16(skip), NewlyAdmitted: count - uint16(skip)})
		}
	}
	p.WhitelistSHA256, p.SourceEncodedBytes, p.MaxNewTransactions, err = BoundedResumeWhitelistID(m.Descriptor, p.Senders, p.NonceEndExclusive, frames)
	if err != nil {
		t.Fatal(err)
	}
	return p, raw, baseline, frames
}

func TestBoundedResumePlanPartialCrossFrameCapsAndStableID(t *testing.T) {
	p, raw, baseline, frames := boundedResumeStructureFixture(t)
	id, err := BoundedResumePlanID(p, raw, baseline)
	if err != nil {
		t.Fatal(err)
	}
	if again, err := BoundedResumePlanID(p, raw, baseline); err != nil || again != id {
		t.Fatal("same content changed v2 plan ID", err)
	}
	if p.MaxSourceFrames != 8 || p.MaxSourceTransactions != 4000 || p.CanonicalNonceSum != 1583 || p.MaxNewTransactions != 2417 || p.SourceEncodedBytes != 416672 || p.MaxSourceEncodedBytes != 424672 {
		t.Fatalf("prefix geometry/lifetime caps differ %+v", p)
	}
	var skipped, newly uint64
	for _, f := range frames {
		skipped += uint64(f.CanonicalSkipped)
		newly += uint64(f.NewlyAdmitted)
		if f.SenderIndex == 2 && f.FirstNonce == 500 && (f.CanonicalSkipped != 42 || f.NewlyAdmitted != 458) {
			t.Fatal("cross-frame historical nonce542 was rounded")
		}
		if f.SenderIndex == 3 && f.NewlyAdmitted != 0 {
			t.Fatal("fully canonical sender acquired new lifetime credit")
		}
	}
	if skipped != 1583 || newly != 2417 {
		t.Fatal("canonical skipped transactions counted as new")
	}
}

func TestBoundedResumePlanEveryBindingChangesIDOrRejects(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*BoundedResumePlan)
	}{
		{"version", func(p *BoundedResumePlan) { p.Version = 1 }},
		{"identity", func(p *BoundedResumePlan) { p.Identity.GenesisHash[0]++ }},
		{"manifest", func(p *BoundedResumePlan) { p.ManifestSHA256[0]++ }},
		{"corpus", func(p *BoundedResumePlan) { p.CorpusID[0]++ }},
		{"genesis", func(p *BoundedResumePlan) { p.GenesisFileSHA256[0]++ }},
		{"sourceproof", func(p *BoundedResumePlan) { p.SourceProofSHA256[0]++ }},
		{"mapping", func(p *BoundedResumePlan) { p.MappingSHA256[0]++ }},
		{"outerproof", func(p *BoundedResumePlan) { p.RecoveryOuterSHA256[0]++ }},
		{"recovery", func(p *BoundedResumePlan) { p.RecoverySHA256[0]++ }},
		{"vector", func(p *BoundedResumePlan) { p.AccountVectorSHA256[0]++ }},
		{"restore", func(p *BoundedResumePlan) { p.OriginalRestoreSHA256[0]++ }},
		{"height", func(p *BoundedResumePlan) { p.Canonical.Number++ }},
		{"hash", func(p *BoundedResumePlan) { p.Canonical.Hash[0]++ }},
		{"root", func(p *BoundedResumePlan) { p.Canonical.Root[0]++ }},
		{"nonce sum", func(p *BoundedResumePlan) { p.CanonicalNonceSum++ }},
		{"sender", func(p *BoundedResumePlan) { p.Senders[0].Address[0]++ }},
		{"sender nonce", func(p *BoundedResumePlan) { p.Senders[0].Nonce++ }},
		{"end", func(p *BoundedResumePlan) { p.NonceEndExclusive = 500 }},
		{"frames", func(p *BoundedResumePlan) { p.MaxSourceFrames++ }},
		{"source transactions", func(p *BoundedResumePlan) { p.MaxSourceTransactions++ }},
		{"new transactions", func(p *BoundedResumePlan) { p.MaxNewTransactions++ }},
		{"actual encoded", func(p *BoundedResumePlan) { p.SourceEncodedBytes++ }},
		{"encoded limit", func(p *BoundedResumePlan) { p.MaxSourceEncodedBytes++ }},
		{"whitelist", func(p *BoundedResumePlan) { p.WhitelistSHA256[0]++ }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, raw, baseline, _ := boundedResumeStructureFixture(t)
			before, err := BoundedResumePlanID(p, raw, baseline)
			if err != nil {
				t.Fatal(err)
			}
			tc.edit(&p)
			if after, err := BoundedResumePlanID(p, raw, baseline); err == nil && after == before {
				t.Fatal("changed binding kept its prior plan ID")
			}
		})
	}
}

func TestBoundedResumePlanRejectsInvalidGeometryVectorAndOverflow(t *testing.T) {
	p, raw, baseline, _ := boundedResumeStructureFixture(t)
	for _, end := range []uint64{0, 499, 501, 10500, ^uint64(0)} {
		changed := p
		changed.NonceEndExclusive = end
		if err := ValidateBoundedResumePlanStructure(changed, raw, baseline); err == nil {
			t.Fatalf("invalid end accepted %d", end)
		}
	}
	changed := append([]CanonicalAccount(nil), baseline...)
	changed[0].Nonce = 1001
	if err := ValidateBoundedResumePlanStructure(p, raw, changed); err == nil {
		t.Fatal("fresh canonical nonce outside the sealed prefix accepted")
	}
	if _, err := boundedResumeAdd(^uint64(0), 1); err == nil {
		t.Fatal("addition overflow accepted")
	}
	if _, err := boundedResumeMul(^uint64(0), 2); err == nil {
		t.Fatal("multiplication overflow accepted")
	}
}

func TestBoundedResumeWhitelistRejectsMissingReorderedConflictingFrames(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func([]BoundedResumeFrame) []BoundedResumeFrame
	}{
		{"missing", func(f []BoundedResumeFrame) []BoundedResumeFrame { return f[:len(f)-1] }},
		{"order", func(f []BoundedResumeFrame) []BoundedResumeFrame { f[0], f[1] = f[1], f[0]; return f }},
		{"duplicate ID", func(f []BoundedResumeFrame) []BoundedResumeFrame { f[1].ID = f[0].ID; return f }},
		{"duplicate payload", func(f []BoundedResumeFrame) []BoundedResumeFrame { f[1].PayloadHash = f[0].PayloadHash; return f }},
		{"nonce gap", func(f []BoundedResumeFrame) []BoundedResumeFrame { f[7].FirstNonce++; return f }},
		{"sender", func(f []BoundedResumeFrame) []BoundedResumeFrame { f[7].Address[0]++; return f }},
		{"rounded skip", func(f []BoundedResumeFrame) []BoundedResumeFrame {
			f[7].CanonicalSkipped--
			f[7].NewlyAdmitted++
			return f
		}},
		{"byte cap", func(f []BoundedResumeFrame) []BoundedResumeFrame { f[7].PayloadBytes = 53001; return f }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, raw, _, frames := boundedResumeStructureFixture(t)
			m, _ := DecodeCorpusManifest(raw)
			if _, _, _, err := BoundedResumeWhitelistID(m.Descriptor, p.Senders, p.NonceEndExclusive, tc.edit(frames)); err == nil {
				t.Fatal("invalid whitelist accepted")
			}
		})
	}
}

func TestBoundedResumeDocumentRejectsLegacyAliasesUnknownAndTrailing(t *testing.T) {
	p, raw, baseline, _ := boundedResumeStructureFixture(t)
	id, _ := BoundedResumePlanID(p, raw, baseline)
	doc := BoundedResumePlanDocument{Kind: "gov5-bounded-resume-prefix-plan-v2", Plan: p, PlanID: id}
	encoded, _ := json.Marshal(doc)
	if _, err := DecodeBoundedResumePlanDocument(encoded); err != nil {
		t.Fatal(err)
	}
	for _, raw := range [][]byte{[]byte(`{"kind":"x","Kind":"y"}`), []byte(`{"kind":"x","unknown":1}`), append(encoded, []byte("{}")...)} {
		if _, err := DecodeBoundedResumePlanDocument(raw); err == nil {
			t.Fatal("non-strict v2 document accepted")
		}
	}
	doc.Kind = "gov5-resume-four-frame-plan-v1"
	encoded, _ = json.Marshal(doc)
	if _, err := DecodeBoundedResumePlanDocument(encoded); err == nil {
		t.Fatal("legacy v1 content implicitly upgraded")
	}
}
