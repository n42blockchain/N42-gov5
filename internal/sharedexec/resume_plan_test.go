package sharedexec

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"sort"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
)

// All hashes/addresses here are synthetic metadata. This fixture makes no
// claim to have verified a source file, recovered a signer or audited a DB.
func resumeStructureFixture(t *testing.T) (ResumePlan, []byte, []CanonicalAccount) {
	t.Helper()
	d := corpusDescriptorForTest()
	d.NonceStart, d.NoncesPerSender, d.FrameTransactions = 0, 1000, 500
	d.RecipientCount = 4
	id, err := d.ID()
	if err != nil {
		t.Fatal(err)
	}
	m := CorpusManifest{Descriptor: d, CorpusID: id, Shards: []CorpusShard{
		{Index: 0, File: "shard-00.gcf", Transactions: 2000, Frames: 4, Bytes: 1 << 20, SHA256: types.Hash{20}},
		{Index: 1, File: "shard-01.gcf", Transactions: 2000, Frames: 4, Bytes: 1 << 20, SHA256: types.Hash{21}},
	}}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	p := ResumePlan{Version: 1, Identity: d.Identity, ManifestSHA256: types.Hash(sha256.Sum256(raw)), CorpusID: id, GenesisFileSHA256: d.GenesisFileHash, SourceProofSHA256: types.Hash{4}, RecoveryOuterSHA256: types.Hash{5}, RecoverySHA256: types.Hash{6}, OriginalRestoreSHA256: types.Hash{7}, Canonical: ResumeCanonicalAnchor{Number: 3, Hash: types.Hash{8}, Root: types.Hash{9}}, CanonicalNonceSum: 583}
	// Logical sender order deliberately differs from address order.
	for i, item := range []struct {
		address byte
		nonce   uint64
	}{{4, 0}, {1, 41}, {3, 42}, {2, 500}} {
		address := types.Address{19: item.address}
		p.Senders = append(p.Senders, ResumeSender{Index: uint32(i), Address: address, Nonce: item.nonce})
		p.Frames = append(p.Frames, ResumeFrame{ID: CorpusFrameID{CorpusID: id, Shard: uint16(i % 2), Sequence: uint64(i / 2)}, SenderIndex: uint32(i), PayloadHash: types.Hash{byte(10 + i)}, PayloadBytes: 54000, CanonicalSkipped: uint16(item.nonce), NewlyAdmitted: uint16(500 - item.nonce)})
	}
	p.MappingSHA256, err = ResumeMappingID(p.Senders)
	if err != nil {
		t.Fatal(err)
	}
	baseline := make([]CanonicalAccount, len(p.Senders))
	for i, sender := range p.Senders {
		baseline[i] = CanonicalAccount{Address: sender.Address, Exists: true, Nonce: sender.Nonce, Balance: "1000000000", CodeHash: crypto.Keccak256Hash(nil)}
	}
	sort.Slice(baseline, func(i, j int) bool { return bytes.Compare(baseline[i].Address[:], baseline[j].Address[:]) < 0 })
	vector, err := json.Marshal(baseline)
	if err != nil {
		t.Fatal(err)
	}
	p.AccountVectorSHA256 = types.Hash(sha256.Sum256(vector))
	return p, raw, baseline
}

func TestResumePlanStructureBindsAddressJoinedPartialPrefixes(t *testing.T) {
	p, raw, baseline := resumeStructureFixture(t)
	id, err := ResumePlanID(p, raw, baseline)
	if err != nil {
		t.Fatal(err)
	}
	// Fixed synthetic metadata golden, independently encoded with Python
	// struct.pack little-endian widths and hashlib.sha256. This is not a
	// source-authentication or transaction-signature interoperability claim.
	if id.Hex() != "0xd8a783c8c9fc6389a48664113fdbe0a582d712b567d154414ea15cedc3f691c7" || p.MappingSHA256.Hex() != "0x8a8344766de09386b274acecd040e4b23cfbce715c74a052eaadb01f5ad11db7" {
		t.Fatalf("resume encoding golden changed: plan=%s mapping=%s", id.Hex(), p.MappingSHA256.Hex())
	}
	if again, err := ResumePlanID(p, raw, baseline); err != nil || again != id {
		t.Fatal("same input changed its plan ID", err)
	}
	var skipped, newly uint64
	for _, frame := range p.Frames {
		skipped += uint64(frame.CanonicalSkipped)
		newly += uint64(frame.NewlyAdmitted)
	}
	if skipped != 583 || newly != 1417 || p.Frames[1].NewlyAdmitted != 459 || p.Frames[2].NewlyAdmitted != 458 || p.Frames[3].NewlyAdmitted != 0 {
		t.Fatal("partial prefixes were rounded or canonical skip was counted as new")
	}
}

