package inference

import (
	"context"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
)

type mockExecutor struct {
	output     []byte
	confidence float64
}

func (m *mockExecutor) Execute(_ context.Context, modelHash types.Hash, input []byte) (*InferenceResult, error) {
	return &InferenceResult{
		Output:       m.output,
		Confidence:   m.confidence,
		ModelVersion: "v1",
		GasUsed:      1000,
		ComputedAt:   time.Now(),
	}, nil
}

func TestModelRegistryLifecycle(t *testing.T) {
	r := NewModelRegistry()

	hash, err := r.Register("gpt-mini", FormatONNX, 1024*1024, "v1.0", []string{"text-gen", "embedding"})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	m, ok := r.Get(hash)
	if !ok {
		t.Fatal("model not found")
	}
	if m.Name != "gpt-mini" {
		t.Fatalf("name = %q, want %q", m.Name, "gpt-mini")
	}
	if m.Format != FormatONNX {
		t.Fatalf("format = %v, want ONNX", m.Format)
	}

	// Idempotent
	hash2, _ := r.Register("gpt-mini", FormatONNX, 1024*1024, "v1.0", nil)
	if hash2 != hash {
		t.Fatal("same model should produce same hash")
	}
	if r.Count() != 1 {
		t.Fatalf("count = %d, want 1", r.Count())
	}

	// Find by capability
	models := r.FindByCapability("embedding")
	if len(models) != 1 {
		t.Fatalf("found %d models with embedding, want 1", len(models))
	}

	models = r.FindByCapability("image-gen")
	if len(models) != 0 {
		t.Fatalf("found %d models with image-gen, want 0", len(models))
	}
}

func TestModelRegistryEmptyName(t *testing.T) {
	r := NewModelRegistry()
	_, err := r.Register("", FormatONNX, 0, "", nil)
	if err == nil {
		t.Fatal("expected error for empty name")
	}
}

func TestInferenceServiceSubmitAndExecute(t *testing.T) {
	models := NewModelRegistry()
	modelHash, _ := models.Register("test-model", FormatSafeTensors, 512, "v1", nil)

	svc := NewInferenceService(models)
	svc.SetExecutor(&mockExecutor{output: []byte("result"), confidence: 0.95})

	// Submit request
	reqID, err := svc.SubmitRequest(modelHash, []byte("input-data"), types.HexToAddress("0x01"))
	if err != nil {
		t.Fatalf("SubmitRequest: %v", err)
	}

	// Execute
	result, err := svc.Execute(context.Background(), reqID)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if string(result.Output) != "result" {
		t.Fatalf("output = %q, want %q", string(result.Output), "result")
	}
	if result.Confidence != 0.95 {
		t.Fatalf("confidence = %f, want 0.95", result.Confidence)
	}

	// Should be optimistic-verified (opML)
	_, _, status, _ := svc.GetRequest(reqID)
	if status != RequestOptimisticVerified {
		t.Fatalf("status = %v, want OptimisticVerified", status)
	}
}

func TestInferenceServiceFinalize(t *testing.T) {
	models := NewModelRegistry()
	modelHash, _ := models.Register("test", FormatONNX, 256, "v1", nil)

	svc := NewInferenceService(models)
	svc.SetExecutor(&mockExecutor{output: []byte("ok"), confidence: 0.99})

	reqID, _ := svc.SubmitRequest(modelHash, []byte("x"), types.Address{})
	svc.Execute(context.Background(), reqID)

	err := svc.FinalizeRequest(reqID)
	if err != nil {
		t.Fatalf("FinalizeRequest: %v", err)
	}

	_, _, status, _ := svc.GetRequest(reqID)
	if status != RequestVerified {
		t.Fatalf("status = %v, want Verified", status)
	}
}

func TestInferenceServiceChallenge(t *testing.T) {
	models := NewModelRegistry()
	modelHash, _ := models.Register("test", FormatONNX, 256, "v1", nil)

	svc := NewInferenceService(models)
	svc.SetExecutor(&mockExecutor{output: []byte("ok"), confidence: 0.5})

	reqID, _ := svc.SubmitRequest(modelHash, []byte("x"), types.Address{})
	svc.Execute(context.Background(), reqID)

	err := svc.ChallengeRequest(reqID)
	if err != nil {
		t.Fatalf("ChallengeRequest: %v", err)
	}

	_, _, status, _ := svc.GetRequest(reqID)
	if status != RequestChallenged {
		t.Fatalf("status = %v, want Challenged", status)
	}
}

func TestInferenceServiceUnregisteredModel(t *testing.T) {
	svc := NewInferenceService(NewModelRegistry())

	_, err := svc.SubmitRequest(types.HexToHash("0xff"), []byte("x"), types.Address{})
	if err == nil {
		t.Fatal("expected error for unregistered model")
	}
}

func TestInferenceServiceNoExecutor(t *testing.T) {
	models := NewModelRegistry()
	modelHash, _ := models.Register("test", FormatONNX, 256, "v1", nil)

	svc := NewInferenceService(models) // no executor set

	reqID, _ := svc.SubmitRequest(modelHash, []byte("x"), types.Address{})
	_, err := svc.Execute(context.Background(), reqID)
	if err == nil {
		t.Fatal("expected error with no executor")
	}
}

