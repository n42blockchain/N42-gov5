package conf

import (
	"encoding/json"
	"testing"
)

// TestDefaultZKProverCfg verifies default values.
func TestDefaultZKProverCfg(t *testing.T) {
	cfg := DefaultZKProverCfg()

	if cfg.Enabled {
		t.Fatal("expected Enabled=false by default")
	}
	if cfg.ProverAddr != "localhost:50051" {
		t.Fatalf("ProverAddr: got %s, want localhost:50051", cfg.ProverAddr)
	}
	if cfg.ProverTimeout != 600 {
		t.Fatalf("ProverTimeout: got %d, want 600", cfg.ProverTimeout)
	}
	if cfg.ProofType != "stark" {
		t.Fatalf("ProofType: got %s, want stark", cfg.ProofType)
	}
	if cfg.MaxConcurrent != 2 {
		t.Fatalf("MaxConcurrent: got %d, want 2", cfg.MaxConcurrent)
	}
	if cfg.GuestBinary != "build/bin/zkguest" {
		t.Fatalf("GuestBinary: got %s, want build/bin/zkguest", cfg.GuestBinary)
	}
	if cfg.VerifyOnly {
		t.Fatal("expected VerifyOnly=false by default")
	}
	if cfg.RequireProof {
		t.Fatal("expected RequireProof=false by default")
	}
}

// TestZKProverCfg_JSONRoundTrip verifies JSON serialization.
func TestZKProverCfg_JSONRoundTrip(t *testing.T) {
	cfg := ZKProverCfg{
		Enabled:       true,
		ProverAddr:    "prover.example.com:50051",
		ProverTimeout: 300,
		ProofType:     "snark",
		MaxConcurrent: 4,
		GuestBinary:   "/opt/zkguest",
		VerifyOnly:    true,
		RequireProof:  true,
	}

	data, err := json.Marshal(&cfg)
	if err != nil {
		t.Fatal(err)
	}

	var decoded ZKProverCfg
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}

	if decoded != cfg {
		t.Fatalf("JSON round-trip mismatch: got %+v, want %+v", decoded, cfg)
	}
}

// TestZKProverCfg_JSONTags verifies the JSON field names.
func TestZKProverCfg_JSONTags(t *testing.T) {
	cfg := DefaultZKProverCfg()
	data, err := json.Marshal(&cfg)
	if err != nil {
		t.Fatal(err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}

	expectedKeys := []string{
		"enabled", "prover_addr", "prover_timeout", "proof_type",
		"max_concurrent", "guest_binary", "verify_only", "require_proof",
	}
	for _, key := range expectedKeys {
		if _, ok := m[key]; !ok {
			t.Fatalf("expected JSON key %q not found", key)
		}
	}
}

// TestZKProverCfg_Validate_Disabled verifies disabled config always passes.
func TestZKProverCfg_Validate_Disabled(t *testing.T) {
	cfg := ZKProverCfg{Enabled: false}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("disabled config should validate: %v", err)
	}
}

// TestZKProverCfg_Validate_Default verifies default config passes.
func TestZKProverCfg_Validate_Default(t *testing.T) {
	cfg := DefaultZKProverCfg()
	// Default is disabled, should pass.
	if err := cfg.Validate(); err != nil {
		t.Fatalf("default config should validate: %v", err)
	}

	// Enable and validate with defaults.
	cfg.Enabled = true
	if err := cfg.Validate(); err != nil {
		t.Fatalf("enabled default config should validate: %v", err)
	}
}

// TestZKProverCfg_Validate_InvalidProofType verifies proof type validation.
func TestZKProverCfg_Validate_InvalidProofType(t *testing.T) {
	cfg := DefaultZKProverCfg()
	cfg.Enabled = true
	cfg.ProofType = "plonk"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for invalid proof type")
	}
}

// TestZKProverCfg_Validate_MissingAddr verifies prover address validation.
func TestZKProverCfg_Validate_MissingAddr(t *testing.T) {
	cfg := DefaultZKProverCfg()
	cfg.Enabled = true
	cfg.ProverAddr = ""
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for missing prover address")
	}
}

// TestZKProverCfg_Validate_ZeroConcurrency verifies concurrency validation.
// MaxConcurrent=0 means unlimited — should be valid.
// MaxConcurrent=-1 should be rejected.
func TestZKProverCfg_Validate_ZeroConcurrency(t *testing.T) {
	cfg := DefaultZKProverCfg()
	cfg.Enabled = true

	// Zero means unlimited — should pass validation.
	cfg.MaxConcurrent = 0
	if err := cfg.Validate(); err != nil {
		t.Fatalf("MaxConcurrent=0 (unlimited) should be valid, got: %v", err)
	}

	// Negative should fail.
	cfg.MaxConcurrent = -1
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for negative max concurrent")
	}
}

// TestZKProverCfg_Validate_ZeroTimeout verifies timeout validation.
func TestZKProverCfg_Validate_ZeroTimeout(t *testing.T) {
	cfg := DefaultZKProverCfg()
	cfg.Enabled = true
	cfg.ProverTimeout = 0
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for zero timeout")
	}
}

// TestZKProverCfg_Validate_VerifyOnly verifies that verify-only mode
// doesn't require prover address.
func TestZKProverCfg_Validate_VerifyOnly(t *testing.T) {
	cfg := ZKProverCfg{
		Enabled:    true,
		VerifyOnly: true,
		ProofType:  "stark",
	}
	// VerifyOnly mode should skip prover-specific checks.
	if err := cfg.Validate(); err != nil {
		t.Fatalf("verify-only config should validate: %v", err)
	}
}
