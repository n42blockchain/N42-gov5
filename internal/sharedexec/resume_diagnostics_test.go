package sharedexec

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/n42blockchain/N42/internal"
)

func TestResumeDiagnosticsTerminalSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "resume.jsonl")
	d, err := newResumeDiagnostics(path)
	if err != nil {
		t.Fatal(err)
	}
	q := &strictIngressQueue{pending: 500, reservedTx: 500}
	d.frame(true)
	d.verify(true)
	time.Sleep(time.Millisecond)
	d.verify(false)
	d.frame(false)
	trace := &resumeFrameTrace{outcome: 0}
	trace.add(4, time.Millisecond)
	trace.add(11, 2*time.Millisecond)
	d.merge(trace)
	d.finish(q)
	status, err := os.ReadFile(path + ".status.json")
	if err != nil {
		t.Fatal(err)
	}
	var seal struct {
		Complete bool
		Error    string
	}
	if err := json.Unmarshal(status, &seal); err != nil || !seal.Complete || seal.Error != "" {
		t.Fatalf("diagnostics seal: %s: %v", status, err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	scan := bufio.NewScanner(f)
	if !scan.Scan() {
		t.Fatal("missing terminal record")
	}
	var row struct {
		SchemaVersion    int                    `json:"schema_version"`
		MonotonicNS      int64                  `json:"monotonic_ns"`
		MonotonicEndNS   int64                  `json:"monotonic_end_ns"`
		TerminalComplete bool                   `json:"terminal_complete"`
		ActiveFrames     int                    `json:"active_frames"`
		ActiveVerify     int                    `json:"active_verify"`
		FrameAreaNS      uint64                 `json:"frame_area_ns"`
		VerifyAreaNS     uint64                 `json:"verify_area_ns"`
		Results          map[string]uint64      `json:"results"`
		Stages           map[string]resumeStage `json:"stages"`
	}
	if err := json.Unmarshal(scan.Bytes(), &row); err != nil {
		t.Fatal(err)
	}
	if row.SchemaVersion != 1 || !row.TerminalComplete || row.MonotonicEndNS < row.MonotonicNS || row.ActiveFrames != 0 || row.ActiveVerify != 0 || row.FrameAreaNS == 0 || row.VerifyAreaNS == 0 || row.Results["admitted"] != 1 || row.Stages["verification_walk"].Count != 1 || row.Stages["first_receipt_grant_to_ack"].Count != 1 {
		t.Fatalf("bad terminal row: %+v", row)
	}
	if scan.Scan() || scan.Err() != nil {
		t.Fatalf("extra or invalid JSONL record: %v", scan.Err())
	}
}

func TestResumeDiagnosticsCloseDrainsWorkerBeforeFinalRow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "drain.jsonl")
	d, err := newResumeDiagnostics(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	q := &strictIngressQueue{}
	s := &Server{ctx: ctx, cancel: cancel, ingress: q, resumeDiagnostics: d}
	s.resumeObserverWG.Add(1)
	go d.run(s, q)
	release := make(chan struct{})
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		d.frame(true)
		<-release
		trace := &resumeFrameTrace{outcome: 4}
		trace.add(10, time.Millisecond)
		d.frame(false)
		d.merge(trace)
	}()
	done := make(chan struct{})
	go func() { s.Close(); close(done) }()
	select {
	case <-done:
		t.Fatal("Close returned before ingress worker")
	case <-time.After(5 * time.Millisecond):
	}
	close(release)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not drain observer")
	}
	s.Close() // repeated Close must not write to the sealed file
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var final struct {
		TerminalComplete bool              `json:"terminal_complete"`
		ActiveFrames     int               `json:"active_frames"`
		Results          map[string]uint64 `json:"results"`
	}
	lines := bytesSplitLines(data)
	if len(lines) == 0 || json.Unmarshal(lines[len(lines)-1], &final) != nil || !final.TerminalComplete || final.ActiveFrames != 0 || final.Results["ack_write_failed_after_publish"] != 1 {
		t.Fatalf("incomplete final row: %s", data)
	}
}

func TestResumeDiagnosticsConcurrentMergeAndWriteFailureSeal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "error.jsonl")
	d, err := newResumeDiagnostics(path)
	if err != nil {
		t.Fatal(err)
	}
	q := &strictIngressQueue{}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				tr := &resumeFrameTrace{outcome: 0}
				tr.add(4, time.Microsecond)
				d.merge(tr)
			}
		}()
	}
	for i := 0; i < 5; i++ {
		if err := d.writeSnapshot(q, false); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
	if err := d.file.Close(); err != nil {
		t.Fatal(err)
	}
	d.finish(q)
	status, err := os.ReadFile(path + ".status.json")
	if err != nil {
		t.Fatal(err)
	}
	var seal struct {
		Complete bool
		Error    string
	}
	if err := json.Unmarshal(status, &seal); err != nil || seal.Complete || seal.Error == "" {
		t.Fatalf("unsealed writer failure: %s: %v", status, err)
	}
}

func TestResumeDiagnosticsAccountObserverAfterUnlock(t *testing.T) {
	f := newResumeExecutionFixture(t)
	called := false
	_, outerErr := f.chain.ReadPerformanceCanonicalAccountsObserved(context.Background(), nil, func(lockWait, joinLag, read time.Duration) {
		called = true
		if lockWait < 0 || joinLag < 0 || read < 0 {
			t.Errorf("negative account phase: %v %v %v", lockWait, joinLag, read)
		}
		done := make(chan struct{}, 1)
		go func() { _, _ = f.chain.ReadPerformanceCanonicalAccounts(context.Background(), nil); done <- struct{}{} }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("observer held BlockChain mutation lock")
		}
	})
	if !called {
		t.Fatal("account observer was not called")
	}
	if outerErr != nil && !errors.Is(outerErr, internal.ErrPerformanceCanonicalNotApplied) {
		t.Fatal(outerErr)
	}
}

func bytesSplitLines(data []byte) [][]byte {
	var lines [][]byte
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if len(line) > 0 {
			lines = append(lines, line)
		}
	}
	return lines
}