func TestResumePlanStructureRejectsBindingRangeAndVectorErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*ResumePlan, *[]byte, *[]CanonicalAccount)
	}{
		{"identity", func(p *ResumePlan, _ *[]byte, _ *[]CanonicalAccount) { p.Identity.GenesisHash[0]++ }},
		{"manifest bytes", func(_ *ResumePlan, raw *[]byte, _ *[]CanonicalAccount) { *raw = append(*raw, ' ') }},
		{"corpus", func(p *ResumePlan, _ *[]byte, _ *[]CanonicalAccount) { p.CorpusID[0]++ }},
		{"genesis file", func(p *ResumePlan, _ *[]byte, _ *[]CanonicalAccount) { p.GenesisFileSHA256[0]++ }},
		{"missing source proof", func(p *ResumePlan, _ *[]byte, _ *[]CanonicalAccount) { p.SourceProofSHA256 = types.Hash{} }},
		{"mapping digest", func(p *ResumePlan, _ *[]byte, _ *[]CanonicalAccount) { p.MappingSHA256[0]++ }},
		{"sender position", func(p *ResumePlan, _ *[]byte, _ *[]CanonicalAccount) { p.Senders[0].Index = 1 }},
		{"duplicate address", func(p *ResumePlan, _ *[]byte, _ *[]CanonicalAccount) { p.Senders[0].Address = p.Senders[1].Address }},
		{"missing sender", func(p *ResumePlan, _ *[]byte, _ *[]CanonicalAccount) { p.Senders = p.Senders[:3] }},
		{"position instead of address join", func(p *ResumePlan, _ *[]byte, b *[]CanonicalAccount) {
			for i := range p.Senders {
				p.Senders[i].Nonce = (*b)[i].Nonce
			}
		}},
		{"unordered account vector", func(_ *ResumePlan, _ *[]byte, b *[]CanonicalAccount) { (*b)[0], (*b)[1] = (*b)[1], (*b)[0] }},
		{"missing account", func(_ *ResumePlan, _ *[]byte, b *[]CanonicalAccount) { (*b)[0].Exists = false }},
		{"contract account", func(_ *ResumePlan, _ *[]byte, b *[]CanonicalAccount) { (*b)[0].CodeHash[0]++ }},
		{"noncanonical balance", func(_ *ResumePlan, _ *[]byte, b *[]CanonicalAccount) { (*b)[0].Balance = "01000000000" }},
		{"insufficient balance", func(_ *ResumePlan, _ *[]byte, b *[]CanonicalAccount) { (*b)[0].Balance = "1" }},
		{"nonce outside source", func(_ *ResumePlan, _ *[]byte, b *[]CanonicalAccount) { (*b)[0].Nonce = 1001 }},
		{"nonce sum", func(p *ResumePlan, _ *[]byte, _ *[]CanonicalAccount) { p.CanonicalNonceSum++ }},
		{"account digest", func(p *ResumePlan, _ *[]byte, _ *[]CanonicalAccount) { p.AccountVectorSHA256[0]++ }},
		{"height overflow", func(p *ResumePlan, _ *[]byte, _ *[]CanonicalAccount) { p.Canonical.Number = ^uint64(0) }},
		{"zero root", func(p *ResumePlan, _ *[]byte, _ *[]CanonicalAccount) { p.Canonical.Root = types.Hash{} }},
		{"frame order", func(p *ResumePlan, _ *[]byte, _ *[]CanonicalAccount) {
			p.Frames[0], p.Frames[1] = p.Frames[1], p.Frames[0]
		}},
		{"next original frame", func(p *ResumePlan, _ *[]byte, _ *[]CanonicalAccount) { p.Frames[0].ID.Sequence += 2 }},
		{"rounded down prefix", func(p *ResumePlan, _ *[]byte, _ *[]CanonicalAccount) {
			p.Frames[1].CanonicalSkipped, p.Frames[1].NewlyAdmitted = 0, 500
		}},
		{"rounded up prefix", func(p *ResumePlan, _ *[]byte, _ *[]CanonicalAccount) {
			p.Frames[2].CanonicalSkipped, p.Frames[2].NewlyAdmitted = 500, 0
		}},
		{"oversized payload", func(p *ResumePlan, _ *[]byte, _ *[]CanonicalAccount) {
			p.Frames[0].PayloadBytes = CorpusMaxFramePayload + 1
		}},
		{"empty payload hash", func(p *ResumePlan, _ *[]byte, _ *[]CanonicalAccount) { p.Frames[0].PayloadHash = types.Hash{} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, raw, baseline := resumeStructureFixture(t)
			tc.change(&p, &raw, &baseline)
			if _, err := ResumePlanID(p, raw, baseline); err == nil {
				t.Fatal("invalid resume structure acquired a plan ID")
			}
		})
	}
}

