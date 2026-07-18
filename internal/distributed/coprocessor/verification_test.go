package coprocessor

import (
	"errors"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
)

func TestTieredVerifierZK(t *testing.T) {
	r := NewRegistry()
	ph := types.HexToHash("0xaabb")
	r.Register(ph, []byte("vk"), "test-zk")

	tv := NewTieredVerifier(r)

	input := []byte("task-input")
	outputs := []byte("task-outputs")
	task := &Task{
		ProgramHash:      ph,
		InputHash:        crypto.Keccak256Hash(input),
		VerificationTier: TierZK,
	}

	proof := BuildZKProofEnvelope(ph, task.InputHash, outputs, []byte("succinct-blob"))
	ok, err := tv.Verify(task, proof, outputs)
	if err != nil {
		t.Fatalf("ZK verify: %v", err)
	}
	if !ok {
		t.Fatal("ZK verify should return true")
	}
}

func TestTieredVerifierZKRejectsBindingMismatch(t *testing.T) {
	r := NewRegistry()
	ph := types.HexToHash("0xaabb")
	r.Register(ph, []byte("vk"), "test-zk")
	tv := NewTieredVerifier(r)

	outputs := []byte("task-outputs")
	task := &Task{
		ProgramHash:      ph,
		InputHash:        crypto.Keccak256Hash([]byte("task-input")),
		VerificationTier: TierZK,
	}

	// Envelope bound to a DIFFERENT program: replaying someone else's valid
	// proof against this task must fail.
	otherProgram := types.HexToHash("0xffff")
	proof := BuildZKProofEnvelope(otherProgram, task.InputHash, outputs, []byte("blob"))
	if _, err := tv.Verify(task, proof, outputs); !errors.Is(err, ErrProofBindingMismatch) {
		t.Fatalf("foreign-program envelope: err=%v want ErrProofBindingMismatch", err)
	}

	// Correct header but the claimed outputs were swapped after proving.
	proof = BuildZKProofEnvelope(ph, task.InputHash, outputs, []byte("blob"))
	if _, err := tv.Verify(task, proof, []byte("different-outputs")); !errors.Is(err, ErrProofBindingMismatch) {
		t.Fatalf("swapped outputs: err=%v want ErrProofBindingMismatch", err)
	}
}

func TestTieredVerifierZKRejectsEmpty(t *testing.T) {
	r := NewRegistry()
	ph := types.HexToHash("0xaabb")
	r.Register(ph, []byte("vk"), "test-zk")

	tv := NewTieredVerifier(r)
	task := &Task{ProgramHash: ph, VerificationTier: TierZK}

	if _, err := tv.Verify(task, nil, nil); err != ErrInvalidProof {
		t.Fatalf("expected ErrInvalidProof, got %v", err)
	}
	// A bare header with no succinct blob is not a proof either.
	bare := BuildZKProofEnvelope(ph, types.Hash{}, nil, nil)
	if _, err := tv.Verify(task, bare, nil); err != ErrInvalidProof {
		t.Fatalf("blobless envelope: expected ErrInvalidProof, got %v", err)
	}
}

func TestTieredVerifierZKBlobBackend(t *testing.T) {
	r := NewRegistry()
	ph := types.HexToHash("0xaabb")
	r.Register(ph, []byte("vk"), "test-zk")
	tv := NewTieredVerifier(r)

	var gotVK, gotBlob []byte
	tv.SetZKBlobVerifier(func(program *Program, blob []byte) (bool, error) {
		gotVK, gotBlob = program.VerificationKey, blob
		return false, nil // backend says the succinct proof is bogus
	})

	outputs := []byte("outputs")
	task := &Task{ProgramHash: ph, VerificationTier: TierZK}
	proof := BuildZKProofEnvelope(ph, task.InputHash, outputs, []byte("bogus-blob"))
	ok, err := tv.Verify(task, proof, outputs)
	if ok || err != nil {
		t.Fatalf("backend rejection must propagate: ok=%v err=%v", ok, err)
	}
	if string(gotVK) != "vk" || string(gotBlob) != "bogus-blob" {
		t.Fatalf("backend got vk=%q blob=%q", gotVK, gotBlob)
	}
}

func TestTieredVerifierOptimistic(t *testing.T) {
	tv := NewTieredVerifier(NewRegistry())

	task := &Task{
		VerificationTier: TierOptimistic,
		Bond:             1_000_000,
	}

	claimed := []byte("claimed-result")
	ok, err := tv.Verify(task, claimed, claimed)
	if err != nil {
		t.Fatalf("Optimistic verify: %v", err)
	}
	if !ok {
		t.Fatal("Optimistic verify should return true")
	}
}

func TestTieredVerifierOptimisticRequiresBond(t *testing.T) {
	tv := NewTieredVerifier(NewRegistry())

	task := &Task{
		VerificationTier: TierOptimistic,
		Bond:             0, // no bond
	}

	claimed := []byte("claimed-result")
	_, err := tv.Verify(task, claimed, claimed)
	if err != ErrBondRequired {
		t.Fatalf("expected ErrBondRequired, got %v", err)
	}
}

func TestTieredVerifierOptimisticRejectsDivergentOutputs(t *testing.T) {
	tv := NewTieredVerifier(NewRegistry())
	task := &Task{VerificationTier: TierOptimistic, Bond: 1_000_000}

	// The claimed result IS the challenge target — two diverging copies would
	// let the provider point at whichever survives a dispute.
	_, err := tv.Verify(task, []byte("result-A"), []byte("result-B"))
	if err != ErrResultOutputsMismatch {
		t.Fatalf("expected ErrResultOutputsMismatch, got %v", err)
	}
}

