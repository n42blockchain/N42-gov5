package staged

import (
	"context"
	"fmt"
	"testing"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
)

func newTestDB(t *testing.T) kv.RwDB {
	t.Helper()
	return memdb.NewTestDB(t)
}

func TestPipelineForwardProgress(t *testing.T) {
	db := newTestDB(t)
	var executionOrder []string

	stages := []*Stage{
		{
			ID:          StageHeaders,
			Description: "Download headers",
			Forward: func(ctx context.Context, s *StageState, tx kv.RwTx, target uint64) (uint64, error) {
				executionOrder = append(executionOrder, string(StageHeaders))
				return target, nil
			},
		},
		{
			ID:          StageBodies,
			Description: "Download bodies",
			Forward: func(ctx context.Context, s *StageState, tx kv.RwTx, target uint64) (uint64, error) {
				executionOrder = append(executionOrder, string(StageBodies))
				return target, nil
			},
		},
		{
			ID:          StageFinish,
			Description: "Finish",
			Forward: func(ctx context.Context, s *StageState, tx kv.RwTx, target uint64) (uint64, error) {
				executionOrder = append(executionOrder, string(StageFinish))
				return target, nil
			},
		},
	}

	pipeline := NewPipeline(db, stages)
	err := pipeline.Run(context.Background(), 100)
	if err != nil {
		t.Fatalf("pipeline.Run() error: %v", err)
	}

	if len(executionOrder) != 3 {
		t.Fatalf("expected 3 stages executed, got %d", len(executionOrder))
	}
	if executionOrder[0] != "Headers" || executionOrder[1] != "Bodies" || executionOrder[2] != "Finish" {
		t.Fatalf("wrong execution order: %v", executionOrder)
	}

	progress, err := pipeline.Progress()
	if err != nil {
		t.Fatalf("Progress() error: %v", err)
	}
	for _, stage := range stages {
		if progress[stage.ID] != 100 {
			t.Errorf("stage %s progress = %d, want 100", stage.ID, progress[stage.ID])
		}
	}
}

func TestPipelineSkipsCompletedStages(t *testing.T) {
	db := newTestDB(t)
	callCount := 0

	stages := []*Stage{
		{
			ID: StageHeaders,
			Forward: func(ctx context.Context, s *StageState, tx kv.RwTx, target uint64) (uint64, error) {
				callCount++
				return target, nil
			},
		},
	}

	pipeline := NewPipeline(db, stages)

	// First run
	if err := pipeline.Run(context.Background(), 50); err != nil {
		t.Fatal(err)
	}
	if callCount != 1 {
		t.Fatalf("expected 1 call, got %d", callCount)
	}

	// Second run with same target — should skip
	if err := pipeline.Run(context.Background(), 50); err != nil {
		t.Fatal(err)
	}
	if callCount != 1 {
		t.Fatalf("expected still 1 call (skipped), got %d", callCount)
	}

	// Third run with higher target — should execute
	if err := pipeline.Run(context.Background(), 100); err != nil {
		t.Fatal(err)
	}
	if callCount != 2 {
		t.Fatalf("expected 2 calls, got %d", callCount)
	}
}

func TestPipelineUnwind(t *testing.T) {
	db := newTestDB(t)
	var unwoundStages []string

	stages := []*Stage{
		{
			ID: StageHeaders,
			Forward: func(ctx context.Context, s *StageState, tx kv.RwTx, target uint64) (uint64, error) {
				return target, nil
			},
			Unwind: func(ctx context.Context, u *UnwindState, tx kv.RwTx) error {
				unwoundStages = append(unwoundStages, string(StageHeaders))
				return nil
			},
		},
		{
			ID: StageBodies,
			Forward: func(ctx context.Context, s *StageState, tx kv.RwTx, target uint64) (uint64, error) {
				return target, nil
			},
			Unwind: func(ctx context.Context, u *UnwindState, tx kv.RwTx) error {
				unwoundStages = append(unwoundStages, string(StageBodies))
				return nil
			},
		},
		{
			ID: StageExecution,
			Forward: func(ctx context.Context, s *StageState, tx kv.RwTx, target uint64) (uint64, error) {
				return target, nil
			},
			Unwind: func(ctx context.Context, u *UnwindState, tx kv.RwTx) error {
				unwoundStages = append(unwoundStages, string(StageExecution))
				return nil
			},
		},
	}

	pipeline := NewPipeline(db, stages)

	// Forward to block 100
	if err := pipeline.Run(context.Background(), 100); err != nil {
		t.Fatal(err)
	}

	// Request unwind to block 50
	pipeline.RequestUnwind(50)
	if err := pipeline.Run(context.Background(), 100); err != nil {
		t.Fatal(err)
	}

	// Should unwind in reverse order
	if len(unwoundStages) != 3 {
		t.Fatalf("expected 3 unwinds, got %d: %v", len(unwoundStages), unwoundStages)
	}
	if unwoundStages[0] != "Execution" || unwoundStages[1] != "Bodies" || unwoundStages[2] != "Headers" {
		t.Fatalf("wrong unwind order: %v", unwoundStages)
	}

	// Verify progress is reset to 50
	progress, _ := pipeline.Progress()
	for _, stage := range stages {
		if progress[stage.ID] != 50 {
			t.Errorf("stage %s progress = %d after unwind, want 50", stage.ID, progress[stage.ID])
		}
	}
}

func TestPipelineStageError(t *testing.T) {
	db := newTestDB(t)

	stages := []*Stage{
		{
			ID: StageHeaders,
			Forward: func(ctx context.Context, s *StageState, tx kv.RwTx, target uint64) (uint64, error) {
				return 0, fmt.Errorf("download failed")
			},
		},
	}

	pipeline := NewPipeline(db, stages)
	err := pipeline.Run(context.Background(), 100)
	if err == nil {
		t.Fatal("expected error")
	}
	if err.Error() != "stage Headers failed: download failed" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPipelineCancellation(t *testing.T) {
	db := newTestDB(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	stages := []*Stage{
		{
			ID: StageHeaders,
			Forward: func(ctx context.Context, s *StageState, tx kv.RwTx, target uint64) (uint64, error) {
				t.Fatal("should not be called")
				return 0, nil
			},
		},
	}

	pipeline := NewPipeline(db, stages)
	err := pipeline.Run(ctx, 100)
	if err != context.Canceled {
		t.Fatalf("expected context.Canceled, got: %v", err)
	}
}
