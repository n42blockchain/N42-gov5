package coprocessor

import (
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
)

func TestTieredVerifierZK(t *testing.T) {
	r := NewRegistry()
	ph := types.HexToHash("0xaabb")
	r.Register(ph, []byte("vk"), "test-zk")

	tv := NewTieredVerifier(r)

	task := &Task{
		ProgramHash:      ph,
		VerificationTier: TierZK,
	}

	ok, err := tv.Verify(task, []byte("proof-data"))
	if err != nil {
		t.Fatalf("ZK verify: %v", err)
	}
	if !ok {
		t.Fatal("ZK verify should return true")
	}
}

func TestTieredVerifierZKRejectsEmpty(t *testing.T) {
	r := NewRegistry()
	ph := types.HexToHash("0xaabb")
	r.Register(ph, []byte("vk"), "test-zk")

	tv := NewTieredVerifier(r)
	task := &Task{ProgramHash: ph, VerificationTier: TierZK}

	_, err := tv.Verify(task, nil)
	if err != ErrInvalidProof {
		t.Fatalf("expected ErrInvalidProof, got %v", err)
	}
}

func TestTieredVerifierOptimistic(t *testing.T) {
	tv := NewTieredVerifier(NewRegistry())

	task := &Task{
		VerificationTier: TierOptimistic,
		Bond:             1_000_000,
	}

	ok, err := tv.Verify(task, []byte("claimed-result"))
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

	_, err := tv.Verify(task, []byte("claimed-result"))
	if err != ErrBondRequired {
		t.Fatalf("expected ErrBondRequired, got %v", err)
	}
}

func TestTieredVerifierTEE(t *testing.T) {
	tv := NewTieredVerifier(NewRegistry())

	task := &Task{VerificationTier: TierTEE}
	attestation := make([]byte, 128) // mock attestation report

	ok, err := tv.Verify(task, attestation)
	if err != nil {
		t.Fatalf("TEE verify: %v", err)
	}
	if !ok {
		t.Fatal("TEE verify should return true")
	}
}

func TestTieredVerifierTEERejectsShort(t *testing.T) {
	tv := NewTieredVerifier(NewRegistry())

	task := &Task{VerificationTier: TierTEE}

	_, err := tv.Verify(task, []byte("short"))
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
	ok, err := tv.Verify(task, []byte("proof"))
	if err != nil || !ok || !called {
		t.Fatal("custom verifier not called correctly")
	}
}

func TestTieredVerifierUnsupportedTier(t *testing.T) {
	tv := NewTieredVerifier(NewRegistry())
	task := &Task{VerificationTier: VerificationTier(99)}

	_, err := tv.Verify(task, []byte("proof"))
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

	ok, err := svc.SubmitProof(taskID, []byte("proof"), []byte("output"))
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

func (m *mockVerifier) Verify(task *Task, proof []byte) (bool, error) {
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

	_, err := tv.Verify(task, []byte("some-proof"))
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
