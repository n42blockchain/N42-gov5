package zkprover

import (
	"context"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
)

func testConfig() *conf.ZKProverCfg {
	return &conf.ZKProverCfg{
		Enabled:       true,
		ProverAddr:    "localhost:50051",
		ProverTimeout: 10,
		ProofType:     "stark",
		MaxConcurrent: 3,
		GuestBinary:   "build/bin/zkguest",
	}
}

// TestService_NewService verifies service creation.
func TestService_NewService(t *testing.T) {
	svc, err := NewService(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	if svc == nil {
		t.Fatal("expected non-nil service")
	}
	if svc.PendingJobs() != 0 {
		t.Fatalf("expected 0 pending jobs, got %d", svc.PendingJobs())
	}
}

// TestService_NewServiceNilConfig verifies error on nil config.
func TestService_NewServiceNilConfig(t *testing.T) {
	_, err := NewService(nil)
	if err == nil {
		t.Fatal("expected error for nil config")
	}
}

// TestService_StartStop verifies clean lifecycle.
func TestService_StartStop(t *testing.T) {
	svc, err := NewService(testConfig())
	if err != nil {
		t.Fatal(err)
	}

	svc.Start()
	if !svc.running {
		t.Fatal("Start() did not mark service as running")
	}
	if svc.ctx == nil || svc.ctx.Err() != nil {
		t.Fatal("Start() did not install a live context")
	}
	svc.Stop()
	if svc.running {
		t.Fatal("Stop() did not mark service as stopped")
	}
	if svc.ctx == nil || svc.ctx.Err() == nil {
		t.Fatal("Stop() did not cancel service context")
	}
}

func TestService_RestartRecreatesContext(t *testing.T) {
	svc, err := NewService(testConfig())
	if err != nil {
		t.Fatal(err)
	}

	var submitCtxErr error
	client := &testProverClient{
		submitWithContextFn: func(ctx context.Context, blockHash types.Hash, blockNum uint64) (string, error) {
			submitCtxErr = ctx.Err()
			return "remote-job-restart", submitCtxErr
		},
	}
	svc.SetProverClient(client)

	svc.Start()
	svc.Stop()
	svc.Start()
	defer svc.Stop()

	hash := types.HexToHash("0x1010101010101010101010101010101010101010101010101010101010101010")
	if err := svc.SubmitBlock(hash, 101, []byte("guest-input")); err != nil {
		t.Fatal(err)
	}
	if submitCtxErr != nil {
		t.Fatalf("expected restarted service to use a live context, got %v", submitCtxErr)
	}
}

// TestService_SubmitBlock verifies basic job submission.
func TestService_SubmitBlock(t *testing.T) {
	svc, err := NewService(testConfig())
	if err != nil {
		t.Fatal(err)
	}

	hash := types.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111")
	err = svc.SubmitBlock(hash, 100, []byte("guest-input"))
	if err != nil {
		t.Fatal(err)
	}

	if svc.PendingJobs() != 1 {
		t.Fatalf("expected 1 pending job, got %d", svc.PendingJobs())
	}
}

// TestService_SubmitBlock_Dedup verifies that duplicate submissions are no-ops.
func TestService_SubmitBlock_Dedup(t *testing.T) {
	svc, err := NewService(testConfig())
	if err != nil {
		t.Fatal(err)
	}

	hash := types.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222222")

	for i := 0; i < 5; i++ {
		err = svc.SubmitBlock(hash, 200, []byte("guest-input"))
		if err != nil {
			t.Fatal(err)
		}
	}

	if svc.PendingJobs() != 1 {
		t.Fatalf("expected 1 pending job after dedup, got %d", svc.PendingJobs())
	}
}

// TestService_SubmitBlock_AlreadyProven verifies that blocks with cached proofs
// are skipped.
func TestService_SubmitBlock_AlreadyProven(t *testing.T) {
	svc, err := NewService(testConfig())
	if err != nil {
		t.Fatal(err)
	}

	hash := types.HexToHash("0x3333333333333333333333333333333333333333333333333333333333333333")

	// Store proof first.
	svc.StoreProof(&Proof{
		BlockHash:   hash,
		BlockNumber: 300,
		ProofData:   []byte("proof-data"),
		Type:        ProofTypeSTARK,
	})

	// SubmitBlock should return nil (already proven).
	err = svc.SubmitBlock(hash, 300, []byte("guest-input"))
	if err != nil {
		t.Fatal(err)
	}

	if svc.PendingJobs() != 0 {
		t.Fatalf("expected 0 pending jobs for already-proven block, got %d", svc.PendingJobs())
	}
}

// TestService_SubmitBlock_ConcurrencyLimit verifies max concurrent enforcement.
func TestService_SubmitBlock_ConcurrencyLimit(t *testing.T) {
	cfg := testConfig()
	cfg.MaxConcurrent = 2

	svc, err := NewService(cfg)
	if err != nil {
		t.Fatal(err)
	}

	// Submit 2 blocks (at limit).
	for i := 0; i < 2; i++ {
		hash := types.Hash{}
		hash[0] = byte(i)
		err = svc.SubmitBlock(hash, uint64(i), []byte("input"))
		if err != nil {
			t.Fatalf("submit %d failed: %v", i, err)
		}
	}

	// Third should fail.
	hash3 := types.Hash{}
	hash3[0] = 2
	err = svc.SubmitBlock(hash3, 2, []byte("input"))
	if err == nil {
		t.Fatal("expected error when max concurrent reached")
	}

	if svc.PendingJobs() != 2 {
		t.Fatalf("expected 2 pending jobs, got %d", svc.PendingJobs())
	}
}

// TestService_StoreProof_GetProof verifies proof cache operations.
func TestService_StoreProof_GetProof(t *testing.T) {
	svc, err := NewService(testConfig())
	if err != nil {
		t.Fatal(err)
	}

	hash := types.HexToHash("0x4444444444444444444444444444444444444444444444444444444444444444")
	proof := &Proof{
		BlockHash:    hash,
		BlockNumber:  400,
		ProofData:    []byte("proof-data-stark"),
		PublicInputs: []byte("public-inputs"),
		Type:         ProofTypeSTARK,
	}

	// Before store: not found.
	_, found := svc.GetProof(hash)
	if found {
		t.Fatal("expected not found before StoreProof")
	}

	svc.StoreProof(proof)

	// After store: found.
	got, found := svc.GetProof(hash)
	if !found {
		t.Fatal("expected proof to be found after StoreProof")
	}
	if got.BlockNumber != 400 {
		t.Fatalf("BlockNumber: got %d, want 400", got.BlockNumber)
	}
	if got.Type != ProofTypeSTARK {
		t.Fatalf("Type: got %s, want stark", got.Type)
	}
}

// TestService_StoreProof_Nil verifies nil proof is handled gracefully.
func TestService_StoreProof_Nil(t *testing.T) {
	svc, err := NewService(testConfig())
	if err != nil {
		t.Fatal(err)
	}

	// Should not panic.
	svc.StoreProof(nil)
}

// TestService_CheckJobs_Timeout verifies that timed-out jobs are cleaned up.
func TestService_CheckJobs_Timeout(t *testing.T) {
	cfg := testConfig()
	cfg.ProverTimeout = 1 // 1 second timeout

	svc, err := NewService(cfg)
	if err != nil {
		t.Fatal(err)
	}

	hash := types.HexToHash("0x5555555555555555555555555555555555555555555555555555555555555555")

	// Manually insert a job with old submit time.
	svc.mu.Lock()
	svc.jobs["test-job"] = &jobInfo{
		JobID:      "test-job",
		BlockHash:  hash,
		BlockNum:   500,
		SubmitTime: time.Now().Add(-5 * time.Second), // 5 seconds ago, well past 1s timeout
	}
	svc.mu.Unlock()

	if svc.PendingJobs() != 1 {
		t.Fatalf("expected 1 pending job, got %d", svc.PendingJobs())
	}

	// Run checkJobs to trigger cleanup.
	svc.checkJobs()

	if svc.PendingJobs() != 0 {
		t.Fatalf("expected 0 pending jobs after timeout cleanup, got %d", svc.PendingJobs())
	}
}

// TestService_CancelJob verifies CancelJob removes a pending job.
func TestService_CancelJob(t *testing.T) {
	svc, err := NewService(testConfig())
	if err != nil {
		t.Fatal(err)
	}

	hash := types.HexToHash("0x6666666666666666666666666666666666666666666666666666666666666666")
	if err := svc.SubmitBlock(hash, 600, []byte("input")); err != nil {
		t.Fatal(err)
	}
	if svc.PendingJobs() != 1 {
		t.Fatalf("expected 1 pending, got %d", svc.PendingJobs())
	}

	// Cancel existing job.
	if !svc.CancelJob(hash) {
		t.Fatal("expected CancelJob to return true for existing job")
	}
	if svc.PendingJobs() != 0 {
		t.Fatalf("expected 0 pending after cancel, got %d", svc.PendingJobs())
	}

	// Cancel non-existent job.
	if svc.CancelJob(hash) {
		t.Fatal("expected CancelJob to return false for non-existent job")
	}
}

// TestService_HasPendingJob verifies HasPendingJob queries.
func TestService_HasPendingJob(t *testing.T) {
	svc, err := NewService(testConfig())
	if err != nil {
		t.Fatal(err)
	}

	hash1 := types.HexToHash("0x7777777777777777777777777777777777777777777777777777777777777777")
	hash2 := types.HexToHash("0x8888888888888888888888888888888888888888888888888888888888888888")

	if svc.HasPendingJob(hash1) {
		t.Fatal("expected no pending job before submission")
	}

	if err := svc.SubmitBlock(hash1, 700, []byte("input")); err != nil {
		t.Fatal(err)
	}

	if !svc.HasPendingJob(hash1) {
		t.Fatal("expected pending job after submission")
	}
	if svc.HasPendingJob(hash2) {
		t.Fatal("expected no pending job for different hash")
	}
}

// TestService_GuestInputStored verifies GuestInput is stored in jobInfo.
func TestService_GuestInputStored(t *testing.T) {
	svc, err := NewService(testConfig())
	if err != nil {
		t.Fatal(err)
	}

	hash := types.HexToHash("0x9999999999999999999999999999999999999999999999999999999999999999")
	input := []byte("serialized-guest-input-for-zkvm")
	if err := svc.SubmitBlock(hash, 900, input); err != nil {
		t.Fatal(err)
	}

	// Verify GuestInput is stored.
	svc.mu.RLock()
	defer svc.mu.RUnlock()
	for _, info := range svc.jobs {
		if info.BlockHash == hash {
			if string(info.GuestInput) != string(input) {
				t.Fatalf("GuestInput mismatch: got %q, want %q", info.GuestInput, input)
			}
			return
		}
	}
	t.Fatal("job not found for submitted block")
}

// TestService_StoreProof_RemovesPendingJob verifies StoreProof removes a pending job.
func TestService_StoreProof_RemovesPendingJob(t *testing.T) {
	svc, err := NewService(testConfig())
	if err != nil {
		t.Fatal(err)
	}

	hash := types.HexToHash("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err := svc.SubmitBlock(hash, 1000, []byte("input")); err != nil {
		t.Fatal(err)
	}
	if svc.PendingJobs() != 1 {
		t.Fatalf("expected 1 pending, got %d", svc.PendingJobs())
	}

	svc.StoreProof(&Proof{
		BlockHash:   hash,
		BlockNumber: 1000,
		ProofData:   []byte("proof"),
		Type:        ProofTypeSTARK,
	})

	if svc.PendingJobs() != 0 {
		t.Fatalf("expected 0 pending after StoreProof, got %d", svc.PendingJobs())
	}
	if svc.HasPendingJob(hash) {
		t.Fatal("expected no pending job after StoreProof")
	}
}

// TestService_CheckJobs_MultipleTimeout verifies multiple jobs timeout simultaneously.
func TestService_CheckJobs_MultipleTimeout(t *testing.T) {
	cfg := testConfig()
	cfg.ProverTimeout = 1
	cfg.MaxConcurrent = 10

	svc, err := NewService(cfg)
	if err != nil {
		t.Fatal(err)
	}

	svc.mu.Lock()
	for i := 0; i < 5; i++ {
		hash := types.Hash{}
		hash[0] = byte(i)
		svc.jobs[string(rune('A'+i))] = &jobInfo{
			JobID:      string(rune('A' + i)),
			BlockHash:  hash,
			BlockNum:   uint64(i),
			SubmitTime: time.Now().Add(-10 * time.Second),
		}
	}
	svc.mu.Unlock()

	if svc.PendingJobs() != 5 {
		t.Fatalf("expected 5 pending, got %d", svc.PendingJobs())
	}

	svc.checkJobs()

	if svc.PendingJobs() != 0 {
		t.Fatalf("expected 0 pending after timeout, got %d", svc.PendingJobs())
	}
}

// TestService_CheckJobs_NotTimedOut verifies recent jobs are not cleaned.
func TestService_CheckJobs_NotTimedOut(t *testing.T) {
	cfg := testConfig()
	cfg.ProverTimeout = 600

	svc, err := NewService(cfg)
	if err != nil {
		t.Fatal(err)
	}

	hash := types.Hash{0x01}
	svc.mu.Lock()
	svc.jobs["recent"] = &jobInfo{
		JobID:      "recent",
		BlockHash:  hash,
		BlockNum:   1,
		SubmitTime: time.Now(),
	}
	svc.mu.Unlock()

	svc.checkJobs()

	if svc.PendingJobs() != 1 {
		t.Fatalf("expected recent job to remain, got %d", svc.PendingJobs())
	}
}

// TestService_PendingJobs_AfterOperations verifies PendingJobs count accuracy.
func TestService_PendingJobs_AfterOperations(t *testing.T) {
	cfg := testConfig()
	cfg.MaxConcurrent = 10
	svc, err := NewService(cfg)
	if err != nil {
		t.Fatal(err)
	}

	if svc.PendingJobs() != 0 {
		t.Fatal("expected 0 initially")
	}

	// Submit 3 blocks.
	for i := 0; i < 3; i++ {
		h := types.Hash{}
		h[0] = byte(i + 1)
		svc.SubmitBlock(h, uint64(i), []byte("input"))
	}
	if svc.PendingJobs() != 3 {
		t.Fatalf("expected 3, got %d", svc.PendingJobs())
	}

	// Cancel one.
	h1 := types.Hash{}
	h1[0] = 1
	svc.CancelJob(h1)
	if svc.PendingJobs() != 2 {
		t.Fatalf("expected 2 after cancel, got %d", svc.PendingJobs())
	}

	// Store proof for another.
	h2 := types.Hash{}
	h2[0] = 2
	svc.StoreProof(&Proof{BlockHash: h2, ProofData: []byte("p"), Type: ProofTypeSTARK})
	if svc.PendingJobs() != 1 {
		t.Fatalf("expected 1 after store, got %d", svc.PendingJobs())
	}
}

// TestService_MultipleBlocks verifies managing multiple concurrent jobs.
func TestService_MultipleBlocks(t *testing.T) {
	cfg := testConfig()
	cfg.MaxConcurrent = 10

	svc, err := NewService(cfg)
	if err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 5; i++ {
		hash := types.Hash{}
		hash[31] = byte(i)
		if err := svc.SubmitBlock(hash, uint64(i+100), []byte("input")); err != nil {
			t.Fatalf("submit block %d: %v", i, err)
		}
	}

	if svc.PendingJobs() != 5 {
		t.Fatalf("expected 5 pending jobs, got %d", svc.PendingJobs())
	}

	// Store a proof for one.
	hash0 := types.Hash{}
	hash0[31] = 0
	svc.StoreProof(&Proof{
		BlockHash:   hash0,
		BlockNumber: 100,
		ProofData:   []byte("proof"),
		Type:        ProofTypeSTARK,
	})

	// Verify cached proof is retrievable.
	_, found := svc.GetProof(hash0)
	if !found {
		t.Fatal("expected proof for block 100")
	}
}

// TestNoopProverClient verifies NoopProverClient behavior.
func TestNoopProverClient(t *testing.T) {
	client := &NoopProverClient{}

	// Submit should return error.
	_, err := client.Submit(nil, types.Hash{}, 0, nil)
	if err == nil {
		t.Fatal("expected error from NoopProverClient.Submit")
	}

	// Status should return nil proof.
	proof, err := client.Status(nil, "test-id")
	if err != nil {
		t.Fatalf("unexpected error from Status: %v", err)
	}
	if proof != nil {
		t.Fatal("expected nil proof from NoopProverClient.Status")
	}

	// Close should succeed.
	if err := client.Close(); err != nil {
		t.Fatalf("unexpected error from Close: %v", err)
	}
}

// TestService_SetProverClient verifies SetProverClient is used by SubmitBlock.
func TestService_SetProverClient(t *testing.T) {
	svc, err := NewService(testConfig())
	if err != nil {
		t.Fatal(err)
	}

	submitted := false
	client := &testProverClient{
		submitFn: func(blockHash types.Hash, blockNum uint64) (string, error) {
			submitted = true
			return "remote-job-123", nil
		},
	}
	svc.SetProverClient(client)

	hash := types.HexToHash("0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	if err := svc.SubmitBlock(hash, 42, []byte("input")); err != nil {
		t.Fatal(err)
	}

	if !submitted {
		t.Fatal("expected prover client Submit to be called")
	}
}

// TestService_CheckJobs_WithClient verifies checkJobs polls the client.
func TestService_CheckJobs_WithClient(t *testing.T) {
	cfg := testConfig()
	cfg.ProverTimeout = 600
	svc, err := NewService(cfg)
	if err != nil {
		t.Fatal(err)
	}

	hash := types.HexToHash("0xcccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc")

	client := &testProverClient{
		statusFn: func(jobID string) (*Proof, error) {
			return &Proof{
				ProofData: []byte("completed-proof"),
				Type:      ProofTypeSTARK,
			}, nil
		},
	}
	svc.SetProverClient(client)

	// Manually add a job.
	svc.mu.Lock()
	svc.jobs["job-1"] = &jobInfo{
		JobID:      "job-1",
		BlockHash:  hash,
		BlockNum:   100,
		SubmitTime: time.Now(),
	}
	svc.mu.Unlock()

	svc.checkJobs()

	// Proof should now be in cache.
	proof, found := svc.GetProof(hash)
	if !found {
		t.Fatal("expected proof to be cached after checkJobs")
	}
	if string(proof.ProofData) != "completed-proof" {
		t.Fatalf("unexpected proof data: %s", proof.ProofData)
	}
	if svc.PendingJobs() != 0 {
		t.Fatalf("expected 0 pending after proof received, got %d", svc.PendingJobs())
	}
}

// testProverClient is a test implementation of ProverClient.
type testProverClient struct {
	submitFn            func(blockHash types.Hash, blockNum uint64) (string, error)
	submitWithContextFn func(ctx context.Context, blockHash types.Hash, blockNum uint64) (string, error)
	statusFn            func(jobID string) (*Proof, error)
}

func (c *testProverClient) Submit(ctx context.Context, blockHash types.Hash, blockNum uint64, _ []byte) (string, error) {
	if c.submitWithContextFn != nil {
		return c.submitWithContextFn(ctx, blockHash, blockNum)
	}
	if c.submitFn != nil {
		return c.submitFn(blockHash, blockNum)
	}
	return "", nil
}

func (c *testProverClient) Status(_ context.Context, jobID string) (*Proof, error) {
	if c.statusFn != nil {
		return c.statusFn(jobID)
	}
	return nil, nil
}

func (c *testProverClient) Close() error { return nil }

// TestService_SubmitBlock_UnlimitedConcurrency verifies MaxConcurrent=0 means unlimited.
func TestService_SubmitBlock_UnlimitedConcurrency(t *testing.T) {
	cfg := testConfig()
	cfg.MaxConcurrent = 0 // unlimited

	svc, err := NewService(cfg)
	if err != nil {
		t.Fatal(err)
	}

	// Submit many blocks — should all succeed.
	for i := 0; i < 100; i++ {
		hash := types.Hash{}
		hash[0] = byte(i)
		hash[1] = byte(i >> 8)
		if err := svc.SubmitBlock(hash, uint64(i), []byte("input")); err != nil {
			t.Fatalf("submit %d failed: %v", i, err)
		}
	}

	if svc.PendingJobs() != 100 {
		t.Fatalf("expected 100 pending jobs, got %d", svc.PendingJobs())
	}
}

// TestService_ProvingDuration verifies that proving duration is tracked
// when a proof is received from the prover client.
func TestService_ProvingDuration(t *testing.T) {
	cfg := testConfig()
	cfg.ProverTimeout = 600
	svc, err := NewService(cfg)
	if err != nil {
		t.Fatal(err)
	}

	hash := types.Hash{0xDD}

	client := &testProverClient{
		statusFn: func(jobID string) (*Proof, error) {
			return &Proof{
				ProofData: []byte("proof"),
				Type:      ProofTypeSTARK,
			}, nil
		},
	}
	svc.SetProverClient(client)

	// Insert job with a submit time 2 seconds ago.
	svc.mu.Lock()
	svc.jobs["duration-test"] = &jobInfo{
		JobID:      "duration-test",
		BlockHash:  hash,
		BlockNum:   999,
		SubmitTime: time.Now().Add(-2 * time.Second),
	}
	svc.mu.Unlock()

	svc.checkJobs()

	// Proof should be stored.
	_, found := svc.GetProof(hash)
	if !found {
		t.Fatal("expected proof to be cached")
	}
	if svc.PendingJobs() != 0 {
		t.Fatalf("expected 0 pending, got %d", svc.PendingJobs())
	}
}
