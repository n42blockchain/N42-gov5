package inference

import (
	"context"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
)

func newTestBackend(t *testing.T) (*PrecompileBackend, *InferenceService, types.Hash) {
	t.Helper()
	models := NewModelRegistry()
	modelHash, err := models.Register("test-model", FormatONNX, 1024, "v1", []string{"text-gen"})
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	svc := NewInferenceService(models)
	svc.SetExecutor(&mockExecutor{output: []byte("result-bytes"), confidence: 0.9})

	cas := NewMemCAS()
	backend := NewPrecompileBackend(svc, cas.Load, func(b []byte) (types.Hash, error) {
		return cas.Store(b)
	})
	return backend, svc, modelHash
}

// TestPrecompileBackend_SubmitRequest_Success covers SubmitRequest's happy
// path: CAS load succeeds, the service accepts the request, and the async
// executeAsync goroutine eventually stores the output via casStore.
func TestPrecompileBackend_SubmitRequest_Success(t *testing.T) {
	backend, svc, modelHash := newTestBackend(t)

	cas := NewMemCAS()
	inputHash, err := cas.Store([]byte("input"))
	if err != nil {
		t.Fatalf("Store() error = %v", err)
	}
	// Make the backend's own casLoad able to find this input too.
	backend.casLoad = cas.Load

	reqID, err := backend.SubmitRequest(modelHash, inputHash, 0, types.HexToAddress("0x01"))
	if err != nil {
		t.Fatalf("SubmitRequest() error = %v", err)
	}
	if reqID == (types.Hash{}) {
		t.Fatal("SubmitRequest() returned zero request id")
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, result, status, err := svc.GetRequest(reqID); err == nil && result != nil && status == RequestOptimisticVerified {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("request did not complete within deadline")
}

// TestPrecompileBackend_SubmitRequest_NoCASLoad covers the guard when no CAS
// load callback is configured.
func TestPrecompileBackend_SubmitRequest_NoCASLoad(t *testing.T) {
	models := NewModelRegistry()
	svc := NewInferenceService(models)
	backend := NewPrecompileBackend(svc, nil, nil)

	_, err := backend.SubmitRequest(types.Hash{}, types.Hash{}, 0, types.Address{})
	if err == nil {
		t.Fatal("SubmitRequest() error = nil, want error when casLoad is nil")
	}
}

// TestPrecompileBackend_SubmitRequest_CASLoadError covers the propagation of
// a CAS-load failure (hash not found in the store).
func TestPrecompileBackend_SubmitRequest_CASLoadError(t *testing.T) {
	backend, _, modelHash := newTestBackend(t)

	_, err := backend.SubmitRequest(modelHash, types.HexToHash("0xdeadbeef"), 0, types.HexToAddress("0x01"))
	if err == nil {
		t.Fatal("SubmitRequest() error = nil, want CAS load error")
	}
}

// TestPrecompileBackend_GetResult_CacheHit covers GetResult reading straight
// from the ResultCache after a completed execution.
func TestPrecompileBackend_GetResult_CacheHit(t *testing.T) {
	backend, svc, modelHash := newTestBackend(t)

	reqID, err := svc.SubmitRequest(modelHash, []byte("input"), types.HexToAddress("0x01"))
	if err != nil {
		t.Fatalf("SubmitRequest() error = %v", err)
	}
	if _, err := svc.Execute(context.Background(), reqID); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	status, outputCAS, err := backend.GetResult(reqID)
	if err != nil {
		t.Fatalf("GetResult() error = %v", err)
	}
	if status == 0 {
		t.Fatal("GetResult() status = 0 (pending), want a completed status")
	}
	if outputCAS == (types.Hash{}) {
		t.Fatal("GetResult() outputCAS is zero, want the output hash")
	}
}

// TestPrecompileBackend_GetResult_UnknownRequest covers the error branch
// when neither the cache nor the tracked-request map know the request id.
func TestPrecompileBackend_GetResult_UnknownRequest(t *testing.T) {
	backend, _, _ := newTestBackend(t)

	_, _, err := backend.GetResult(types.HexToHash("0xunknown"))
	if err == nil {
		t.Fatal("GetResult() error = nil, want error for unknown request")
	}
}

// TestPrecompileBackend_GetModel covers both the found and not-found
// branches.
func TestPrecompileBackend_GetModel(t *testing.T) {
	backend, _, modelHash := newTestBackend(t)

	name, format, caps, err := backend.GetModel(modelHash)
	if err != nil {
		t.Fatalf("GetModel() error = %v", err)
	}
	if name != "test-model" {
		t.Fatalf("name = %q, want test-model", name)
	}
	if format != FormatONNX.String() {
		t.Fatalf("format = %q, want %q", format, FormatONNX.String())
	}
	if caps != "text-gen" {
		t.Fatalf("capabilities = %q, want text-gen", caps)
	}

	_, _, _, err = backend.GetModel(types.HexToHash("0xnotregistered"))
	if err == nil {
		t.Fatal("GetModel() error = nil, want error for unregistered model")
	}
}

// TestPrecompileBackend_ListModels covers both the all-models and
// filter-by-capability branches.
func TestPrecompileBackend_ListModels(t *testing.T) {
	backend, svc, modelHash := newTestBackend(t)

	otherHash, err := svc.Models().Register("other-model", FormatONNX, 512, "v1", []string{"image-gen"})
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	all, err := backend.ListModels("")
	if err != nil {
		t.Fatalf("ListModels(\"\") error = %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("ListModels(\"\") len = %d, want 2", len(all))
	}

	filtered, err := backend.ListModels("text-gen")
	if err != nil {
		t.Fatalf("ListModels(text-gen) error = %v", err)
	}
	if len(filtered) != 1 || filtered[0] != modelHash {
		t.Fatalf("ListModels(text-gen) = %v, want [%x]", filtered, modelHash)
	}

	filtered2, err := backend.ListModels("image-gen")
	if err != nil {
		t.Fatalf("ListModels(image-gen) error = %v", err)
	}
	if len(filtered2) != 1 || filtered2[0] != otherHash {
		t.Fatalf("ListModels(image-gen) = %v, want [%x]", filtered2, otherHash)
	}
}
