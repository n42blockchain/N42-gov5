package params

import "testing"

func TestResolveExecutionProfileDefaultsToN42(t *testing.T) {
	p, err := ResolveExecutionProfile("")
	if err != nil {
		t.Fatalf("ResolveExecutionProfile returned error: %v", err)
	}
	if p.Name() != ExecutionProfileN42 {
		t.Fatalf("name = %q, want %q", p.Name(), ExecutionProfileN42)
	}
	if !p.IsN42() {
		t.Fatal("expected default profile to be n42")
	}
	if p.String() != string(ExecutionProfileN42) {
		t.Fatalf("string = %q, want %q", p.String(), ExecutionProfileN42)
	}
}

func TestResolveExecutionProfileEthereumAliases(t *testing.T) {
	tests := []string{"eth-el", "eth", "ethereum", "ethereum-el"}
	for _, input := range tests {
		t.Run(input, func(t *testing.T) {
			p, err := ResolveExecutionProfile(input)
			if err != nil {
				t.Fatalf("ResolveExecutionProfile returned error: %v", err)
			}
			if p.Name() != ExecutionProfileEthereumEL {
				t.Fatalf("name = %q, want %q", p.Name(), ExecutionProfileEthereumEL)
			}
			if !p.IsEthereumEL() {
				t.Fatal("expected ethereum EL profile")
			}
			if p.String() != string(ExecutionProfileEthereumEL) {
				t.Fatalf("string = %q, want %q", p.String(), ExecutionProfileEthereumEL)
			}
		})
	}
}

func TestResolveExecutionProfileRejectsUnknown(t *testing.T) {
	if _, err := ResolveExecutionProfile("mystery"); err == nil {
		t.Fatal("expected error for unknown profile")
	}
}
