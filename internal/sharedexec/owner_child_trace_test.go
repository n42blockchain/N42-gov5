package sharedexec

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
)

func TestOwnerChildTraceBoundedSummaryAndFreshFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "owner-trace.jsonl")
	s := new(Server)
	if err := s.EnableOwnerChildTrace(path); err != nil {
		t.Fatal(err)
	}
	if err := s.EnableOwnerChildTrace(path); err == nil {
		t.Fatal("second trace attached to one owner")
	}
	anchor := types.Hash{1}
	start := time.Now()
	for i := 0; i < maxOwnerTraceEventsPerBlock+3; i++ {
		s.trace.record(start.Add(time.Duration(i)*time.Nanosecond), "evm_ordinary_begin", "started", anchor, 2, 3, [16]byte{4}, types.Hash{}, 1)
	}
	if err := s.trace.close(); err != nil {
		t.Fatal(err)
	}
	completionRaw, err := os.ReadFile(path + ".complete")
	if err != nil {
		t.Fatal(err)
	}
	var completion ownerTraceCompletion
	if err := json.Unmarshal(completionRaw, &completion); err != nil {
		t.Fatal(err)
	}
	traceRaw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(traceRaw)
	if completion.SHA256 != hex.EncodeToString(sum[:]) || completion.Accepted != maxOwnerTraceEventsPerBlock || completion.Dropped != 3 || completion.WriteErrs != 0 {
		t.Fatalf("incomplete completion: %+v", completion)
	}
	if err := new(Server).EnableOwnerChildTrace(path); err == nil {
		t.Fatal("existing trace file overwritten")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	scan := bufio.NewScanner(f)
	var events int
	var summary ownerTraceRecord
	for scan.Scan() {
		var record ownerTraceRecord
		if err := json.Unmarshal(scan.Bytes(), &record); err != nil {
			t.Fatal(err)
		}
		if record.Kind == "event" {
			events++
			if record.Seq != uint64(events) || record.Anchor != anchor || record.Number != 2 || record.View != 3 || record.Run == "" || record.Origin != "build" {
				t.Fatalf("unbound event: %+v", record)
			}
		} else if record.Kind == "summary" {
			summary = record
		}
	}
	if err := scan.Err(); err != nil {
		t.Fatal(err)
	}
	if events != maxOwnerTraceEventsPerBlock || summary.Accepted != uint64(events) || summary.Dropped != 3 || summary.WriteErrs != 0 {
		t.Fatalf("trace accepted=%d dropped=%d errors=%d events=%d", summary.Accepted, summary.Dropped, summary.WriteErrs, events)
	}
}

func TestOwnerChildTraceOriginsDistinguishImportFromBuild(t *testing.T) {
	for event, want := range map[string]string{
		"evm_speculative_begin":    "speculative",
		"evm_ordinary_end":         "build",
		"import_chain_begin":       "import",
		"db_update_callback_begin": "canonical",
	} {
		if got := ownerTraceOrigin(event); got != want {
			t.Fatalf("event %s origin %s, want %s", event, got, want)
		}
	}
}
