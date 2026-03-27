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
	if !p.SupportsBridgeRuntime() {
		t.Fatal("expected n42 profile to support bridge runtime")
	}
	if !p.SupportsDistributedRuntime() {
		t.Fatal("expected n42 profile to support distributed runtime")
	}
	if !p.SupportsAIRuntime() {
		t.Fatal("expected n42 profile to support AI runtime")
	}
	if !p.SupportsMCPRuntime() {
		t.Fatal("expected n42 profile to support MCP runtime")
	}
	if !p.SupportsWeb3GatewayRuntime() {
		t.Fatal("expected n42 profile to support web3 gateway runtime")
	}
	if !p.SupportsDeveloperRuntime() {
		t.Fatal("expected n42 profile to support developer runtime")
	}
	if !p.SupportsZKProofAPI() {
		t.Fatal("expected n42 profile to support zk proof API")
	}
	if !p.SupportsConfiguredChain("private") || !p.SupportsConfiguredChain("mainnet") || !p.SupportsConfiguredChain("testnet") {
		t.Fatal("expected n42 profile to support configured n42 chains")
	}
	if !p.SupportsConsensus(AposConsensu) || !p.SupportsConsensus(HotStuffConsensus) {
		t.Fatal("expected n42 profile to support n42 consensus engines")
	}
	if !p.SupportsConsensus(CliqueConsensus) || !p.SupportsConsensus(Faker) {
		t.Fatal("expected n42 profile to support shared consensus engines")
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
			if p.SupportsBridgeRuntime() {
				t.Fatal("expected ethereum EL profile not to support bridge runtime")
			}
			if p.SupportsDistributedRuntime() {
				t.Fatal("expected ethereum EL profile not to support distributed runtime")
			}
			if p.SupportsAIRuntime() {
				t.Fatal("expected ethereum EL profile not to support AI runtime")
			}
			if p.SupportsMCPRuntime() {
				t.Fatal("expected ethereum EL profile not to support MCP runtime")
			}
			if !p.SupportsWeb3GatewayRuntime() {
				t.Fatal("expected ethereum EL profile to support web3 gateway runtime")
			}
			if !p.SupportsDeveloperRuntime() {
				t.Fatal("expected ethereum EL profile to support developer runtime")
			}
			if p.SupportsZKProofAPI() {
				t.Fatal("expected ethereum EL profile not to support zk proof API")
			}
			if !p.SupportsConfiguredChain("private") {
				t.Fatal("expected ethereum EL profile to support private chain")
			}
			if p.SupportsConfiguredChain("mainnet") || p.SupportsConfiguredChain("testnet") {
				t.Fatal("expected ethereum EL profile not to support n42 canonical chains")
			}
			if p.SupportsConsensus(AposConsensu) || p.SupportsConsensus(HotStuffConsensus) {
				t.Fatal("expected ethereum EL profile not to support n42 consensus engines")
			}
			if !p.SupportsConsensus(CliqueConsensus) || !p.SupportsConsensus(Faker) {
				t.Fatal("expected ethereum EL profile to support shared consensus engines")
			}
		})
	}
}

func TestResolveExecutionProfileRejectsUnknown(t *testing.T) {
	if _, err := ResolveExecutionProfile("mystery"); err == nil {
		t.Fatal("expected error for unknown profile")
	}
}
