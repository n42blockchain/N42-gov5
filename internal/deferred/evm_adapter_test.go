package deferred

import (
	"testing"
)

func TestNewCommitFunc(t *testing.T) {
	commitFn := NewCommitFunc()
	result := &ExecutionResult{
		BlockNumber: 1,
		GasUsed:     21000,
	}
	// Should not error
	if err := commitFn(result); err != nil {
		t.Fatalf("NewCommitFunc returned error: %v", err)
	}
}

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.QueueSize != 64 {
		t.Errorf("QueueSize = %d, want 64", cfg.QueueSize)
	}
	if cfg.Workers != 1 {
		t.Errorf("Workers = %d, want 1", cfg.Workers)
	}
}

func TestDefaultPipelineConfig(t *testing.T) {
	cfg := DefaultPipelineConfig()
	if cfg.CommitInterval <= 0 {
		t.Error("CommitInterval should be positive")
	}
	if cfg.Execution.QueueSize != 64 {
		t.Errorf("Execution.QueueSize = %d, want 64", cfg.Execution.QueueSize)
	}
}
