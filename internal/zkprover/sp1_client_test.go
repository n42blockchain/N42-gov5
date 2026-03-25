package zkprover

import (
	"context"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
)

func TestSP1ClientSimulationSmoke(t *testing.T) {
	// Create SP1 client in simulation mode (no network endpoint)
	client := NewSP1ProverClient("", "", "")
	defer client.Close()

	// Build a minimal guest input for smoke testing.
	// In a real scenario, this would be built by input_builder.BuildGuestInput().
	// For the smoke test, we use an empty input that the guest program can handle.
	blockHash := types.HexToHash("0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef")
	blockNumber := uint64(42)

	// The guest Execute() will fail with empty input (no valid block data),
	// but we're testing the SP1 client job lifecycle, not the guest execution.
	guestInput := []byte{} // empty input → guest will return error

	ctx := context.Background()

	// Submit a job
	jobID, err := client.Submit(ctx, blockHash, blockNumber, guestInput)
	if err != nil {
		t.Fatalf("Submit failed: %v", err)
	}
	if jobID == "" {
		t.Fatal("Expected non-empty job ID")
	}
	t.Logf("Job submitted: %s", jobID)

	// Wait for job to complete (or fail, since input is empty)
	deadline := time.After(5 * time.Second)
	for {
		proof, err := client.Status(ctx, jobID)
		if err != nil {
			// Expected: guest execution will fail with empty input
			t.Logf("Job failed as expected with empty input: %v", err)
			return
		}
		if proof != nil {
			t.Logf("Unexpected success: proof generated for empty input")
			// Verify proof structure
			if proof.Type != ProofTypeSP1 {
				t.Errorf("proof.Type = %s, want %s", proof.Type, ProofTypeSP1)
			}
			if proof.BlockNumber != blockNumber {
				t.Errorf("proof.BlockNumber = %d, want %d", proof.BlockNumber, blockNumber)
			}
			return
		}
		select {
		case <-deadline:
			t.Fatal("Timed out waiting for SP1 job")
		case <-time.After(100 * time.Millisecond):
			continue
		}
	}
}

func TestSP1ClientJobLifecycle(t *testing.T) {
	client := NewSP1ProverClient("", "", "")
	defer client.Close()

	ctx := context.Background()

	// Submit multiple jobs
	for i := uint64(1); i <= 3; i++ {
		hash := types.HexToHash("0x" + string(rune('a'+i)) + "000000000000000000000000000000000000000000000000000000000000000")
		_, err := client.Submit(ctx, hash, i, []byte{})
		if err != nil {
			t.Fatalf("Submit(%d) failed: %v", i, err)
		}
	}

	// Wait for all jobs to complete/fail
	time.Sleep(2 * time.Second)

	// Verify all jobs have a terminal status
	client.mu.Lock()
	for id, job := range client.jobs {
		if job.status != "completed" && job.status != "failed" {
			t.Errorf("Job %s still in status %s", id, job.status)
		}
	}
	client.mu.Unlock()
}

func TestSP1ClientUnknownJob(t *testing.T) {
	client := NewSP1ProverClient("", "", "")
	defer client.Close()

	_, err := client.Status(context.Background(), "nonexistent-job")
	if err == nil {
		t.Fatal("Expected error for unknown job")
	}
}

func TestSP1ClientModeString(t *testing.T) {
	sim := NewSP1ProverClient("", "", "")
	if sim.modeString() != "simulation" {
		t.Errorf("expected simulation mode, got %s", sim.modeString())
	}

	net := NewSP1ProverClient("http://sp1.example.com:50051", "", "")
	if net.modeString() != "network" {
		t.Errorf("expected network mode, got %s", net.modeString())
	}
}
