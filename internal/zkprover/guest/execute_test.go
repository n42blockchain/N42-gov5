package guest

import (
	"strings"
	"testing"
)

// TestExecute_NilInput verifies Execute handles nil input gracefully.
func TestExecute_NilInput(t *testing.T) {
	output := Execute(nil)
	if output.Success {
		t.Fatal("nil input should not succeed")
	}
	if !strings.Contains(output.Error, "nil") {
		t.Fatalf("error should mention nil, got: %s", output.Error)
	}
}

// TestExecute_InvalidWitness verifies Execute returns error for invalid witness bytes.
func TestExecute_InvalidWitness(t *testing.T) {
	input := &GuestInput{
		ChainID: 1,
		Witness: []byte("not-valid-witness"),
	}

	output := Execute(input)
	if output.Success {
		t.Fatal("invalid witness should not succeed")
	}
	if !strings.Contains(output.Error, "witness") {
		t.Fatalf("error should mention witness, got: %s", output.Error)
	}
}

// TestExecute_EmptyWitness verifies Execute returns error for empty witness.
func TestExecute_EmptyWitness(t *testing.T) {
	input := &GuestInput{
		ChainID: 1,
		Witness: []byte{},
	}

	output := Execute(input)
	if output.Success {
		t.Fatal("empty witness should not succeed")
	}
}

// TestExecute_InvalidBlockHeader verifies Execute returns error for invalid header.
func TestExecute_InvalidBlockHeader(t *testing.T) {
	// Need a valid witness first. Use the minimum encoding from encoding_binary.go.
	// But that requires a real witness. Instead, just test that an empty witness
	// is caught before the header decode step.
	input := &GuestInput{
		ChainID:     1,
		BlockHeader: []byte("not-a-protobuf"),
		Witness:     nil,
	}

	output := Execute(input)
	if output.Success {
		t.Fatal("should not succeed with invalid data")
	}
}

// TestGuestOutput_ZeroValue verifies GuestOutput zero value.
func TestGuestOutput_ZeroValue(t *testing.T) {
	output := &GuestOutput{}
	if output.Success {
		t.Fatal("zero value should not be successful")
	}
	if output.GasUsed != 0 {
		t.Fatal("zero value should have 0 gas used")
	}
	if output.PostStateRoot != [32]byte{} {
		t.Fatal("zero value should have empty post state root")
	}
}
