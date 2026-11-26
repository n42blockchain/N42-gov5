package hotstuff

import (
	"bufio"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto/bls"
)

func unhex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(strings.TrimPrefix(s, "0x"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestEquivocationEvidenceStandalone(t *testing.T) {
	setup := newTestSetup(t, 4)
	engine, outputCh := newTestEngine(t, setup, 1)
	h1, h2 := types.Hash{1}, types.Hash{2}
	if err := engine.ProcessEvent(ConsensusEvent{Type: EventBlockReady, Hash: h1}); err != nil {
		t.Fatal(err)
	}
	drainOutputs(outputCh)
	for _, h := range []types.Hash{h1, h2} {
		sig := setup.keys[0].Sign(SigningMessage(1, h))
		if err := engine.ProcessEvent(ConsensusEvent{Type: EventMessage,
			Msg: ConsensusMsg{Type: MsgVote, Payload: &Vote{View: 1, BlockHash: h, Voter: 0, Signature: sig.Marshal()}}}); err != nil {
			t.Fatal(err)
		}
	}
	dir := t.TempDir()
	store := NewEquivocationStore(8)
	store.SetJSONLPath(filepath.Join(dir, "equivocations.jsonl"))
	stored := 0
	for _, o := range drainOutputs(outputCh) {
		if o.Type == OutputEquivocationDetected && store.RecordOutput(o) {
			stored++
		}
	}
	if stored != 1 {
		t.Fatalf("want one evidence entry, got %d", stored)
	}
	rep := store.Report()
	if rep.Total != 1 || len(rep.Evidence) != 1 {
		t.Fatalf("report %+v", rep)
	}
	ev := rep.Evidence[0]
	if ev.Kind != EvidenceKindVote || ev.View != 1 || ev.ValidatorIndex != 0 || ev.DetectedAt == 0 {
		t.Fatalf("bad evidence %+v", ev)
	}
	pk, err := bls.PublicKeyFromBytes(unhex(t, ev.PublicKey))
	if err != nil {
		t.Fatal(err)
	}
	for i, m := range []SignedMessage{ev.First, ev.Second} {
		if !VerifyBLSSignature(unhex(t, m.Signature), pk, unhex(t, m.Signed)) {
			t.Fatalf("signature %d does not verify standalone", i)
		}
	}
	if ev.First.Signed == ev.Second.Signed {
		t.Fatal("signed messages must differ")
	}
	f, err := os.Open(filepath.Join(dir, "equivocations.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	lines := 0
	for sc.Scan() {
		var got EquivocationEvidence
		if err := json.Unmarshal(sc.Bytes(), &got); err != nil || got.Hash1 != ev.Hash1 {
			t.Fatalf("bad jsonl line: %v", err)
		}
		lines++
	}
	if lines != 1 {
		t.Fatalf("want 1 jsonl line, got %d", lines)
	}
}

func TestEquivocationStoreBoundedAndDeduped(t *testing.T) {
	s := NewEquivocationStore(2)
	mk := func(view uint64) EngineOutput {
		return EngineOutput{View: ViewNumber(view), EvidenceKind: EvidenceKindVote, Hash1: types.Hash{1}, Hash2: types.Hash{2},
			Sig1: []byte{1}, Sig2: []byte{2}, Msg1: []byte{3}, Msg2: []byte{4}, PublicKey: []byte{5}}
	}
	for v := uint64(1); v <= 3; v++ {
		if !s.RecordOutput(mk(v)) {
			t.Fatal("first record must store")
		}
	}
	if s.RecordOutput(mk(3)) {
		t.Fatal("duplicate must be ignored")
	}
	r := s.Report()
	if r.Total != 3 || len(r.Evidence) != 2 || r.Evidence[0].View != 2 {
		t.Fatalf("bounded report wrong: %+v", r)
	}
	if s.RecordOutput(EngineOutput{View: 9}) {
		t.Fatal("output without signatures must be ignored")
	}
}

func TestN42MetricIncrements(t *testing.T) {
	to, eq := metricN42ViewTimeouts.Get(), metricN42Equivocations.Get()
	updateMetricsTimeout()
	updateMetricsEquivocation()
	if metricN42ViewTimeouts.Get() != to+1 || metricN42Equivocations.Get() != eq+1 {
		t.Fatal("n42 counters not incremented with hotstuff_* counterparts")
	}
	updateMetricsBlockCommitted(77)
	updateMetricsCommittedNumber(55)
	updateMetricsCommittedNumber(0)
	if metricN42CommittedView.Get() != 77 || metricN42CommittedNumber.Get() != 55 {
		t.Fatal("committed gauges wrong")
	}
	e := &ConsensusEngine{}
	tc := metricN42TCFormed.Get()
	e.countTCAdvance(5)
	e.countTCAdvance(5)
	e.countTCAdvance(4)
	e.countTCAdvance(6)
	if metricN42TCFormed.Get() != tc+2 {
		t.Fatal("TC must count once per view")
	}
}