func TestTieredVerifierTEEFailsClosed(t *testing.T) {
	tv := NewTieredVerifier(NewRegistry())

	task := &Task{VerificationTier: TierTEE}
	attestation := make([]byte, 128) // structurally plausible quote

	// No quote verifier configured: accepting an unchecked quote would be a
	// bond-free optimistic tier, so the tier must reject.
	if _, err := tv.Verify(task, attestation, nil); err != ErrTEEVerifierUnavailable {
		t.Fatalf("expected ErrTEEVerifierUnavailable, got %v", err)
	}

	called := false
	tv.SetTEEQuoteVerifier(func(task *Task, quote, publicOutputs []byte) (bool, error) {
		called = true
		return true, nil
	})
	ok, err := tv.Verify(task, attestation, nil)
	if err != nil || !ok || !called {
		t.Fatalf("configured quote verifier not honored: ok=%v err=%v called=%v", ok, err, called)
	}
}

func TestTieredVerifierTEERejectsShort(t *testing.T) {
	tv := NewTieredVerifier(NewRegistry())

	task := &Task{VerificationTier: TierTEE}

	_, err := tv.Verify(task, []byte("short"), nil)
	if err != ErrInvalidAttestation {
		t.Fatalf("expected ErrInvalidAttestation, got %v", err)
	}
}

func TestTieredVerifierCustom(t *testing.T) {
	tv := NewTieredVerifier(NewRegistry())

	// Register custom verifier
	called := false
	tv.RegisterVerifier(VerificationTier(99), &mockVerifier{
		fn: func(task *Task, proof []byte) (bool, error) {
			called = true
			return true, nil
		},
	})

	task := &Task{VerificationTier: VerificationTier(99)}
	ok, err := tv.Verify(task, []byte("proof"), nil)
	if err != nil || !ok || !called {
		t.Fatal("custom verifier not called correctly")
	}
}

func TestTieredVerifierUnsupportedTier(t *testing.T) {
	tv := NewTieredVerifier(NewRegistry())
	task := &Task{VerificationTier: VerificationTier(99)}

	_, err := tv.Verify(task, []byte("proof"), nil)
	if err == nil {
		t.Fatal("expected error for unsupported tier")
	}
}

func TestServiceOptimisticFlow(t *testing.T) {
	cfg := testConfig()
	cfg.OptimisticChallengeSec = 1
	svc, err := NewService(cfg)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	svc.Start()

	ph := types.HexToHash("0xee")
	svc.Registry().Register(ph, []byte("vk"), "test-prog")

	taskID, err := svc.SubmitTask(ph, []byte("input"), types.Address{})
	if err != nil {
		t.Fatalf("SubmitTask: %v", err)
	}

	// Set optimistic tier and bond on the task
	svc.Tasks().mu.Lock()
	task := svc.Tasks().tasks[taskID]
	task.VerificationTier = TierOptimistic
	task.Bond = 1_000_000
	svc.Tasks().mu.Unlock()

	claimed := []byte("claimed-output")
	ok, err := svc.SubmitProof(taskID, claimed, claimed)
	if err != nil || !ok {
		t.Fatalf("SubmitProof: ok=%v err=%v", ok, err)
	}

	// Check optimistic-verified status safely under lock
	svc.Tasks().mu.RLock()
	status := svc.Tasks().tasks[taskID].Status
	svc.Tasks().mu.RUnlock()
	if status != TaskOptimisticVerified {
		t.Fatalf("status = %v, want OptimisticVerified", status)
	}

	// Wait for challenge window to expire + maintenance loop
	time.Sleep(2 * time.Second)

	// Stop service to prevent concurrent writes before reading
	svc.Stop()

	// Task should now be fully verified
	task, _ = svc.Tasks().GetTask(taskID)
	if task.Status != TaskVerified {
		t.Fatalf("status = %v, want Verified after challenge window", task.Status)
	}
}

type mockVerifier struct {
	fn func(task *Task, proof []byte) (bool, error)
}

func (m *mockVerifier) Verify(task *Task, proof, publicOutputs []byte) (bool, error) {
	return m.fn(task, proof)
}

func TestTieredVerifierZKUnregisteredProgram(t *testing.T) {
	r := NewRegistry()
	// Do NOT register any program; use a hash with no entry
	tv := NewTieredVerifier(r)

	task := &Task{
		ProgramHash:      types.HexToHash("0xdeadbeef"),
		VerificationTier: TierZK,
	}

	proof := BuildZKProofEnvelope(task.ProgramHash, task.InputHash, nil, []byte("blob"))
	_, err := tv.Verify(task, proof, nil)
	if err != ErrProgramNotRegistered {
		t.Fatalf("expected ErrProgramNotRegistered, got %v", err)
	}
}

func TestVerificationTierString(t *testing.T) {
	cases := []struct {
		tier VerificationTier
		want string
	}{
		{TierZK, "zk"},
		{TierOptimistic, "optimistic"},
		{TierTEE, "tee"},
		{VerificationTier(99), "unknown"},
	}
	for _, tc := range cases {
		if got := tc.tier.String(); got != tc.want {
			t.Errorf("VerificationTier(%d).String() = %q, want %q", tc.tier, got, tc.want)
		}
	}
}

func TestCapabilityString(t *testing.T) {
	cases := []struct {
		cap  Capability
		want string
	}{
		{CapZK, "zk"},
		{CapWASM, "wasm"},
		{CapAI, "ai"},
		{CapGeneral, "general"},
		{Capability(99), "unknown"},
	}
	for _, tc := range cases {
		if got := tc.cap.String(); got != tc.want {
			t.Errorf("Capability(%d).String() = %q, want %q", tc.cap, got, tc.want)
		}
	}
}
