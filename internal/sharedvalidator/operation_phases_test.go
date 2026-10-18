package sharedvalidator

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestOperationPhasesDisjointIntervalsAndUnaccounted(t *testing.T) {
	start := time.Unix(123, 0)
	p := &operationPhases{operation: "produce", path: "appliedChild", started: start, changed: start, elapsed: make(map[string]int64)}
	p.phaseAt("produce_mu_wait", start.Add(time.Millisecond))
	p.phaseAt("applied_child_rpc", start.Add(3*time.Millisecond))
	p.phaseAt("", start.Add(8*time.Millisecond))
	p.phaseAt("applied_child_rpc", start.Add(10*time.Millisecond))
	value := p.record(start.Add(14*time.Millisecond), nil)
	if value.SchemaVersion != 1 || value.Operation != "produce" || value.Path != "appliedChild" || value.Outcome != "ok" || value.LastPhase != "applied_child_rpc" || value.StartedUnixMicros != start.UnixMicro() {
		t.Fatalf("incorrect operation binding: %+v", value)
	}
	if value.TotalNs != int64(14*time.Millisecond) || value.AccountedNs != int64(11*time.Millisecond) || value.UnaccountedNs != int64(3*time.Millisecond) || value.PhaseNs["produce_mu_wait"] != int64(2*time.Millisecond) || value.PhaseNs["applied_child_rpc"] != int64(9*time.Millisecond) {
		t.Fatalf("overlapping or missing operation time: %+v", value)
	}
	p.elapsed["applied_child_rpc"]++
	if value.PhaseNs["applied_child_rpc"] != int64(9*time.Millisecond) {
		t.Fatal("finished record aliases mutable operation phases")
	}
}

func TestOperationPhasesFailedActivePhaseAndPrivateError(t *testing.T) {
	for _, test := range []struct {
		err     error
		outcome string
	}{
		{errors.New("secret transaction payload"), "error"},
		{context.Canceled, "canceled"},
		{context.DeadlineExceeded, "deadline"},
	} {
		start := time.Unix(123, 0)
		p := &operationPhases{operation: "import", started: start, changed: start, elapsed: make(map[string]int64), rawBytes: 1000, transactions: 500}
		p.phaseAt("import_rpc", start.Add(time.Millisecond))
		value := p.record(start.Add(6*time.Millisecond), test.err)
		if value.Outcome != test.outcome || value.LastPhase != "import_rpc" || value.PhaseNs["import_rpc"] != int64(5*time.Millisecond) || value.RawBytes != 1000 || value.Transactions != 500 {
			t.Fatalf("failed operation lost its active interval: %+v", value)
		}
		raw, err := json.Marshal(value)
		if err != nil || strings.Contains(string(raw), "secret") {
			t.Fatalf("phase record includes arbitrary error text: %s, %v", raw, err)
		}
	}
}

func TestOperationPhasesEarlyReplicaAndProducerErrorsReturn(t *testing.T) {
	// Real entrypoints must still return early errors before any DB/engine IO.
	r := new(Replica)
	if _, err := r.Import(context.Background(), nil); err == nil {
		t.Fatal("invalid availability payload accepted")
	}
	if err := r.CommitCertificate(context.Background(), nil); err == nil {
		t.Fatal("missing certificate accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := &Producer{ctx: ctx}
	if _, err := p.Produce(ctx, [32]byte{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled producer did not preserve context error: %v", err)
	}
}
