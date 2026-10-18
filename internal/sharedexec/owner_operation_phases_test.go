package sharedexec

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestOwnerOperationPhasesDisjointIntervalsIncludeDeferredReconcile(t *testing.T) {
	start := time.Unix(123, 0)
	p := &ownerOperationPhases{operation: "commit", started: start, changed: start, elapsed: make(map[string]int64), duplicate: true}
	p.phaseAt("execute_wait", start.Add(time.Millisecond))
	p.phaseAt("canonical_commit", start.Add(3*time.Millisecond))
	p.phaseAt("ingress_sender_recovery", start.Add(4*time.Millisecond))
	p.phaseAt("ingress_queue_apply", start.Add(9*time.Millisecond))
	p.phaseAt("", start.Add(10*time.Millisecond))
	r := p.record(start.Add(12*time.Millisecond), nil)
	if r.SchemaVersion != 1 || r.Operation != "commit" || r.Outcome != "ok" || !r.Duplicate || r.LastPhase != "ingress_queue_apply" || r.TotalNs != int64(12*time.Millisecond) || r.AccountedNs != int64(9*time.Millisecond) || r.UnaccountedNs != int64(3*time.Millisecond) || r.PhaseNs["ingress_sender_recovery"] != int64(5*time.Millisecond) {
		t.Fatal("owner intervals omit reconcile or overlap", r)
	}
	p.elapsed["ingress_sender_recovery"]++
	if r.PhaseNs["ingress_sender_recovery"] != int64(5*time.Millisecond) {
		t.Fatal("record aliases live phase map")
	}
	var decoded ownerOperationPhaseRecord
	if err := json.Unmarshal([]byte(r.String()), &decoded); err != nil || decoded.TotalNs != r.TotalNs {
		t.Fatal("console record is not structured JSON", err)
	}
}

func TestOwnerOperationPhasesErrorsDoNotExposePrivateText(t *testing.T) {
	for _, c := range []struct {
		err     error
		outcome string
	}{{errors.New("secret payload"), "error"}, {context.Canceled, "canceled"}, {context.DeadlineExceeded, "deadline"}} {
		p := newOwnerOperationPhases("applied_child", nil)
		p.phase("execute_wait")
		r := p.record(time.Now(), c.err)
		if r.Outcome != c.outcome || r.LastPhase != "execute_wait" || strings.Contains(r.String(), "secret") || !json.Valid([]byte(r.String())) {
			t.Fatal("owner failure record differs", r)
		}
	}
}

func TestOwnerOperationPhasesEarlyEntrypointsRemainFailClosed(t *testing.T) {
	s := new(Server)
	if _, err := s.commit(context.Background(), CommitRequest{}, nil); err == nil {
		t.Fatal("empty commit accepted")
	}
	if _, err := s.AppliedChild(context.Background(), AppliedChildRequest{}); err == nil {
		t.Fatal("empty applied child accepted")
	}
	var absent *ownerOperationPhases
	absent.phase("not enabled")
	absent.pause()
}

func TestOwnerOperationPhasesHTTPMalformedRequestRemainsFailClosed(t *testing.T) {
	s := new(Server)
	for _, input := range []string{`{`, `{} {}`} {
		w := httptest.NewRecorder()
		s.serveAppliedChild(w, httptest.NewRequest(http.MethodPost, "/v1/applied-child", strings.NewReader(input)))
		if w.Code != http.StatusBadRequest {
			t.Fatal("malformed HTTP request accepted", w.Code)
		}
	}
}