func TestResumePlanIDBindsEveryProofAndPayloadWhileMappingOmitsNonce(t *testing.T) {
	p, raw, baseline := resumeStructureFixture(t)
	before, err := ResumePlanID(p, raw, baseline)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*ResumePlan){
		func(p *ResumePlan) { p.SourceProofSHA256[0]++ },
		func(p *ResumePlan) { p.RecoveryOuterSHA256[0]++ },
		func(p *ResumePlan) { p.RecoverySHA256[0]++ },
		func(p *ResumePlan) { p.OriginalRestoreSHA256[0]++ },
		func(p *ResumePlan) { p.Canonical.Number++ },
		func(p *ResumePlan) { p.Canonical.Hash[0]++ },
		func(p *ResumePlan) { p.Canonical.Root[0]++ },
		func(p *ResumePlan) { p.Frames[0].PayloadHash[0]++ },
		func(p *ResumePlan) { p.Frames[0].PayloadBytes++ },
	} {
		changed, _, _ := resumeStructureFixture(t)
		change(&changed)
		if after, err := ResumePlanID(changed, raw, baseline); err != nil || after == before {
			t.Fatal("offline binding omitted from the plan ID", err)
		}
	}
	mapping, err := ResumeMappingID(p.Senders)
	if err != nil {
		t.Fatal(err)
	}
	p.Senders[0].Nonce++
	if after, err := ResumeMappingID(p.Senders); err != nil || after != mapping {
		t.Fatal("nonce leaked into public mapping ID", err)
	}
	if _, err := ResumePlanID(p, raw, baseline); err == nil {
		t.Fatal("nonce changed without its canonical vector binding")
	}
}

func TestResumePlanStructureSelectsMinimumLogicalIndexInEachNonceBucket(t *testing.T) {
	p, raw, baseline := resumeStructureFixture(t)
	m, err := DecodeCorpusManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	m.Descriptor.SenderCount = 6
	m.CorpusID, err = m.Descriptor.ID()
	if err != nil {
		t.Fatal(err)
	}
	for i := range m.Shards {
		m.Shards[i].Transactions, m.Shards[i].Frames = 3000, 6
	}
	for i, nonce := range []uint64{41, 500} {
		address := types.Address{19: byte(5 + i)}
		p.Senders = append(p.Senders, ResumeSender{Index: uint32(4 + i), Address: address, Nonce: nonce})
		baseline = append(baseline, CanonicalAccount{Address: address, Exists: true, Nonce: nonce, Balance: "1000000000", CodeHash: crypto.Keccak256Hash(nil)})
		p.CanonicalNonceSum += nonce
	}
	raw, err = json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	p.ManifestSHA256, p.CorpusID = types.Hash(sha256.Sum256(raw)), m.CorpusID
	p.MappingSHA256, err = ResumeMappingID(p.Senders)
	if err != nil {
		t.Fatal(err)
	}
	vector, err := json.Marshal(baseline)
	if err != nil {
		t.Fatal(err)
	}
	p.AccountVectorSHA256 = types.Hash(sha256.Sum256(vector))
	for i := range p.Frames {
		p.Frames[i].ID.CorpusID = m.CorpusID
	}
	if err := ValidateResumePlanStructure(p, raw, baseline); err != nil {
		t.Fatal("minimum-index selection rejected", err)
	}
	// Sender four has the same canonical nonce as sender one, but the
	// predeclared v1 policy cannot silently select that later logical index.
	p.Frames[1].SenderIndex = 4
	p.Frames[1].ID.Shard, p.Frames[1].ID.Sequence = 0, 2
	sort.Slice(p.Frames, func(i, j int) bool { return p.Frames[i].SenderIndex < p.Frames[j].SenderIndex })
	if err := ValidateResumePlanStructure(p, raw, baseline); err == nil {
		t.Fatal("a later sender in the same nonce bucket replaced the fixed minimum")
	}
}

func TestResumePlanStructureCannotAuthenticatePermutedSourceMapping(t *testing.T) {
	p, raw, baseline := resumeStructureFixture(t)
	before, err := ResumePlanID(p, raw, baseline)
	if err != nil {
		t.Fatal(err)
	}
	p.Senders[0].Address, p.Senders[1].Address = p.Senders[1].Address, p.Senders[0].Address
	p.Senders[0].Nonce, p.Senders[1].Nonce = p.Senders[1].Nonce, p.Senders[0].Nonce
	p.MappingSHA256, err = ResumeMappingID(p.Senders)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		p.Frames[i].CanonicalSkipped = uint16(p.Senders[i].Nonce)
		p.Frames[i].NewlyAdmitted = 500 - p.Frames[i].CanonicalSkipped
	}
	// With no original signed source bytes, a complete bijection can still
	// satisfy structural checks. Only the future source scanner can prove
	// which address actually signed each logical sender's original frame.
	after, err := ResumePlanID(p, raw, baseline)
	if err != nil || after == before {
		t.Fatal("structural validation confused source authentication with metadata binding", err)
	}
}