func TestInferenceServiceCounts(t *testing.T) {
	models := NewModelRegistry()
	modelHash, _ := models.Register("test", FormatONNX, 256, "v1", nil)

	svc := NewInferenceService(models)
	svc.SetExecutor(&mockExecutor{output: []byte("ok"), confidence: 0.9})

	svc.SubmitRequest(modelHash, []byte("a"), types.Address{})
	svc.SubmitRequest(modelHash, []byte("b"), types.Address{})

	if svc.TotalCount() != 2 {
		t.Fatalf("total = %d, want 2", svc.TotalCount())
	}
	if svc.PendingCount() != 2 {
		t.Fatalf("pending = %d, want 2", svc.PendingCount())
	}
}

func TestInferenceModelFormatString(t *testing.T) {
	cases := []struct {
		format ModelFormat
		want   string
	}{
		{FormatONNX, "onnx"},
		{FormatSafeTensors, "safetensors"},
		{FormatCustom, "custom"},
		{ModelFormat(99), "unknown"},
	}
	for _, tc := range cases {
		if got := tc.format.String(); got != tc.want {
			t.Errorf("ModelFormat(%d).String() = %q, want %q", tc.format, got, tc.want)
		}
	}
}

func TestModelRegistryUnregister(t *testing.T) {
	r := NewModelRegistry()

	// Register a model
	hash, err := r.Register("my-model", FormatCustom, 512, "v2", []string{"classify"})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if r.Count() != 1 {
		t.Fatalf("count = %d, want 1", r.Count())
	}

	// Unregister the existing model
	if err := r.Unregister(hash); err != nil {
		t.Fatalf("Unregister: %v", err)
	}
	if r.Count() != 0 {
		t.Fatalf("count = %d, want 0 after unregister", r.Count())
	}

	_, ok := r.Get(hash)
	if ok {
		t.Fatal("model should not be found after unregister")
	}

	// Unregister non-existent model should return an error
	err = r.Unregister(types.HexToHash("0xdeadbeef"))
	if err == nil {
		t.Fatal("expected error when unregistering non-existent model")
	}
}

func TestRequestStatusString(t *testing.T) {
	cases := []struct {
		status RequestStatus
		want   string
	}{
		{RequestPending, "pending"},
		{RequestProcessing, "processing"},
		{RequestOptimisticVerified, "optimistic_verified"},
		{RequestVerified, "verified"},
		{RequestFailed, "failed"},
		{RequestChallenged, "challenged"},
		{RequestStatus(255), "unknown"},
	}
	for _, tc := range cases {
		if got := tc.status.String(); got != tc.want {
			t.Errorf("RequestStatus(%d).String() = %q, want %q", tc.status, got, tc.want)
		}
	}
}

func TestInferenceServicePurgeCompleted(t *testing.T) {
	models := NewModelRegistry()
	modelHash, _ := models.Register("test", FormatONNX, 256, "v1", nil)

	svc := NewInferenceService(models)
	svc.SetExecutor(&mockExecutor{output: []byte("ok"), confidence: 0.9})

	// Submit 3 requests
	req1, _ := svc.SubmitRequest(modelHash, []byte("a"), types.Address{})
	req2, _ := svc.SubmitRequest(modelHash, []byte("b"), types.Address{})
	req3, _ := svc.SubmitRequest(modelHash, []byte("c"), types.Address{})

	// Execute req1 and req2
	svc.Execute(context.Background(), req1)
	svc.Execute(context.Background(), req2)

	// Finalize req1 (moves to RequestVerified)
	svc.FinalizeRequest(req1)

	// req3 is still pending, req2 is optimistic_verified, req1 is verified
	if svc.TotalCount() != 3 {
		t.Fatalf("total before purge = %d, want 3", svc.TotalCount())
	}

	purged := svc.PurgeCompleted()
	// Only req1 (verified) should be purged. req2 is optimistic_verified (not purged), req3 is pending.
	if purged != 1 {
		t.Fatalf("purged = %d, want 1", purged)
	}
	if svc.TotalCount() != 2 {
		t.Fatalf("total after purge = %d, want 2", svc.TotalCount())
	}

	// Verify req3 (pending) still exists
	_, _, status, err := svc.GetRequest(req3)
	if err != nil {
		t.Fatalf("GetRequest(req3): %v", err)
	}
	if status != RequestPending {
		t.Fatalf("req3 status = %v, want pending", status)
	}

	// Verify req2 (optimistic_verified) still exists
	_, _, status, err = svc.GetRequest(req2)
	if err != nil {
		t.Fatalf("GetRequest(req2): %v", err)
	}
	if status != RequestOptimisticVerified {
		t.Fatalf("req2 status = %v, want optimistic_verified", status)
	}
}

