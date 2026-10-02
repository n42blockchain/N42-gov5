package diagnostics

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

func newTestClient(t *testing.T) *DiagnosticClient {
	t.Helper()
	ctx := context.Background()
	c, err := NewDiagnosticClient(ctx, http.NewServeMux(), t.TempDir(), false, nil)
	if err != nil {
		t.Fatalf("NewDiagnosticClient: %v", err)
	}
	t.Cleanup(func() { c.db.Close() })
	return c
}

func TestNewDiagnosticClientAndSaveReadRoundTrip(t *testing.T) {
	c := newTestClient(t)

	c.syncStages = []SyncStage{{ID: "stage1"}}

	c.SaveData()

	hinfo, stages, _, _, _ := ReadSavedData(c.db)
	_ = hinfo
	if len(stages) != 1 || stages[0].ID != "stage1" {
		t.Fatalf("ReadSavedData stages = %v, want one stage named stage1", stages)
	}
}

func TestBlockEexcStatsDataSetAndJSON(t *testing.T) {
	c := newTestClient(t)

	want := BlockExecutionStatistics{
		From:        1,
		To:          2,
		BlockNumber: 2,
		BlkPerSec:   3.5,
	}
	c.BlockExecution.SetData(want)
	if got := c.BlockExecution.Data(); got != want {
		t.Fatalf("Data() = %+v, want %+v", got, want)
	}

	var buf bytes.Buffer
	c.BlockExecutionInfoJson(&buf)

	var got BlockExecutionStatistics
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got != want {
		t.Fatalf("decoded JSON = %+v, want %+v", got, want)
	}
}

func TestResourcesUsageJsonResetsAfterRead(t *testing.T) {
	c := newTestClient(t)
	c.resourcesUsage.MemoryUsage = append(c.resourcesUsage.MemoryUsage, MemoryStats{Alloc: 42})

	var buf bytes.Buffer
	c.ResourcesUsageJson(&buf)

	var got ResourcesUsage
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(got.MemoryUsage) != 1 || got.MemoryUsage[0].Alloc != 42 {
		t.Fatalf("got %+v, want one entry with Alloc=42", got)
	}

	// The call drains resourcesUsage, so a second read is empty.
	var buf2 bytes.Buffer
	c.ResourcesUsageJson(&buf2)
	var got2 ResourcesUsage
	if err := json.Unmarshal(buf2.Bytes(), &got2); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(got2.MemoryUsage) != 0 {
		t.Fatalf("expected drained resourcesUsage, got %+v", got2)
	}
}

func TestRunBlockExecutionListenerReceivesUpdates(t *testing.T) {
	c := newTestClient(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c.runBlockExecutionListener(ctx)

	want := BlockExecutionStatistics{BlockNumber: 7}
	// Give the listener goroutine a moment to subscribe before sending;
	// retry a few times instead of a single fixed sleep.
	deadline := time.Now().Add(200 * time.Millisecond)
	for {
		Send(want)
		if c.BlockExecution.Data() == want {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("listener did not observe sent BlockExecutionStatistics in time")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestRunMemoryStatsListenerReceivesUpdates(t *testing.T) {
	c := newTestClient(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c.runMemoryStatsListener(ctx)

	want := MemoryStats{Alloc: 99}
	deadline := time.Now().Add(200 * time.Millisecond)
	for {
		Send(want)
		c.resourcesUsageMutex.Lock()
		n := len(c.resourcesUsage.MemoryUsage)
		c.resourcesUsageMutex.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("listener did not observe sent MemoryStats in time")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
