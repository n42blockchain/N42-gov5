package node

import (
	"testing"

	"github.com/n42blockchain/N42/params"
)

func TestNodeProfileReturnsResolvedDescriptor(t *testing.T) {
	profile, err := params.ResolveExecutionProfile("eth")
	if err != nil {
		t.Fatalf("ResolveExecutionProfile returned error: %v", err)
	}

	n := &Node{profile: profile}
	if got := n.Profile(); got.Name() != params.ExecutionProfileEthereumEL {
		t.Fatalf("profile name = %q, want %q", got.Name(), params.ExecutionProfileEthereumEL)
	}
}