func TestInferenceServiceListRequests(t *testing.T) {
	models := NewModelRegistry()
	modelHash, _ := models.Register("test", FormatONNX, 256, "v1", nil)

	svc := NewInferenceService(models)

	svc.SubmitRequest(modelHash, []byte("a"), types.Address{})
	svc.SubmitRequest(modelHash, []byte("b"), types.Address{})
	svc.SubmitRequest(modelHash, []byte("c"), types.Address{})

	list := svc.ListRequests()
	if len(list) != 3 {
		t.Fatalf("ListRequests len = %d, want 3", len(list))
	}

	// All should be pending
	for _, item := range list {
		if item.Status != RequestPending {
			t.Fatalf("request %s status = %v, want pending", item.ID.Hex()[:10], item.Status)
		}
	}
}

func TestInferenceServiceDoubleExecute(t *testing.T) {
	models := NewModelRegistry()
	modelHash, _ := models.Register("test", FormatONNX, 256, "v1", nil)

	svc := NewInferenceService(models)
	svc.SetExecutor(&mockExecutor{output: []byte("ok"), confidence: 0.9})

	reqID, _ := svc.SubmitRequest(modelHash, []byte("x"), types.Address{})

	// First execute should succeed
	_, err := svc.Execute(context.Background(), reqID)
	if err != nil {
		t.Fatalf("first Execute: %v", err)
	}

	// Second execute should fail (not in pending state)
	_, err = svc.Execute(context.Background(), reqID)
	if err == nil {
		t.Fatal("expected error on double execute")
	}
}

func TestInferenceServiceDoubleFinalize(t *testing.T) {
	models := NewModelRegistry()
	modelHash, _ := models.Register("test", FormatONNX, 256, "v1", nil)

	svc := NewInferenceService(models)
	svc.SetExecutor(&mockExecutor{output: []byte("ok"), confidence: 0.99})

	reqID, _ := svc.SubmitRequest(modelHash, []byte("x"), types.Address{})
	svc.Execute(context.Background(), reqID)

	// First finalize should succeed
	err := svc.FinalizeRequest(reqID)
	if err != nil {
		t.Fatalf("first FinalizeRequest: %v", err)
	}

	// Second finalize should fail (already verified, not optimistic_verified)
	err = svc.FinalizeRequest(reqID)
	if err == nil {
		t.Fatal("expected error on double finalize")
	}
}

func TestInferenceServiceChallengeVerified(t *testing.T) {
	models := NewModelRegistry()
	modelHash, _ := models.Register("test", FormatONNX, 256, "v1", nil)

	svc := NewInferenceService(models)
	svc.SetExecutor(&mockExecutor{output: []byte("ok"), confidence: 0.99})

	reqID, _ := svc.SubmitRequest(modelHash, []byte("x"), types.Address{})
	svc.Execute(context.Background(), reqID)

	// Finalize the request (moves to Verified)
	svc.FinalizeRequest(reqID)

	// Challenging a verified request should fail (not optimistic_verified)
	err := svc.ChallengeRequest(reqID)
	if err == nil {
		t.Fatal("expected error challenging verified request")
	}
}

func TestModelRegistryList(t *testing.T) {
	r := NewModelRegistry()

	r.Register("model-a", FormatONNX, 1024, "v1", []string{"text-gen"})
	r.Register("model-b", FormatSafeTensors, 2048, "v1", []string{"embedding"})
	r.Register("model-c", FormatCustom, 512, "v2", []string{"classify"})

	models := r.List()
	if len(models) != 3 {
		t.Fatalf("List len = %d, want 3", len(models))
	}

	// Verify all names are present
	names := make(map[string]bool)
	for _, m := range models {
		names[m.Name] = true
	}
	for _, want := range []string{"model-a", "model-b", "model-c"} {
		if !names[want] {
			t.Fatalf("model %q not found in List()", want)
		}
	}
}

func TestModelRegistryFindByCapabilityMultiple(t *testing.T) {
	r := NewModelRegistry()

	r.Register("model-a", FormatONNX, 1024, "v1", []string{"embedding", "text-gen"})
	r.Register("model-b", FormatSafeTensors, 2048, "v1", []string{"embedding", "classify"})
	r.Register("model-c", FormatCustom, 512, "v2", []string{"classify"})

	// Both model-a and model-b have "embedding"
	models := r.FindByCapability("embedding")
	if len(models) != 2 {
		t.Fatalf("FindByCapability('embedding') len = %d, want 2", len(models))
	}

	// Both model-b and model-c have "classify"
	models = r.FindByCapability("classify")
	if len(models) != 2 {
		t.Fatalf("FindByCapability('classify') len = %d, want 2", len(models))
	}

	// Only model-a has "text-gen"
	models = r.FindByCapability("text-gen")
	if len(models) != 1 {
		t.Fatalf("FindByCapability('text-gen') len = %d, want 1", len(models))
	}
}

func TestInferenceServiceGetRequestNotFound(t *testing.T) {
	svc := NewInferenceService(NewModelRegistry())

	_, _, _, err := svc.GetRequest(types.HexToHash("0xdeadbeef"))
	if err == nil {
		t.Fatal("expected error for non-existent request")
	}
}
